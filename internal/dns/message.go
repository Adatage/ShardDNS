package dns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	TypeA          uint16 = 1
	TypeNS         uint16 = 2
	TypeCNAME      uint16 = 5
	TypeSOA        uint16 = 6
	TypeHINFO      uint16 = 13
	TypePTR        uint16 = 12
	TypeMX         uint16 = 15
	TypeTXT        uint16 = 16
	TypeAAAA       uint16 = 28
	TypeSRV        uint16 = 33
	TypeNAPTR      uint16 = 35
	TypeDNAME      uint16 = 39
	TypeOPT        uint16 = 41
	TypeDS         uint16 = 43
	TypeSSHFP      uint16 = 44
	TypeRRSIG      uint16 = 46
	TypeNSEC       uint16 = 47
	TypeDNSKEY     uint16 = 48
	TypeNSEC3      uint16 = 50
	TypeNSEC3PARAM uint16 = 51
	TypeTLSA       uint16 = 52
	TypeCAA        uint16 = 257
	TypeAXFR       uint16 = 252
	TypeANY        uint16 = 255

	ClassIN  uint16 = 1
	ClassANY uint16 = 255

	FlagQR uint16 = 0x8000
	FlagAA uint16 = 0x0400
	FlagTC uint16 = 0x0200
	FlagRD uint16 = 0x0100
	FlagRA uint16 = 0x0080

	RcodeNoError        = 0
	RcodeFormatError    = 1
	RcodeServerFailure  = 2
	RcodeNXDomain       = 3
	RcodeNotImplemented = 4
	RcodeRefused        = 5

	MaxUDPMessageSize = 512
	MaxEDNSUDPSize    = 1232
	maxNameLen        = 255
	maxLabelLen       = 63
	maxPointerHops    = 16
)

var typeNames = map[uint16]string{
	TypeA:          "A",
	TypeNS:         "NS",
	TypeCNAME:      "CNAME",
	TypeSOA:        "SOA",
	TypeHINFO:      "HINFO",
	TypePTR:        "PTR",
	TypeMX:         "MX",
	TypeTXT:        "TXT",
	TypeAAAA:       "AAAA",
	TypeSRV:        "SRV",
	TypeNAPTR:      "NAPTR",
	TypeDNAME:      "DNAME",
	TypeOPT:        "OPT",
	TypeDS:         "DS",
	TypeSSHFP:      "SSHFP",
	TypeRRSIG:      "RRSIG",
	TypeNSEC:       "NSEC",
	TypeDNSKEY:     "DNSKEY",
	TypeNSEC3:      "NSEC3",
	TypeNSEC3PARAM: "NSEC3PARAM",
	TypeTLSA:       "TLSA",
	TypeCAA:        "CAA",
	TypeAXFR:       "AXFR",
	TypeANY:        "ANY",
}

var typeCodes = func() map[string]uint16 {
	m := make(map[string]uint16, len(typeNames)+1)
	for c, n := range typeNames {
		m[n] = c
	}
	m["*"] = TypeANY
	return m
}()

func TypeName(t uint16) string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return "TYPE" + strconv.Itoa(int(t))
}

func TypeCode(name string) uint16 {
	return typeCodes[strings.ToUpper(name)]
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
	EDNS0      bool
	EDNS0DO    bool
	EDNS0Size  uint16
}

func (m *Message) SetEDNS0(size uint16, do bool) {
	if size == 0 {
		size = MaxEDNSUDPSize
	}
	m.EDNS0 = true
	m.EDNS0Size = size
	m.EDNS0DO = do
}

func (m *Message) UDPSize() int {
	if !m.EDNS0 {
		return MaxUDPMessageSize
	}
	size := int(m.EDNS0Size)
	if size < MaxUDPMessageSize {
		return MaxUDPMessageSize
	}
	if size > MaxEDNSUDPSize {
		return MaxEDNSUDPSize
	}
	return size
}

func SetRcode(m *Message, rcode int) {
	m.Flags = (m.Flags &^ 0x000F) | uint16(rcode&0x0F)
}

func (m *Message) Rcode() int {
	return int(m.Flags & 0x000F)
}

func (m *Message) Opcode() int {
	return int(m.Flags>>11) & 0x0F
}

