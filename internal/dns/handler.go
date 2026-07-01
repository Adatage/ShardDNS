package dns

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Adatage/ShardDNS/internal/store"
)

// Handler answers DNS queries out of the ScyllaDB store. It contains no
// per-query cache — every query is a fresh point-select.
type Handler struct {
	Store  *store.Store
	Logger *slog.Logger
}

// NewHandler constructs a Handler.
func NewHandler(s *store.Store, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Store: s, Logger: logger}
}

// Handle processes a request and returns a response Message. Every query
// gets its own 2-second budget derived from ctx.
func (h *Handler) Handle(ctx context.Context, req *Message) *Message {
	resp := NewResponse(req)

	// Malformed / unsupported opcodes.
	if len(req.Questions) == 0 {
		SetRcode(resp, RcodeFormatError)
		return resp
	}
	// We only implement OPCODE 0 (standard query). Bits 11-14 of Flags.
	if opcode := (req.Flags >> 11) & 0x0F; opcode != 0 {
		SetRcode(resp, RcodeNotImplemented)
		return resp
	}

	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Only handle the first question — RFC 1035 allows only one question in
	// practice; every real server does the same.
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

	// Mark authoritative.
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

	// SOA queries at the apex are served directly from the SOA row.
	if q.Type == TypeSOA {
		h.appendRecords(resp, &resp.Answers, records, qname)
		if len(resp.Answers) == 0 {
			h.setNXDomain(qctx, resp, zone)
		}
		return resp
	}

	if len(records) == 0 {
		// CNAME chase (one hop only — chains are rare in authoritative data).
		if q.Type != TypeCNAME {
			cnames, err := h.Store.LookupRecords(qctx, zone, qname, "CNAME")
			if err == nil && len(cnames) > 0 {
				h.appendRecords(resp, &resp.Answers, cnames, qname)
				// Resolve the CNAME target *within the same zone* one hop.
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

		h.setNXDomain(qctx, resp, zone)
		return resp
	}

	h.appendRecords(resp, &resp.Answers, records, qname)
	return resp
}

// answerANY returns every record for (zone, qname).
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

// appendRecords converts store.Record values into wire-format RRs and
// appends them to the target section.
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
		// Emit the qname (not the stored "@") so responses look natural.
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

// setNXDomain sets NXDOMAIN and adds the zone's SOA to the Authority
// section so resolvers can cache the negative response (RFC 2308).
func (h *Handler) setNXDomain(ctx context.Context, resp *Message, zone string) {
	SetRcode(resp, RcodeNXDomain)
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

// findZone finds the deepest zone (longest suffix) that matches qname by
// stripping labels one at a time and probing the zones table.
//
// This is a hot path — but zone lookups are cheap point-selects and there
// are usually very few label strips before a hit (or a definitive miss).
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
