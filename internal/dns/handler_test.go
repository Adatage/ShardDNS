package dns

import (
	"context"
	"strings"
	"testing"

	"github.com/Adatage/ShardDNS/internal/store"
)

type fakeResolver struct {
	zones   map[string]*store.Zone
	records map[string][]*store.Record
}

func newFake() *fakeResolver {
	f := &fakeResolver{zones: map[string]*store.Zone{}, records: map[string][]*store.Record{}}
	f.zones["example.com"] = &store.Zone{
		Name: "example.com", PrimaryNS: "ns1.example.com.", AdminEmail: "hostmaster.example.com.",
		Serial: 2026010101, Refresh: 3600, Retry: 900, Expire: 604800, MinTTL: 300,
	}
	f.zones["other.net"] = &store.Zone{
		Name: "other.net", PrimaryNS: "ns1.other.net.", AdminEmail: "admin@other.net",
		Serial: 1, Refresh: 3600, Retry: 900, Expire: 604800, MinTTL: 120,
	}
	return f
}

func (f *fakeResolver) add(zone, name, typ, rdata string) {
	f.records[zone+"|"+name] = append(f.records[zone+"|"+name], &store.Record{Zone: zone, Name: name, Type: typ, TTL: 60, RData: rdata})
}

func (f *fakeResolver) FindZone(_ context.Context, qname string) (*store.Zone, error) {
	for name := qname; name != ""; {
		if z, ok := f.zones[name]; ok {
			return z, nil
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			break
		}
		name = name[i+1:]
	}
	return nil, store.ErrNotFound
}

func (f *fakeResolver) LookupName(_ context.Context, zone, name string) (*store.NameSet, error) {
	set := &store.NameSet{Records: f.records[zone+"|"+name]}
	if len(set.Records) == 0 {
		for k := range f.records {
			if strings.HasPrefix(k, zone+"|") && strings.HasSuffix(k, "."+name) {
				set.HasChildren = true
			}
		}
	}
	return set, nil
}

func query(t *testing.T, h *Handler, name string, qtype uint16) *Message {
	t.Helper()
	req := &Message{Header: Header{ID: 7, Flags: FlagRD}, Questions: []Question{{Name: name, Type: qtype, Class: ClassIN}}}
	wire, err := req.Pack(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMessage(wire)
	if err != nil {
		t.Fatal(err)
	}
	resp := h.Handle(context.Background(), parsed)
	out, err := resp.Pack(nil)
	if err != nil {
		t.Fatal(err)
	}
	final, err := ParseMessage(out)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func testHandler() (*Handler, *fakeResolver) {
	f := newFake()
	f.add("example.com", "www.example.com", "A", "192.0.2.1")
	f.add("example.com", "www.example.com", "A", "192.0.2.2")
	f.add("example.com", "www.example.com", "AAAA", "2001:db8::1")
	f.add("example.com", "example.com", "NS", "ns1.example.com.")
	f.add("example.com", "example.com", "MX", "10 mail.example.com.")
	f.add("example.com", "ns1.example.com", "A", "192.0.2.53")
	f.add("example.com", "mail.example.com", "A", "192.0.2.25")
	f.add("example.com", "alias.example.com", "CNAME", "www.example.com.")
	f.add("example.com", "chain.example.com", "CNAME", "alias.example.com.")
	f.add("example.com", "loop1.example.com", "CNAME", "loop2.example.com.")
	f.add("example.com", "loop2.example.com", "CNAME", "loop1.example.com.")
	f.add("example.com", "ext.example.com", "CNAME", "host.other.net.")
	f.add("example.com", "gone.example.com", "CNAME", "missing.example.com.")
	f.add("other.net", "host.other.net", "A", "198.51.100.7")
	f.add("example.com", "*.wild.example.com", "A", "192.0.2.99")
	f.add("example.com", "real.wild.example.com", "TXT", "exists")
	f.add("example.com", "_sip._tcp.example.com", "SRV", "10 5 5060 sip.example.com.")
	f.add("example.com", "sub.example.com", "NS", "ns.sub.example.com.")
	f.add("example.com", "ns.sub.example.com", "A", "192.0.2.77")
	f.add("example.com", "legacy.example.com", "DNAME", "example.net.")
	f.add("example.com", "txt.example.com", "TXT", `"v=spf1 -all"`)
	return NewHandler(f, nil), f
}

func TestHandlerAnswers(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "WWW.Example.COM", TypeA)
	if r.Rcode() != RcodeNoError || r.Flags&FlagAA == 0 || len(r.Answers) != 2 {
		t.Fatalf("A: rcode %d flags %x answers %d", r.Rcode(), r.Flags, len(r.Answers))
	}
	if r.Questions[0].Name != "WWW.Example.COM" || r.Answers[0].Name != "WWW.Example.COM" {
		t.Fatalf("query case not echoed: %q / %q", r.Questions[0].Name, r.Answers[0].Name)
	}

	r = query(t, h, "www.example.com", TypeAAAA)
	if len(r.Answers) != 1 {
		t.Fatalf("AAAA answers %d", len(r.Answers))
	}

	r = query(t, h, "txt.example.com", TypeTXT)
	if len(r.Answers) != 1 || string(r.Answers[0].RData) != "\x0bv=spf1 -all" {
		t.Fatalf("TXT: %+v", r.Answers)
	}
}

func TestHandlerNegative(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "www.example.com", TypeMX)
	if r.Rcode() != RcodeNoError || len(r.Answers) != 0 || len(r.Authority) != 1 || r.Authority[0].Type != TypeSOA {
		t.Fatalf("NODATA: rcode %d answers %d authority %d", r.Rcode(), len(r.Answers), len(r.Authority))
	}
	if r.Authority[0].TTL != 300 {
		t.Fatalf("negative TTL %d", r.Authority[0].TTL)
	}

	r = query(t, h, "nope.example.com", TypeA)
	if r.Rcode() != RcodeNXDomain || len(r.Authority) != 1 {
		t.Fatalf("NXDOMAIN: rcode %d authority %d", r.Rcode(), len(r.Authority))
	}

	r = query(t, h, "_tcp.example.com", TypeA)
	if r.Rcode() != RcodeNoError || len(r.Answers) != 0 {
		t.Fatalf("empty non-terminal must be NODATA, got rcode %d", r.Rcode())
	}

	r = query(t, h, "example.org", TypeA)
	if r.Rcode() != RcodeRefused || r.Flags&FlagAA != 0 {
		t.Fatalf("out of zone: rcode %d", r.Rcode())
	}

	r = query(t, h, "evilexample.com", TypeA)
	if r.Rcode() != RcodeRefused {
		t.Fatalf("suffix lookalike must be refused, got %d", r.Rcode())
	}
}

