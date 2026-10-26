package dns

import (
	"bytes"
	"testing"
)

func TestBuildRData(t *testing.T) {
	name := func(n string) []byte { return AppendName(nil, n) }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	tests := []struct {
		typ   uint16
		in    string
		want  []byte
		valid bool
	}{
		{TypeA, "192.0.2.1", []byte{192, 0, 2, 1}, true},
		{TypeA, "2001:db8::1", nil, false},
		{TypeA, "999.0.0.1", nil, false},
		{TypeAAAA, "2001:db8::1", []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, true},
		{TypeAAAA, "192.0.2.1", nil, false},
		{TypeAAAA, "::ffff:192.0.2.1", nil, false},
		{TypeNS, "ns1.example.com.", name("ns1.example.com"), true},
		{TypeCNAME, "target.example.com", name("target.example.com"), true},
		{TypeCNAME, "two words", nil, false},
		{TypeMX, "10 mail.example.com.", cat([]byte{0, 10}, name("mail.example.com")), true},
		{TypeMX, "mail.example.com", nil, false},
		{TypeMX, "70000 mail.example.com", nil, false},
		{TypeTXT, "v=spf1 include:_spf.example.com ~all", append([]byte{36}, "v=spf1 include:_spf.example.com ~all"...), true},
		{TypeTXT, `"hello" "world"`, []byte("\x05hello\x05world"), true},
		{TypeTXT, `"say \"hi\""`, []byte("\x08say \"hi\""), true},
		{TypeTXT, `"a\065b"`, []byte("\x03aAb"), true},
		{TypeTXT, `"unterminated`, nil, false},
		{TypeTXT, ``, []byte{0}, true},
		{TypeSRV, "10 20 443 svc.example.com.", cat([]byte{0, 10, 0, 20, 1, 0xbb}, name("svc.example.com")), true},
		{TypeSRV, "10 20 99999 svc.example.com.", nil, false},
		{TypeCAA, `0 issue "letsencrypt.org"`, append([]byte{0, 5}, "issueletsencrypt.org"...), true},
		{TypeCAA, `128 iodef "mailto:sec@example.com"`, append([]byte{128, 5}, "iodefmailto:sec@example.com"...), true},
		{TypeCAA, `0 issue`, nil, false},
		{TypeSSHFP, "1 2 AABB", []byte{1, 2, 0xaa, 0xbb}, true},
		{TypeSSHFP, "1 2 XYZ", nil, false},
		{TypeTLSA, "3 1 1 0A0B", []byte{3, 1, 1, 0x0a, 0x0b}, true},
		{TypeDS, "12345 13 2 abcd", []byte{0x30, 0x39, 13, 2, 0xab, 0xcd}, true},
		{TypeDNSKEY, "257 3 13 AQID", []byte{1, 1, 3, 13, 1, 2, 3}, true},
		{TypeHINFO, `"amd64" "linux"`, []byte("\x05amd64\x05linux"), true},
		{TypeHINFO, `"amd64"`, nil, false},
		{TypeNAPTR, `100 10 "u" "E2U+sip" "!^.*$!sip:info@example.com!" .`, cat(
			[]byte{0, 100, 0, 10, 1}, []byte("u"), []byte{7}, []byte("E2U+sip"),
			[]byte{byte(len("!^.*$!sip:info@example.com!"))}, []byte("!^.*$!sip:info@example.com!"), []byte{0}), true},
		{TypeDNAME, "example.net.", name("example.net"), true},
		{TypeSOA, "x", nil, false},
	}
	for _, tc := range tests {
		got, err := BuildRData(tc.typ, tc.in)
		if tc.valid {
			if err != nil {
				t.Errorf("%s %q: unexpected error: %v", TypeName(tc.typ), tc.in, err)
				continue
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("%s %q:\n got %x\nwant %x", TypeName(tc.typ), tc.in, got, tc.want)
			}
		} else if err == nil {
			t.Errorf("%s %q: expected error, got %x", TypeName(tc.typ), tc.in, got)
		}
	}
}

func TestTXTChunking(t *testing.T) {
	long := string(bytes.Repeat([]byte("a"), 600))
	got, err := BuildRData(TypeTXT, long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 600+3 || got[0] != 255 || got[256] != 255 || got[512] != 90 {
		t.Fatalf("unexpected chunking: %d bytes, lens %d %d %d", len(got), got[0], got[256], got[512])
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"example.com", "www.example.com", "_dmarc.example.com", "*.example.com", "a-b.example.com", "xn--bcher-kva.example"}
	bad := []string{"", "a..b", "-a.example.com", "a-.example.com", "a.*.example.com", "exa mple.com", "UPPER.com", "a$b.com"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range bad {
		if ValidateName(n) == nil {
			t.Errorf("%q: expected error", n)
		}
	}
}
