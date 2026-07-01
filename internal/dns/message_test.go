package dns

import (
	"bytes"
	"encoding/hex"
	"net"
	"testing"
)

// Test that ParseMessage/Pack round-trip a real query header.
func TestRoundTripQuery(t *testing.T) {
	// id=0x1234, RD set, 1 question: "www.example.com" A IN
	raw, _ := hex.DecodeString("12340100000100000000000003777777076578616d706c6503636f6d0000010001")
	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ID != 0x1234 {
		t.Fatalf("id: %#x", msg.ID)
	}
	if len(msg.Questions) != 1 || msg.Questions[0].Name != "www.example.com" || msg.Questions[0].Type != TypeA {
		t.Fatalf("questions: %+v", msg.Questions)
	}
	out, err := msg.Pack(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("round-trip mismatch\ngot:  %x\nwant: %x", out, raw)
	}
}

func TestBuildRDataA(t *testing.T) {
	rdata, err := BuildRData(TypeA, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rdata, net.IPv4(192, 0, 2, 1).To4()) {
		t.Fatalf("got %x", rdata)
	}
}

func TestBuildRDataSOA(t *testing.T) {
	_, err := BuildRData(TypeSOA, "ns1.example.com. admin.example.com. 2024010101 3600 900 604800 300")
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildRDataMX(t *testing.T) {
	rdata, err := BuildRData(TypeMX, "10 mail.example.com.")
	if err != nil {
		t.Fatal(err)
	}
	if len(rdata) < 3 || rdata[0] != 0 || rdata[1] != 10 {
		t.Fatalf("bad rdata: %x", rdata)
	}
}

func TestBuildRDataCAA(t *testing.T) {
	tests := []struct {
		input    string
		wantLen  int
		wantFlag byte
		wantTag  string
	}{
		{`0 issue "letsencrypt.org"`, 2 + 5 + 15, 0, "issue"},
		{`128 issuewild ";"`, 2 + 9 + 1, 128, "issuewild"},
		{`0 iodef "mailto:admin@example.com"`, 2 + 5 + 24, 0, "iodef"},
	}
	for _, tt := range tests {
		rdata, err := BuildRData(TypeCAA, tt.input)
		if err != nil {
			t.Fatalf("input %q: %v", tt.input, err)
		}
		if len(rdata) != tt.wantLen {
			t.Fatalf("input %q: len=%d want %d (rdata=%x)", tt.input, len(rdata), tt.wantLen, rdata)
		}
		if rdata[0] != tt.wantFlag {
			t.Fatalf("input %q: flags=%d want %d", tt.input, rdata[0], tt.wantFlag)
		}
		tagLen := int(rdata[1])
		if string(rdata[2:2+tagLen]) != tt.wantTag {
			t.Fatalf("input %q: tag=%q want %q", tt.input, string(rdata[2:2+tagLen]), tt.wantTag)
		}
	}
}

func TestNewResponse(t *testing.T) {
	req := &Message{Header: Header{ID: 42, Flags: FlagRD}, Questions: []Question{{Name: "x", Type: TypeA, Class: ClassIN}}}
	resp := NewResponse(req)
	if resp.Flags&FlagQR == 0 || resp.Flags&FlagRD == 0 {
		t.Fatalf("flags: %#x", resp.Flags)
	}
	SetRcode(resp, RcodeNXDomain)
	if resp.Rcode() != RcodeNXDomain {
		t.Fatalf("rcode: %d", resp.Rcode())
	}
}
