package dns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	TypeA          uint16 = 1
	TypeNS         uint16 = 2
	TypeCNAME      uint16 = 5
	TypeSOA        uint16 = 6
	TypePTR        uint16 = 12
	TypeMX         uint16 = 15
	TypeTXT        uint16 = 16
	TypeAAAA       uint16 = 28
	TypeSRV        uint16 = 33
	TypeOPT        uint16 = 41
	TypeDS         uint16 = 43
	TypeRRSIG      uint16 = 46
	TypeNSEC       uint16 = 47
	TypeDNSKEY     uint16 = 48
	TypeNSEC3      uint16 = 50
	TypeNSEC3PARAM uint16 = 51
	TypeCAA        uint16 = 257
	TypeANY        uint16 = 255
	ClassIN        uint16 = 1

	FlagQR uint16 = 0x8000 // Response
	FlagAA uint16 = 0x0400 // Authoritative Answer
	FlagTC uint16 = 0x0200 // Truncated
	FlagRD uint16 = 0x0100 // Recursion Desired (echoed from request)
	FlagRA uint16 = 0x0080 // Recursion Available

	RcodeNoError        = 0
	RcodeFormatError    = 1
	RcodeServerFailure  = 2
	RcodeNXDomain       = 3
	RcodeNotImplemented = 4
	RcodeRefused        = 5

	MaxUDPMessageSize = 512
	maxNameLen        = 255
	maxLabelLen       = 63
	maxPointerHops    = 16
)

func TypeName(t uint16) string {
	switch t {
	case TypeA:
		return "A"
	case TypeNS:
		return "NS"
	case TypeCNAME:
		return "CNAME"
	case TypeSOA:
		return "SOA"
	case TypePTR:
		return "PTR"
	case TypeMX:
		return "MX"
	case TypeTXT:
		return "TXT"
	case TypeAAAA:
		return "AAAA"
	case TypeSRV:
		return "SRV"
	case TypeOPT:
		return "OPT"
	case TypeDS:
		return "DS"
	case TypeRRSIG:
		return "RRSIG"
	case TypeNSEC:
		return "NSEC"
	case TypeDNSKEY:
		return "DNSKEY"
	case TypeNSEC3:
		return "NSEC3"
	case TypeNSEC3PARAM:
		return "NSEC3PARAM"
	case TypeCAA:
		return "CAA"
	case TypeANY:
		return "ANY"
	}
	return "TYPE" + strconv.Itoa(int(t))
}

func TypeCode(name string) uint16 {
	switch strings.ToUpper(name) {
	case "A":
		return TypeA
	case "NS":
		return TypeNS
	case "CNAME":
		return TypeCNAME
	case "SOA":
		return TypeSOA
	case "PTR":
		return TypePTR
	case "MX":
		return TypeMX
	case "TXT":
		return TypeTXT
	case "AAAA":
		return TypeAAAA
	case "SRV":
		return TypeSRV
	case "OPT":
		return TypeOPT
	case "DS":
		return TypeDS
	case "RRSIG":
		return TypeRRSIG
	case "NSEC":
		return TypeNSEC
	case "DNSKEY":
		return TypeDNSKEY
	case "NSEC3":
		return TypeNSEC3
	case "NSEC3PARAM":
		return TypeNSEC3PARAM
	case "CAA":
		return TypeCAA
	case "ANY", "*":
		return TypeANY
	}
	return 0
}

type Header struct {
	ID      uint16
	Flags   uint16
	QDCount uint16
	ANCount uint16
	NSCount uint16
	ARCount uint16
}

type Question struct {
	Name  string
	Type  uint16
	Class uint16
}

type RR struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32
	RData []byte
}

type Message struct {
	Header
	Questions  []Question
	Answers    []RR
	Authority  []RR
	Additional []RR
	EDNS0     bool
	EDNS0DO   bool
	EDNS0Size uint16
}

func (m *Message) SetEDNS0(size uint16, do bool) {
	if size == 0 {
		size = 4096
	}
	m.EDNS0 = true
	m.EDNS0Size = size
	m.EDNS0DO = do
}

func SetRcode(m *Message, rcode int) {
	m.Flags = (m.Flags &^ 0x000F) | uint16(rcode&0x0F)
}

func (m *Message) Rcode() int {
	return int(m.Flags & 0x000F)
}

func NewResponse(req *Message) *Message {
	resp := &Message{
		Header: Header{
			ID:    req.ID,
			Flags: FlagQR | (req.Flags & FlagRD),
		},
		Questions: req.Questions,
	}
	return resp
}