func NewResponse(req *Message) *Message {
	resp := &Message{
		Header: Header{
			ID:    req.ID,
			Flags: FlagQR | (req.Flags & FlagRD) | (req.Flags & (0x0F << 11)),
		},
		Questions: req.Questions,
	}
	if req.EDNS0 {
		resp.SetEDNS0(MaxEDNSUDPSize, req.EDNS0DO)
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

	if int(m.QDCount)*5 > len(buf)-off {
		return nil, errors.New("dns: question count exceeds message size")
	}
	m.Questions = make([]Question, 0, m.QDCount)
	for i := uint16(0); i < m.QDCount; i++ {
		name, next, err := ParseName(buf, off)
		if err != nil {
			return nil, fmt.Errorf("dns: parse question name: %w", err)
		}
		if next+4 > len(buf) {
			return nil, errors.New("dns: truncated question")
		}
		m.Questions = append(m.Questions, Question{
			Name:  name,
			Type:  binary.BigEndian.Uint16(buf[next : next+2]),
			Class: binary.BigEndian.Uint16(buf[next+2 : next+4]),
		})
		off = next + 4
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
		return nil, fmt.Errorf("dns: parse additional: %w", err)
	}
	filtered := m.Additional[:0]
	for _, rr := range m.Additional {
		if rr.Type == TypeOPT {
			m.EDNS0 = true
			m.EDNS0Size = rr.Class
			m.EDNS0DO = rr.TTL&0x00008000 != 0
			continue
		}
		filtered = append(filtered, rr)
	}
	m.Additional = filtered
	return m, nil
}

func parseRRSection(buf []byte, off int, count uint16) ([]RR, int, error) {
	if count == 0 {
		return nil, off, nil
	}
	if int(count)*11 > len(buf)-off {
		return nil, 0, errors.New("record count exceeds message size")
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
		rrs = append(rrs, RR{Name: name, Type: typ, Class: class, TTL: ttl, RData: rdata})
		off = next + rdlen
	}
	return rrs, off, nil
}

func ParseName(buf []byte, offset int) (string, int, error) {
	var (
		sb       strings.Builder
		next     = offset
		followed bool
		endOff   int
		hops     int
		nameLen  = 1
	)
	for {
		if next >= len(buf) {
			return "", 0, errors.New("name overflows buffer")
		}
		b := buf[next]
		switch b & 0xC0 {
		case 0x00:
			if b == 0 {
				if !followed {
					endOff = next + 1
				}
				if sb.Len() == 0 {
					return ".", endOff, nil
				}
				return sb.String(), endOff, nil
			}
			if int(b) > maxLabelLen {
				return "", 0, errors.New("label too long")
			}
			start := next + 1
			end := start + int(b)
			if end > len(buf) {
				return "", 0, errors.New("label overflows buffer")
			}
			nameLen += 1 + int(b)
			if nameLen > maxNameLen {
				return "", 0, errors.New("name too long")
			}
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.Write(buf[start:end])
			next = end
		case 0xC0:
			if next+1 >= len(buf) {
				return "", 0, errors.New("truncated pointer")
			}
			ptr := (int(b&0x3F) << 8) | int(buf[next+1])
			if !followed {
				endOff = next + 2
				followed = true
			}
			hops++
			if hops > maxPointerHops {
				return "", 0, errors.New("pointer loop")
			}
			next = ptr
		default:
			return "", 0, errors.New("reserved label type")
		}
	}
}

func AppendName(buf []byte, name string) []byte {
	out, err := PackName(buf, name)
	if err != nil {
		return append(buf, 0)
	}
	return out
}

func PackName(buf []byte, name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return append(buf, 0), nil
	}
	if len(name)+2 > maxNameLen {
		return buf, fmt.Errorf("dns: name %q too long", name)
	}
	start := 0
	for i := 0; i <= len(name); i++ {
		if i < len(name) && name[i] != '.' {
			continue
		}
		n := i - start
		if n == 0 {
			return buf, fmt.Errorf("dns: empty label in %q", name)
		}
		if n > maxLabelLen {
			return buf, fmt.Errorf("dns: label too long in %q", name)
		}
		buf = append(buf, byte(n))
		buf = append(buf, name[start:i]...)
		start = i + 1
	}
	return append(buf, 0), nil
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
	if buf, err = appendRRs(buf, m.Answers); err != nil {
		return nil, err
	}
	if buf, err = appendRRs(buf, m.Authority); err != nil {
		return nil, err
	}
	if buf, err = appendRRs(buf, m.Additional); err != nil {
		return nil, err
	}
	if m.EDNS0 {
		buf = AppendOPT(buf, m.EDNS0Size, m.EDNS0DO)
	}
	return buf, nil
}

func (m *Message) PackUDP(buf []byte) ([]byte, error) {
	limit := m.UDPSize()
	out, err := m.Pack(buf)
	if err != nil {
		return nil, err
	}
	if len(out) <= limit {
		return out, nil
	}
	if len(m.Additional) > 0 {
		trimmed := *m
		trimmed.Additional = nil
		if out, err = trimmed.Pack(buf); err != nil {
			return nil, err
		}
		if len(out) <= limit {
			return out, nil
		}
	}
	tc := &Message{Header: m.Header, Questions: m.Questions}
	tc.Flags |= FlagTC
	tc.EDNS0, tc.EDNS0DO, tc.EDNS0Size = m.EDNS0, m.EDNS0DO, m.EDNS0Size
	return tc.Pack(buf)
}

func AppendOPT(buf []byte, size uint16, dobit bool) []byte {
	if size == 0 {
		size = MaxEDNSUDPSize
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
