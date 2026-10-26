package dns

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Adatage/ShardDNS/internal/store"
)

const (
	maxChain      = 8
	maxAdditional = 16
	typeIXFR      = 251
)

type Handler struct {
	Resolver store.Resolver
	Logger   *slog.Logger
	rotate   atomic.Uint32
}

func NewHandler(r store.Resolver, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Resolver: r, Logger: logger}
}

func (h *Handler) Handle(ctx context.Context, req *Message) *Message {
	resp := NewResponse(req)

	if req.Opcode() != 0 {
		SetRcode(resp, RcodeNotImplemented)
		return resp
	}
	if len(req.Questions) != 1 {
		SetRcode(resp, RcodeFormatError)
		return resp
	}
	q := req.Questions[0]
	if q.Class != ClassIN && q.Class != ClassANY {
		SetRcode(resp, RcodeRefused)
		return resp
	}
	switch q.Type {
	case TypeOPT:
		SetRcode(resp, RcodeFormatError)
		return resp
	case TypeAXFR, typeIXFR:
		SetRcode(resp, RcodeRefused)
		return resp
	}

	qname := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	if qname == "" {
		SetRcode(resp, RcodeRefused)
		return resp
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	zone, err := h.Resolver.FindZone(ctx, qname)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			SetRcode(resp, RcodeRefused)
			return resp
		}
		h.Logger.ErrorContext(ctx, "find zone failed", "name", qname, "err", err)
		SetRcode(resp, RcodeServerFailure)
		return resp
	}

	resp.Flags |= FlagAA
	if err := h.resolve(ctx, resp, zone, qname, strings.TrimSuffix(q.Name, "."), q.Type); err != nil {
		h.Logger.ErrorContext(ctx, "resolve failed", "zone", zone.Name, "name", qname, "type", TypeName(q.Type), "err", err)
		resp.Answers, resp.Authority, resp.Additional = nil, nil, nil
		resp.Flags &^= FlagAA
		SetRcode(resp, RcodeServerFailure)
	}
	return resp
}

type match struct {
	records []*store.Record
	exists  bool
	cutName string
	cut     []*store.Record
	dname   *store.Record
	dnameAt string
}

func (h *Handler) resolve(ctx context.Context, resp *Message, zone *store.Zone, name, owner string, qtype uint16) error {
	for hops := 0; hops <= maxChain; hops++ {
		m, err := h.lookup(ctx, zone, name, qtype)
		if err != nil {
			return err
		}

		if m.cut != nil {
			resp.Flags &^= FlagAA
			for _, r := range m.cut {
				h.appendRR(&resp.Authority, m.cutName, r)
			}
			h.addGlue(ctx, resp, zone, m.cut)
			return nil
		}

		if m.dname != nil {
			h.appendRR(&resp.Answers, m.dnameAt, m.dname)
			target := RDataNames(TypeDNAME, wireOf(m.dname))
			if target == "" {
				return nil
			}
			next := strings.TrimSuffix(name, "."+m.dnameAt) + "." + strings.ToLower(target)
			if len(next) > 253 {
				SetRcode(resp, RcodeServerFailure)
				return nil
			}
			cname, err := PackName(nil, next)
			if err != nil {
				return nil
			}
			resp.Answers = append(resp.Answers, RR{Name: owner, Type: TypeCNAME, Class: ClassIN, TTL: uint32(m.dname.TTL), RData: cname})
			nz, ok, err := h.zoneFor(ctx, next)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			zone, name, owner = nz, next, next
			continue
		}

		apex := name == zone.Name
		records := m.records

		if qtype == TypeANY {
			if apex {
				if soa, err := h.soaRR(zone); err == nil {
					resp.Answers = append(resp.Answers, soa)
				}
			}
			h.appendSet(&resp.Answers, owner, records)
			if len(resp.Answers) == 0 {
				h.negative(resp, zone, m.exists || apex)
			}
			return nil
		}

		if apex && qtype == TypeSOA {
			soa, err := h.soaRR(zone)
			if err != nil {
				return err
			}
			resp.Answers = append(resp.Answers, soa)
			return nil
		}

		matched := filterType(records, qtype)
		if len(matched) > 0 {
			h.appendSet(&resp.Answers, owner, matched)
			h.addAdditional(ctx, resp, zone, qtype, matched)
			return nil
		}

		if qtype != TypeCNAME {
			if cnames := filterType(records, TypeCNAME); len(cnames) > 0 {
				h.appendSet(&resp.Answers, owner, cnames)
				target := strings.ToLower(RDataNames(TypeCNAME, wireOf(cnames[0])))
				if target == "" {
					return nil
				}
				nz, ok, err := h.zoneFor(ctx, target)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
				zone, name, owner = nz, target, target
				continue
			}
		}

		h.negative(resp, zone, len(records) > 0 || m.exists || apex)
		return nil
	}
	return nil
}