func TestHandlerApex(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "example.com", TypeSOA)
	if len(r.Answers) != 1 || r.Answers[0].Type != TypeSOA {
		t.Fatalf("SOA answers %d", len(r.Answers))
	}
	want, _ := BuildSOA("ns1.example.com.", "hostmaster.example.com.", 2026010101, 3600, 900, 604800, 300)
	if string(r.Answers[0].RData) != string(want) {
		t.Fatalf("SOA rdata mismatch")
	}

	r = query(t, h, "other.net", TypeSOA)
	want, _ = BuildSOA("ns1.other.net.", "admin.other.net", 1, 3600, 900, 604800, 120)
	if string(r.Answers[0].RData) != string(want) {
		t.Fatalf("admin@ not converted")
	}

	r = query(t, h, "example.com", TypeAAAA)
	if r.Rcode() != RcodeNoError || len(r.Answers) != 0 || len(r.Authority) != 1 {
		t.Fatalf("apex NODATA: rcode %d", r.Rcode())
	}

	r = query(t, h, "example.com", TypeANY)
	types := map[uint16]bool{}
	for _, a := range r.Answers {
		types[a.Type] = true
	}
	if !types[TypeSOA] || !types[TypeNS] || !types[TypeMX] {
		t.Fatalf("ANY missing types: %v", types)
	}
}

func TestHandlerCNAME(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "chain.example.com", TypeA)
	if len(r.Answers) != 4 || r.Answers[0].Type != TypeCNAME || r.Answers[1].Type != TypeCNAME {
		t.Fatalf("chain answers: %+v", r.Answers)
	}

	r = query(t, h, "alias.example.com", TypeCNAME)
	if len(r.Answers) != 1 || r.Answers[0].Type != TypeCNAME {
		t.Fatalf("explicit CNAME query: %+v", r.Answers)
	}

	r = query(t, h, "ext.example.com", TypeA)
	if len(r.Answers) != 2 || r.Answers[1].Type != TypeA {
		t.Fatalf("cross-zone CNAME should chase into other served zone: %+v", r.Answers)
	}

	r = query(t, h, "loop1.example.com", TypeA)
	if r.Rcode() != RcodeNoError || len(r.Answers) == 0 || len(r.Answers) > maxChain+2 {
		t.Fatalf("loop not bounded: %d answers", len(r.Answers))
	}

	r = query(t, h, "gone.example.com", TypeA)
	if r.Rcode() != RcodeNXDomain || len(r.Answers) != 1 {
		t.Fatalf("dangling CNAME: rcode %d answers %d", r.Rcode(), len(r.Answers))
	}
}