func ParseMessage(buf []byte) (*Message, error) {
	if len(buf) < 12 {
		return nil, errors.New("dns: message too short")
	}
	m := &Message{
		Header: Header{
			ID:      binary.BigEndian.Uint16(buf[0:2]),
			Flags:   binary.BigEndian.Uint16(buf[2:4]),
			QDCount: binary.BigEndian.Uint16(buf[4:6]),
			ANCount: binary.BigEndian.Uint16(buf[6:8]),
			NSCount: binary.BigEndian.Uint16(buf[8:10]),
			ARCount: binary.BigEndian.Uint16(buf[10:12]),
		},
	}
	off := 12

	m.Questions = make([]Question, 0, m.QDCount)
	for i := uint16(0); i < m.QDCount; i++ {
		name, next, err := ParseName(buf, off)
		if err != nil {
			return nil, fmt.Errorf("dns: parse question name: %w", err)
		}
		if next+4 > len(buf) {
			return nil, errors.New("dns: truncated question")
		}
		q := Question{
			Name:  name,
			Type:  binary.BigEndian.Uint16(buf[next : next+2]),
			Class: binary.BigEndian.Uint16(buf[next+2 : next+4]),
		}
		off = next + 4
		m.Questions = append(m.Questions, q)
	}

	var err error
	m.Answers, off, err = parseRRSection(buf, off, m.ANCount)
	if err != nil {
		return nil, fmt.Errorf("dns: parse answers: %w", err)
	}
	m.Authority, off, err = parseRRSection(buf, off, m.NSCount)
	if err != nil {
		return nil, fmt.Errorf("dns: parse authority: %w", err)
	}
	m.Additional, _, err = parseRRSection(buf, off, m.ARCount)
	if err != nil {
		m.ARCount = 0
		m.Additional = nil
	}
	if len(m.Additional) > 0 {
		filtered := m.Additional[:0]
		for _, rr := range m.Additional {
			if rr.Type == TypeOPT {
				m.EDNS0 = true
				m.EDNS0Size = rr.Class
				m.EDNS0DO = (rr.TTL & 0x00008000) != 0
				continue
			}
			filtered = append(filtered, rr)
		}
		m.Additional = filtered
	}
	return m, nil
}

func parseRRSection(buf []byte, off int, count uint16) ([]RR, int, error) {
	if count == 0 {
		return nil, off, nil
	}
	rrs := make([]RR, 0, count)
	for i := uint16(0); i < count; i++ {
		name, next, err := ParseName(buf, off)
		if err != nil {
			return nil, 0, err
		}
		if next+10 > len(buf) {
			return nil, 0, errors.New("truncated RR header")
		}
		typ := binary.BigEndian.Uint16(buf[next : next+2])
		class := binary.BigEndian.Uint16(buf[next+2 : next+4])
		ttl := binary.BigEndian.Uint32(buf[next+4 : next+8])
		rdlen := int(binary.BigEndian.Uint16(buf[next+8 : next+10]))
		next += 10
		if next+rdlen > len(buf) {
			return nil, 0, errors.New("truncated RDATA")
		}
		rdata := make([]byte, rdlen)
		copy(rdata, buf[next:next+rdlen])
		rrs = append(rrs, RR{
			Name:  name,
			Type:  typ,
			Class: class,
			TTL:   ttl,
			RData: rdata,
		})
		off = next + rdlen
	}
	return rrs, off, nil
}

func ParseName(buf []byte, offset int) (string, int, error) {
	var (
		labels    []string
		next      = offset
		followed  bool
		firstNext int
		hops      int
	)
	for {
		if next >= len(buf) {
			return "", 0, errors.New("name overflows buffer")
		}
		b := buf[next]
		switch b & 0xC0 {
		case 0x00:
			if b == 0 {
				next++
				if !followed {
					firstNext = next
				}
				name := "."
				if len(labels) > 0 {
					name = strings.Join(labels, ".")
				}
				return strings.ToLower(name), firstNext, nil
			}
			if int(b) > maxLabelLen {
				return "", 0, errors.New("label too long")
			}
			start := next + 1
			end := start + int(b)
			if end > len(buf) {
				return "", 0, errors.New("label overflows buffer")
			}
			labels = append(labels, string(buf[start:end]))
			if totalLen(labels) > maxNameLen {
				return "", 0, errors.New("name too long")
			}
			next = end
		case 0xC0:
			if next+1 >= len(buf) {
				return "", 0, errors.New("truncated pointer")
			}
			ptr := (int(b&0x3F) << 8) | int(buf[next+1])
			if !followed {
				firstNext = next + 2
				followed = true
			}
			hops++
			if hops > maxPointerHops {
				return "", 0, errors.New("pointer loop")
			}
			if ptr >= offset && !followed {
				return "", 0, errors.New("forward pointer")
			}
			next = ptr
		default:
			return "", 0, errors.New("reserved label type")
		}
	}
}

