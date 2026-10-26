package dns

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

func SupportedType(t uint16) bool {
	switch t {
	case TypeA, TypeAAAA, TypeNS, TypeCNAME, TypeDNAME, TypePTR, TypeMX, TypeTXT,
		TypeSRV, TypeCAA, TypeNAPTR, TypeSSHFP, TypeTLSA, TypeDS, TypeDNSKEY, TypeHINFO:
		return true
	}
	return false
}

func BuildRData(rtype uint16, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	switch rtype {
	case TypeA:
		addr, err := netip.ParseAddr(text)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("dns: invalid A rdata %q", text)
		}
		b := addr.As4()
		return b[:], nil

	case TypeAAAA:
		addr, err := netip.ParseAddr(text)
		if err != nil || !addr.Is6() || addr.Is4In6() {
			return nil, fmt.Errorf("dns: invalid AAAA rdata %q", text)
		}
		b := addr.As16()
		return b[:], nil

	case TypeNS, TypeCNAME, TypeDNAME, TypePTR:
		if text == "" || strings.ContainsAny(text, " \t") {
			return nil, fmt.Errorf("dns: %s rdata expects a single domain name, got %q", TypeName(rtype), text)
		}
		return PackName(nil, text)

	case TypeMX:
		f := strings.Fields(text)
		if len(f) != 2 {
			return nil, fmt.Errorf("dns: MX rdata expects `preference target`, got %q", text)
		}
		pref, err := parseUint(f[0], 16, "MX preference")
		if err != nil {
			return nil, err
		}
		buf := binary.BigEndian.AppendUint16(nil, uint16(pref))
		return PackName(buf, f[1])

	case TypeTXT:
		return buildTXT(text)

	case TypeSRV:
		f := strings.Fields(text)
		if len(f) != 4 {
			return nil, fmt.Errorf("dns: SRV rdata expects `priority weight port target`, got %q", text)
		}
		var buf []byte
		for i, label := range []string{"SRV priority", "SRV weight", "SRV port"} {
			v, err := parseUint(f[i], 16, label)
			if err != nil {
				return nil, err
			}
			buf = binary.BigEndian.AppendUint16(buf, uint16(v))
		}
		return PackName(buf, f[3])

	case TypeCAA:
		f := strings.SplitN(text, " ", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("dns: CAA rdata expects `flags tag value`, got %q", text)
		}
		flags, err := parseUint(f[0], 8, "CAA flags")
		if err != nil {
			return nil, err
		}
		tag := f[1]
		if tag == "" || len(tag) > 255 {
			return nil, fmt.Errorf("dns: CAA tag length must be 1-255")
		}
		value := strings.TrimSpace(f[2])
		if strings.HasPrefix(value, `"`) {
			vals, err := parseQuotedStrings(value)
			if err != nil || len(vals) != 1 {
				return nil, fmt.Errorf("dns: invalid CAA value %q", value)
			}
			value = vals[0]
		}
		buf := []byte{byte(flags), byte(len(tag))}
		buf = append(buf, tag...)
		return append(buf, value...), nil

	case TypeNAPTR:
		orderStr, rest := cutField(text)
		prefStr, rest := cutField(rest)
		order, err := parseUint(orderStr, 16, "NAPTR order")
		if err != nil {
			return nil, err
		}
		pref, err := parseUint(prefStr, 16, "NAPTR preference")
		if err != nil {
			return nil, err
		}
		strs, remaining, err := takeQuoted(rest, 3)
		if err != nil {
			return nil, err
		}
		repl := strings.TrimSpace(remaining)
		if repl == "" || strings.ContainsAny(repl, " \t") {
			return nil, fmt.Errorf("dns: NAPTR replacement must be a single domain name")
		}
		buf := binary.BigEndian.AppendUint16(nil, uint16(order))
		buf = binary.BigEndian.AppendUint16(buf, uint16(pref))
		for _, s := range strs {
			if len(s) > 255 {
				return nil, fmt.Errorf("dns: NAPTR string too long")
			}
			buf = append(buf, byte(len(s)))
			buf = append(buf, s...)
		}
		return PackName(buf, repl)

	case TypeHINFO:
		strs, rest, err := takeQuoted(text, 2)
		if err != nil || strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("dns: HINFO rdata expects `\"cpu\" \"os\"`, got %q", text)
		}
		var buf []byte
		for _, s := range strs {
			if len(s) > 255 {
				return nil, fmt.Errorf("dns: HINFO string too long")
			}
			buf = append(buf, byte(len(s)))
			buf = append(buf, s...)
		}
		return buf, nil

	case TypeSSHFP:
		f := strings.Fields(text)
		if len(f) < 3 {
			return nil, fmt.Errorf("dns: SSHFP rdata expects `algorithm type fingerprint`, got %q", text)
		}
		alg, err := parseUint(f[0], 8, "SSHFP algorithm")
		if err != nil {
			return nil, err
		}
		fp, err := parseUint(f[1], 8, "SSHFP fingerprint type")
		if err != nil {
			return nil, err
		}
		digest, err := hex.DecodeString(strings.Join(f[2:], ""))
		if err != nil || len(digest) == 0 {
			return nil, fmt.Errorf("dns: invalid SSHFP fingerprint")
		}
		return append([]byte{byte(alg), byte(fp)}, digest...), nil

	case TypeTLSA:
		f := strings.Fields(text)
		if len(f) < 4 {
			return nil, fmt.Errorf("dns: TLSA rdata expects `usage selector matching data`, got %q", text)
		}
		var buf []byte
		for i, label := range []string{"TLSA usage", "TLSA selector", "TLSA matching type"} {
			v, err := parseUint(f[i], 8, label)
			if err != nil {
				return nil, err
			}
			buf = append(buf, byte(v))
		}
		data, err := hex.DecodeString(strings.Join(f[3:], ""))
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("dns: invalid TLSA certificate data")
		}
		return append(buf, data...), nil

	case TypeDS:
		f := strings.Fields(text)
		if len(f) < 4 {
			return nil, fmt.Errorf("dns: DS rdata expects `key-tag algorithm digest-type digest`, got %q", text)
		}
		tag, err := parseUint(f[0], 16, "DS key tag")
		if err != nil {
			return nil, err
		}
		alg, err := parseUint(f[1], 8, "DS algorithm")
		if err != nil {
			return nil, err
		}
		dt, err := parseUint(f[2], 8, "DS digest type")
		if err != nil {
			return nil, err
		}
		digest, err := hex.DecodeString(strings.Join(f[3:], ""))
		if err != nil || len(digest) == 0 {
			return nil, fmt.Errorf("dns: invalid DS digest")
		}
		buf := binary.BigEndian.AppendUint16(nil, uint16(tag))
		buf = append(buf, byte(alg), byte(dt))
		return append(buf, digest...), nil

	case TypeDNSKEY:
		f := strings.Fields(text)
		if len(f) < 4 {
			return nil, fmt.Errorf("dns: DNSKEY rdata expects `flags protocol algorithm key`, got %q", text)
		}
		flags, err := parseUint(f[0], 16, "DNSKEY flags")
		if err != nil {
			return nil, err
		}
		proto, err := parseUint(f[1], 8, "DNSKEY protocol")
		if err != nil {
			return nil, err
		}
		alg, err := parseUint(f[2], 8, "DNSKEY algorithm")
		if err != nil {
			return nil, err
		}
		key, err := base64.StdEncoding.DecodeString(strings.Join(f[3:], ""))
		if err != nil || len(key) == 0 {
			return nil, fmt.Errorf("dns: invalid DNSKEY public key")
		}
		buf := binary.BigEndian.AppendUint16(nil, uint16(flags))
		buf = append(buf, byte(proto), byte(alg))
		return append(buf, key...), nil
	}

	return nil, fmt.Errorf("dns: unsupported RR type %s", TypeName(rtype))
}

