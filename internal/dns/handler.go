package dns

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Adatage/ShardDNS/internal/store"
)

type Handler struct {
	Store  *store.Store
	Logger *slog.Logger
}

func NewHandler(s *store.Store, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Store: s, Logger: logger}
}

func (h *Handler) Handle(ctx context.Context, req *Message) *Message {
	resp := NewResponse(req)

	if len(req.Questions) == 0 {
		SetRcode(resp, RcodeFormatError)
		return resp
	}
	if opcode := (req.Flags >> 11) & 0x0F; opcode != 0 {
		SetRcode(resp, RcodeNotImplemented)
		return resp
	}

	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	q := req.Questions[0]
	qname := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	if qname == "" {
		SetRcode(resp, RcodeRefused)
		return resp
	}

	zone, err := h.findZone(qctx, qname)
	if err != nil {
		h.Logger.ErrorContext(qctx, "findZone failed", "name", qname, "err", err)
		SetRcode(resp, RcodeServerFailure)
		return resp
	}
	if zone == "" {
		SetRcode(resp, RcodeRefused)
		return resp
	}

	resp.Flags |= FlagAA

	if q.Type == TypeANY {
		h.answerANY(qctx, resp, zone, qname)
		return resp
	}

	qtypeName := TypeName(q.Type)
	records, err := h.Store.LookupRecords(qctx, zone, qname, qtypeName)
	if err != nil {
		h.Logger.ErrorContext(qctx, "LookupRecords failed",
			"zone", zone, "name", qname, "type", qtypeName, "err", err)
		SetRcode(resp, RcodeServerFailure)
		return resp
	}

	if q.Type == TypeSOA {
		h.appendRecords(resp, &resp.Answers, records, qname)
		if len(resp.Answers) == 0 {
			// No SOA stored for this name. Determine whether the name exists at all
			// so we can return NXDOMAIN vs NODATA correctly (RFC 2308).
			exists, err := h.Store.NameExists(qctx, zone, qname)
			if err != nil {
				h.Logger.WarnContext(qctx, "NameExists failed", "zone", zone, "name", qname, "err", err)
				SetRcode(resp, RcodeServerFailure)
				return resp
			}
			if exists {
				h.setNOData(qctx, resp, zone)
			} else {
				h.setNXDomain(qctx, resp, zone)
			}
		}
		return resp
	}

	if len(records) == 0 {
		if q.Type != TypeCNAME {
			cnames, err := h.Store.LookupRecords(qctx, zone, qname, "CNAME")
			if err != nil {
				// A DB error here must not silently become NXDOMAIN.
				h.Logger.ErrorContext(qctx, "LookupRecords (CNAME) failed",
					"zone", zone, "name", qname, "err", err)
				SetRcode(resp, RcodeServerFailure)
				return resp
			}
			if len(cnames) > 0 {
				h.appendRecords(resp, &resp.Answers, cnames, qname)
				target := strings.TrimSuffix(strings.ToLower(cnames[0].RData), ".")
				if target != "" && strings.HasSuffix(target, zone) {
					more, err := h.Store.LookupRecords(qctx, zone, target, qtypeName)
					if err == nil && len(more) > 0 {
						h.appendRecords(resp, &resp.Answers, more, target)
					}
				}
				return resp
			}
		}

		// No records of the queried type and no CNAME. Use NameExists to
		// distinguish a true NXDOMAIN (name absent) from NODATA (name present,
		// type absent) so we never cache a false NXDOMAIN for a real name.
		exists, err := h.Store.NameExists(qctx, zone, qname)
		if err != nil {
			h.Logger.WarnContext(qctx, "NameExists failed", "zone", zone, "name", qname, "err", err)
			SetRcode(resp, RcodeServerFailure)
			return resp
		}
		if exists {
			h.setNOData(qctx, resp, zone)
		} else {
			h.setNXDomain(qctx, resp, zone)
		}
		return resp
	}

	h.appendRecords(resp, &resp.Answers, records, qname)
	return resp
}

func (h *Handler) answerANY(ctx context.Context, resp *Message, zone, qname string) {
	records, err := h.Store.LookupAllTypes(ctx, zone, qname)
	if err != nil {
		h.Logger.ErrorContext(ctx, "LookupAllTypes failed",
			"zone", zone, "name", qname, "err", err)
		SetRcode(resp, RcodeServerFailure)
		return
	}
	if len(records) == 0 {
		h.setNXDomain(ctx, resp, zone)
		return
	}
	h.appendRecords(resp, &resp.Answers, records, qname)
}

func (h *Handler) appendRecords(resp *Message, section *[]RR, records []*store.Record, qname string) {
	for _, r := range records {
		typ := TypeCode(r.Type)
		if typ == 0 {
			h.Logger.Warn("skipping record with unknown type",
				"zone", r.Zone, "name", r.Name, "type", r.Type)
			continue
		}
		rdata, err := BuildRData(typ, r.RData)
		if err != nil {
			h.Logger.Warn("skipping record with invalid rdata",
				"zone", r.Zone, "name", r.Name, "type", r.Type, "err", err)
			continue
		}
		name := qname
		if name == "" {
			name = r.Zone
		}
		*section = append(*section, RR{
			Name:  name,
			Type:  typ,
			Class: ClassIN,
			TTL:   uint32(r.TTL),
			RData: rdata,
		})
	}
}

func (h *Handler) setNXDomain(ctx context.Context, resp *Message, zone string) {
	SetRcode(resp, RcodeNXDomain)
	h.appendSOAAuthority(ctx, resp, zone)
}

// setNOData sends a NODATA response (NOERROR, empty ANSWER, SOA in AUTHORITY).
// Use this when the queried name exists in the zone but has no records of the
// requested type (RFC 2308 §2.2).
func (h *Handler) setNOData(ctx context.Context, resp *Message, zone string) {
	h.appendSOAAuthority(ctx, resp, zone)
}

func (h *Handler) appendSOAAuthority(ctx context.Context, resp *Message, zone string) {
	soa, err := h.Store.GetSOA(ctx, zone)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.Logger.WarnContext(ctx, "GetSOA failed", "zone", zone, "err", err)
		}
		return
	}
	rdata, err := BuildRData(TypeSOA, soa.RData)
	if err != nil {
		h.Logger.WarnContext(ctx, "invalid SOA rdata", "zone", zone, "err", err)
		return
	}
	resp.Authority = append(resp.Authority, RR{
		Name:  zone,
		Type:  TypeSOA,
		Class: ClassIN,
		TTL:   uint32(soa.TTL),
		RData: rdata,
	})
}

func (h *Handler) findZone(ctx context.Context, qname string) (string, error) {
	name := qname
	for {
		z, err := h.Store.GetZone(ctx, name)
		if err == nil {
			return z.Name, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return "", nil
		}
		name = name[i+1:]
		if name == "" {
			return "", nil
		}
	}
}
