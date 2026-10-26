package dns

import (
	"bytes"
	"testing"
)

func TestParsePreservesCaseAndRoundTrips(t *testing.T) {
	req := &Message{
		Header:    Header{ID: 0xbeef, Flags: FlagRD},
		Questions: []Question{{Name: "WwW.ExAmPlE.com", Type: TypeA, Class: ClassIN}},
	}
	req.SetEDNS0(4096, true)
	wire, err := req.Pack(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMessage(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Questions[0].Name != "WwW.ExAmPlE.com" {
		t.Fatalf("case not preserved: %q", got.Questions[0].Name)
	}
	if !got.EDNS0 || !got.EDNS0DO || got.EDNS0Size != 4096 {
		t.Fatalf("EDNS0 not parsed: %+v", got)
	}
	if len(got.Additional) != 0 {
		t.Fatalf("OPT leaked into additional")
	}
}

func TestParseCompressedName(t *testing.T) {
	wire := []byte{
		0, 1, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0,
		3, 'f', 'o', 'o', 0, 0, 1, 0, 1,
		0xC0, 12, 0, 1, 0, 1,
	}
	m, err := ParseMessage(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Questions) != 2 || m.Questions[1].Name != "foo" {
		t.Fatalf("got %+v", m.Questions)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	loop := []byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 12, 0, 1, 0, 1}
	cases := map[string][]byte{
		"short":         {1, 2, 3},
		"pointer loop":  loop,
		"huge qdcount":  {0, 1, 0, 0, 0xff, 0xff, 0, 0, 0, 0, 0, 0},
		"truncated rr":  {0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 1, 0, 9},
		"label overrun": {0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 40, 'a'},
	}
	for name, wire := range cases {
		if _, err := ParseMessage(wire); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPackUDPTruncation(t *testing.T) {
	m := &Message{
		Header:    Header{ID: 1, Flags: FlagQR | FlagAA},
		Questions: []Question{{Name: "big.example.com", Type: TypeTXT, Class: ClassIN}},
	}
	for i := 0; i < 20; i++ {
		m.Answers = append(m.Answers, RR{Name: "big.example.com", Type: TypeTXT, Class: ClassIN, TTL: 60, RData: append([]byte{200}, bytes.Repeat([]byte("x"), 200)...)})
	}
	out, err := m.PackUDP(make([]byte, 0, 4096))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxUDPMessageSize {
		t.Fatalf("response %d exceeds limit", len(out))
	}
	parsed, err := ParseMessage(out)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Flags&FlagTC == 0 || len(parsed.Answers) != 0 {
		t.Fatalf("expected empty TC response, got flags %x answers %d", parsed.Flags, len(parsed.Answers))
	}

	m.EDNS0, m.EDNS0Size = true, 4096
	m.Answers = m.Answers[:5]
	out, err = m.PackUDP(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = ParseMessage(out)
	if parsed.Flags&FlagTC != 0 || len(parsed.Answers) != 5 {
		t.Fatalf("EDNS response should fit: flags %x answers %d", parsed.Flags, len(parsed.Answers))
	}
}

func TestPackNameValidation(t *testing.T) {
	if _, err := PackName(nil, "a..b"); err == nil {
		t.Error("empty label should fail")
	}
	long := string(bytes.Repeat([]byte("a"), 64)) + ".com"
	if _, err := PackName(nil, long); err == nil {
		t.Error("long label should fail")
	}
}