func TestHandlerWildcard(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "anything.wild.example.com", TypeA)
	if len(r.Answers) != 1 || r.Answers[0].Name != "anything.wild.example.com" {
		t.Fatalf("wildcard synth: %+v", r.Answers)
	}

	r = query(t, h, "a.b.wild.example.com", TypeA)
	if len(r.Answers) != 1 {
		t.Fatalf("deep wildcard: %+v", r.Answers)
	}

	r = query(t, h, "real.wild.example.com", TypeA)
	if r.Rcode() != RcodeNoError || len(r.Answers) != 0 {
		t.Fatalf("existing name must not match wildcard: rcode %d answers %d", r.Rcode(), len(r.Answers))
	}

	r = query(t, h, "anything.wild.example.com", TypeAAAA)
	if r.Rcode() != RcodeNoError || len(r.Answers) != 0 {
		t.Fatalf("wildcard NODATA: rcode %d", r.Rcode())
	}

	r = query(t, h, "x.example.com", TypeA)
	if r.Rcode() != RcodeNXDomain {
		t.Fatalf("no wildcard here: rcode %d", r.Rcode())
	}
}

func TestHandlerDelegationAndGlue(t *testing.T) {
	h, _ := testHandler()

	r := query(t, h, "host.sub.example.com", TypeA)
	if r.Flags&FlagAA != 0 || len(r.Answers) != 0 || len(r.Authority) != 1 || r.Authority[0].Type != TypeNS {
		t.Fatalf("referral: flags %x answers %d authority %+v", r.Flags, len(r.Answers), r.Authority)
	}
	if len(r.Additional) != 1 || r.Additional[0].Type != TypeA {
		t.Fatalf("glue: %+v", r.Additional)
	}

	r = query(t, h, "example.com", TypeMX)
	if len(r.Answers) != 1 || len(r.Additional) != 1 || r.Additional[0].Name != "mail.example.com" {
		t.Fatalf("MX additional: %+v", r.Additional)
	}

	r = query(t, h, "_sip._tcp.example.com", TypeSRV)
	if len(r.Answers) != 1 {
		t.Fatalf("SRV: %+v", r.Answers)
	}
}

func TestHandlerDNAME(t *testing.T) {
	h, _ := testHandler()
	r := query(t, h, "foo.legacy.example.com", TypeA)
	if len(r.Answers) != 2 || r.Answers[0].Type != TypeDNAME || r.Answers[1].Type != TypeCNAME {
		t.Fatalf("DNAME synth: %+v", r.Answers)
	}
	target, _, _ := ParseName(r.Answers[1].RData, 0)
	if target != "foo.example.net" {
		t.Fatalf("synthesized target %q", target)
	}
}

func TestHandlerProtocolErrors(t *testing.T) {
	h, _ := testHandler()

	empty := h.Handle(context.Background(), &Message{Header: Header{ID: 1}})
	if empty.Rcode() != RcodeFormatError {
		t.Fatalf("no question: %d", empty.Rcode())
	}

	two := h.Handle(context.Background(), &Message{Questions: []Question{{Name: "a.example.com", Type: TypeA, Class: ClassIN}, {Name: "b.example.com", Type: TypeA, Class: ClassIN}}})
	if two.Rcode() != RcodeFormatError {
		t.Fatalf("two questions: %d", two.Rcode())
	}

	notify := h.Handle(context.Background(), &Message{Header: Header{Flags: 4 << 11}, Questions: []Question{{Name: "example.com", Type: TypeSOA, Class: ClassIN}}})
	if notify.Rcode() != RcodeNotImplemented {
		t.Fatalf("opcode: %d", notify.Rcode())
	}

	ch := h.Handle(context.Background(), &Message{Questions: []Question{{Name: "version.bind", Type: TypeTXT, Class: 3}}})
	if ch.Rcode() != RcodeRefused {
		t.Fatalf("CH class: %d", ch.Rcode())
	}

	axfr := query(t, h, "example.com", TypeAXFR)
	if axfr.Rcode() != RcodeRefused {
		t.Fatalf("AXFR: %d", axfr.Rcode())
	}

	root := query(t, h, ".", TypeNS)
	if root.Rcode() != RcodeRefused {
		t.Fatalf("root: %d", root.Rcode())
	}
}

func TestHandlerEDNSEcho(t *testing.T) {
	h, _ := testHandler()
	req := &Message{Questions: []Question{{Name: "www.example.com", Type: TypeA, Class: ClassIN}}}
	req.SetEDNS0(4096, true)
	resp := h.Handle(context.Background(), req)
	if !resp.EDNS0 || !resp.EDNS0DO || resp.EDNS0Size != MaxEDNSUDPSize {
		t.Fatalf("EDNS0 not echoed: %+v", resp)
	}
}