func totalLen(labels []string) int {
	n := 1
	for _, l := range labels {
		n += 1 + len(l)
	}
	return n
}

func AppendName(buf []byte, name string) []byte {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" || name == "." {
		return append(buf, 0)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > maxLabelLen {
			return append(buf, 0)
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, label...)
	}
	return append(buf, 0)
}

func (m *Message) Pack(buf []byte) ([]byte, error) {
	if cap(buf) < 12 {
		buf = make([]byte, 0, 512)
	}
	buf = buf[:0]

	m.QDCount = uint16(len(m.Questions))
	m.ANCount = uint16(len(m.Answers))
	m.NSCount = uint16(len(m.Authority))
	arCount := uint16(len(m.Additional))
	if m.EDNS0 {
		arCount++
	}
	m.ARCount = arCount

	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[0:2], m.ID)
	binary.BigEndian.PutUint16(hdr[2:4], m.Flags)
	binary.BigEndian.PutUint16(hdr[4:6], m.QDCount)
	binary.BigEndian.PutUint16(hdr[6:8], m.ANCount)
	binary.BigEndian.PutUint16(hdr[8:10], m.NSCount)
	binary.BigEndian.PutUint16(hdr[10:12], m.ARCount)
	buf = append(buf, hdr[:]...)

	for _, q := range m.Questions {
		buf = AppendName(buf, q.Name)
		var tc [4]byte
		binary.BigEndian.PutUint16(tc[0:2], q.Type)
		binary.BigEndian.PutUint16(tc[2:4], q.Class)
		buf = append(buf, tc[:]...)
	}

	var err error
	buf, err = appendRRs(buf, m.Answers)
	if err != nil {
		return nil, err
	}
	buf, err = appendRRs(buf, m.Authority)
	if err != nil {
		return nil, err
	}
	buf, err = appendRRs(buf, m.Additional)
	if err != nil {
		return nil, err
	}
	if m.EDNS0 {
		buf = AppendOPT(buf, m.EDNS0Size, m.EDNS0DO)
	}
	return buf, nil
}

func AppendOPT(buf []byte, size uint16, dobit bool) []byte {
	if size == 0 {
		size = 4096
	}
	buf = append(buf, 0)
	var hdr [10]byte
	binary.BigEndian.PutUint16(hdr[0:2], TypeOPT)
	binary.BigEndian.PutUint16(hdr[2:4], size)
	var ttl uint32
	if dobit {
		ttl |= 0x00008000
	}
	binary.BigEndian.PutUint32(hdr[4:8], ttl)
	binary.BigEndian.PutUint16(hdr[8:10], 0)
	return append(buf, hdr[:]...)
}

func appendRRs(buf []byte, rrs []RR) ([]byte, error) {
	for _, rr := range rrs {
		buf = AppendName(buf, rr.Name)
		var hdr [10]byte
		binary.BigEndian.PutUint16(hdr[0:2], rr.Type)
		binary.BigEndian.PutUint16(hdr[2:4], rr.Class)
		binary.BigEndian.PutUint32(hdr[4:8], rr.TTL)
		if len(rr.RData) > 0xFFFF {
			return nil, errors.New("rdata too long")
		}
		binary.BigEndian.PutUint16(hdr[8:10], uint16(len(rr.RData)))
		buf = append(buf, hdr[:]...)
		buf = append(buf, rr.RData...)
	}
	return buf, nil
}