func BuildSOA(primary, admin string, serial uint32, refresh, retry, expire, minimum uint32) ([]byte, error) {
	buf, err := PackName(nil, primary)
	if err != nil {
		return nil, err
	}
	if buf, err = PackName(buf, admin); err != nil {
		return nil, err
	}
	for _, v := range []uint32{serial, refresh, retry, expire, minimum} {
		buf = binary.BigEndian.AppendUint32(buf, v)
	}
	return buf, nil
}

func RDataNames(rtype uint16, rdata []byte) string {
	switch rtype {
	case TypeNS, TypeCNAME, TypeDNAME, TypePTR:
		name, _, err := ParseName(rdata, 0)
		if err == nil {
			return name
		}
	case TypeMX:
		if len(rdata) > 2 {
			name, _, err := ParseName(rdata, 2)
			if err == nil {
				return name
			}
		}
	case TypeSRV:
		if len(rdata) > 6 {
			name, _, err := ParseName(rdata, 6)
			if err == nil {
				return name
			}
		}
	}
	return ""
}

func buildTXT(text string) ([]byte, error) {
	var parts []string
	if strings.HasPrefix(text, `"`) {
		var err error
		parts, err = parseQuotedStrings(text)
		if err != nil {
			return nil, err
		}
	} else {
		parts = []string{text}
	}
	var buf []byte
	for _, p := range parts {
		for len(p) > 255 {
			buf = append(buf, 255)
			buf = append(buf, p[:255]...)
			p = p[255:]
		}
		buf = append(buf, byte(len(p)))
		buf = append(buf, p...)
	}
	if len(buf) == 0 {
		buf = []byte{0}
	}
	return buf, nil
}

func parseQuotedStrings(text string) ([]string, error) {
	var out []string
	rest := strings.TrimSpace(text)
	for rest != "" {
		s, remaining, err := readQuoted(rest)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		rest = strings.TrimSpace(remaining)
	}
	return out, nil
}

func takeQuoted(text string, n int) ([]string, string, error) {
	out := make([]string, 0, n)
	rest := strings.TrimSpace(text)
	for i := 0; i < n; i++ {
		if !strings.HasPrefix(rest, `"`) {
			return nil, "", fmt.Errorf("dns: expected quoted string in %q", text)
		}
		s, remaining, err := readQuoted(rest)
		if err != nil {
			return nil, "", err
		}
		out = append(out, s)
		rest = strings.TrimSpace(remaining)
	}
	return out, rest, nil
}

func readQuoted(s string) (string, string, error) {
	if s == "" || s[0] != '"' {
		return "", "", fmt.Errorf("dns: expected opening quote in %q", s)
	}
	var sb strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return sb.String(), s[i+1:], nil
		case c == '\\' && i+1 < len(s):
			i++
			if isDigit(s[i]) && i+2 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) {
				v, _ := strconv.Atoi(s[i : i+3])
				if v > 255 {
					return "", "", fmt.Errorf("dns: invalid escape \\%s", s[i:i+3])
				}
				sb.WriteByte(byte(v))
				i += 2
			} else {
				sb.WriteByte(s[i])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("dns: unterminated quoted string")
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func parseUint(s string, bits int, what string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		return 0, fmt.Errorf("dns: invalid %s %q", what, s)
	}
	return v, nil
}

func cutField(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}