func (h *Handler) lookup(ctx context.Context, zone *store.Zone, name string, qtype uint16) (*match, error) {
	set, err := h.Resolver.LookupName(ctx, zone.Name, name)
	if err != nil {
		return nil, err
	}
	if len(set.Records) > 0 {
		if name != zone.Name && qtype != TypeDS {
			if ns := filterType(set.Records, TypeNS); len(ns) > 0 {
				return &match{cutName: name, cut: ns}, nil
			}
		}
		return &match{records: set.Records, exists: true}, nil
	}
	if set.HasChildren || name == zone.Name {
		return &match{exists: true}, nil
	}

	encloser := name
	for encloser != zone.Name {
		i := strings.IndexByte(encloser, '.')
		if i < 0 {
			break
		}
		encloser = encloser[i+1:]
		aset, err := h.Resolver.LookupName(ctx, zone.Name, encloser)
		if err != nil {
			return nil, err
		}
		if encloser != zone.Name {
			if ns := filterType(aset.Records, TypeNS); len(ns) > 0 {
				return &match{cutName: encloser, cut: ns}, nil
			}
			if dn := filterType(aset.Records, TypeDNAME); len(dn) > 0 {
				return &match{dname: dn[0], dnameAt: encloser}, nil
			}
		}
		if aset.Exists() || encloser == zone.Name {
			break
		}
	}

	wset, err := h.Resolver.LookupName(ctx, zone.Name, "*."+encloser)
	if err != nil {
		return nil, err
	}
	if len(wset.Records) > 0 {
		return &match{records: wset.Records, exists: true}, nil
	}
	return &match{}, nil
}

func (h *Handler) zoneFor(ctx context.Context, name string) (*store.Zone, bool, error) {
	z, err := h.Resolver.FindZone(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return z, true, nil
}

func (h *Handler) negative(resp *Message, zone *store.Zone, exists bool) {
	if !exists {
		SetRcode(resp, RcodeNXDomain)
	}
	if soa, err := h.soaRR(zone); err == nil {
		if soa.TTL > uint32(zone.MinTTL) {
			soa.TTL = uint32(zone.MinTTL)
		}
		resp.Authority = append(resp.Authority, soa)
	} else {
		h.Logger.Warn("invalid SOA parameters", "zone", zone.Name, "err", err)
	}
}

func (h *Handler) soaRR(zone *store.Zone) (RR, error) {
	admin := strings.Replace(zone.AdminEmail, "@", ".", 1)
	rdata, err := BuildSOA(zone.PrimaryNS, admin, uint32(zone.Serial),
		uint32(zone.Refresh), uint32(zone.Retry), uint32(zone.Expire), uint32(zone.MinTTL))
	if err != nil {
		return RR{}, err
	}
	return RR{Name: zone.Name, Type: TypeSOA, Class: ClassIN, TTL: uint32(zone.MinTTL), RData: rdata}, nil
}

func (h *Handler) appendSet(section *[]RR, owner string, records []*store.Record) {
	n := len(records)
	if n == 0 {
		return
	}
	start := 0
	if n > 1 {
		start = int(h.rotate.Add(1) % uint32(n))
	}
	for i := 0; i < n; i++ {
		h.appendRR(section, owner, records[(start+i)%n])
	}
}

func (h *Handler) appendRR(section *[]RR, owner string, r *store.Record) {
	typ := TypeCode(r.Type)
	wire := wireOf(r)
	if typ == 0 || wire == nil {
		h.Logger.Warn("skipping unservable record", "zone", r.Zone, "name", r.Name, "type", r.Type, "err", r.WireErr)
		return
	}
	*section = append(*section, RR{Name: owner, Type: typ, Class: ClassIN, TTL: uint32(r.TTL), RData: wire})
}

func wireOf(r *store.Record) []byte {
	if r.Wire != nil {
		return r.Wire
	}
	if r.WireErr != nil {
		return nil
	}
	wire, err := BuildRData(TypeCode(r.Type), r.RData)
	if err != nil {
		return nil
	}
	return wire
}

func filterType(records []*store.Record, qtype uint16) []*store.Record {
	var out []*store.Record
	for _, r := range records {
		if TypeCode(r.Type) == qtype {
			out = append(out, r)
		}
	}
	return out
}

func inZone(name, zone string) bool {
	return name == zone || strings.HasSuffix(name, "."+zone)
}

func (h *Handler) addAdditional(ctx context.Context, resp *Message, zone *store.Zone, qtype uint16, records []*store.Record) {
	switch qtype {
	case TypeMX, TypeNS, TypeSRV:
		h.addGlue(ctx, resp, zone, records)
	}
}

func (h *Handler) addGlue(ctx context.Context, resp *Message, zone *store.Zone, records []*store.Record) {
	seen := make(map[string]struct{}, len(records))
	for _, r := range records {
		if len(seen) >= maxAdditional {
			return
		}
		target := strings.ToLower(RDataNames(TypeCode(r.Type), wireOf(r)))
		if target == "" || !inZone(target, zone.Name) {
			continue
		}
		if _, dup := seen[target]; dup {
			continue
		}
		seen[target] = struct{}{}
		set, err := h.Resolver.LookupName(ctx, zone.Name, target)
		if err != nil {
			continue
		}
		for _, a := range set.Records {
			if a.Type == "A" || a.Type == "AAAA" {
				h.appendRR(&resp.Additional, target, a)
			}
		}
	}
}