func BuildRData(rtype uint16, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	switch rtype {
	case TypeA:
		ip := net.ParseIP(text)
		if ip == nil {
			return nil, fmt.Errorf("dns: invalid A rdata %q", text)
		}
		v4 := ip.To4()
		if v4 == nil {
			return nil, fmt.Errorf("dns: not an IPv4 address: %q", text)
		}
		out := make([]byte, 4)
		copy(out, v4)
		return out, nil

	case TypeAAAA:
		ip := net.ParseIP(text)
		if ip == nil {
			return nil, fmt.Errorf("dns: invalid AAAA rdata %q", text)
		}
		v6 := ip.To16()
		if v6 == nil || ip.To4() != nil {
			return nil, fmt.Errorf("dns: not an IPv6 address: %q", text)
		}
		out := make([]byte, 16)
		copy(out, v6)
		return out, nil

	case TypeNS, TypeCNAME, TypePTR:
		return AppendName(nil, text), nil

	case TypeMX:
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("dns: MX rdata expects `pref target`, got %q", text)
		}
		pref, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("dns: MX preference: %w", err)
		}
		buf := make([]byte, 2, 32)
		binary.BigEndian.PutUint16(buf, uint16(pref))
		buf = AppendName(buf, fields[1])
		return buf, nil

	case TypeTXT:
		var parts []string
		if strings.HasPrefix(text, `"`) {
			rest := text
			for len(rest) > 0 {
				rest = strings.TrimSpace(rest)
				if len(rest) == 0 {
					break
				}
				if rest[0] != '"' {
					return nil, fmt.Errorf("dns: unexpected token in TXT rdata: %q", rest)
				}
				end := strings.Index(rest[1:], `"`)
				if end < 0 {
					return nil, fmt.Errorf("dns: unterminated quoted string in TXT rdata")
				}
				parts = append(parts, rest[1:end+1])
				rest = rest[end+2:]
			}
			if len(parts) == 0 {
				parts = []string{""}
			}
		} else {
			s := text
			for len(s) > 255 {
				parts = append(parts, s[:255])
				s = s[255:]
			}
			parts = append(parts, s)
		}
		buf := make([]byte, 0, len(text)+len(parts))
		for _, p := range parts {
			buf = append(buf, byte(len(p)))
			buf = append(buf, p...)
		}
		return buf, nil

	case TypeSOA:
		fields := strings.Fields(text)
		if len(fields) != 7 {
			return nil, fmt.Errorf("dns: SOA rdata expects 7 fields, got %d", len(fields))
		}
		serial, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("dns: SOA serial: %w", err)
		}
		refresh, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("dns: SOA refresh: %w", err)
		}
		retry, err := strconv.ParseUint(fields[4], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("dns: SOA retry: %w", err)
		}
		expire, err := strconv.ParseUint(fields[5], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("dns: SOA expire: %w", err)
		}
		minttl, err := strconv.ParseUint(fields[6], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("dns: SOA minimum: %w", err)
		}
		buf := make([]byte, 0, 64)
		buf = AppendName(buf, fields[0])
		buf = AppendName(buf, fields[1])
		var nums [20]byte
		binary.BigEndian.PutUint32(nums[0:4], uint32(serial))
		binary.BigEndian.PutUint32(nums[4:8], uint32(refresh))
		binary.BigEndian.PutUint32(nums[8:12], uint32(retry))
		binary.BigEndian.PutUint32(nums[12:16], uint32(expire))
		binary.BigEndian.PutUint32(nums[16:20], uint32(minttl))
		buf = append(buf, nums[:]...)
		return buf, nil

	case TypeSRV:
		fields := strings.Fields(text)
		if len(fields) != 4 {
			return nil, fmt.Errorf("dns: SRV rdata expects `prio weight port target`")
		}
		prio, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("dns: SRV priority: %w", err)
		}
		weight, err := strconv.ParseUint(fields[1], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("dns: SRV weight: %w", err)
		}
		port, err := strconv.ParseUint(fields[2], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("dns: SRV port: %w", err)
		}
		buf := make([]byte, 6, 32)
		binary.BigEndian.PutUint16(buf[0:2], uint16(prio))
		binary.BigEndian.PutUint16(buf[2:4], uint16(weight))
		binary.BigEndian.PutUint16(buf[4:6], uint16(port))
		buf = AppendName(buf, fields[3])
		return buf, nil

	case TypeCAA:
		fields := strings.SplitN(text, " ", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("dns: CAA rdata expects `flags tag value`, got %q", text)
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("dns: CAA flags: %w", err)
		}
		tag := strings.TrimSpace(fields[1])
		if len(tag) == 0 || len(tag) > 255 {
			return nil, fmt.Errorf("dns: CAA tag length must be 1–255")
		}
		value := strings.TrimSpace(fields[2])
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		buf := make([]byte, 0, 2+len(tag)+len(value))
		buf = append(buf, byte(flags), byte(len(tag)))
		buf = append(buf, tag...)
		buf = append(buf, value...)
		return buf, nil
	}

	return nil, fmt.Errorf("dns: unsupported RR type %d", rtype)
}
