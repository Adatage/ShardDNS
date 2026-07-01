// Package grpcserver adapts the ScyllaDB-backed store to the generated
// DNSManager gRPC service defined in proto/dns_manager.proto.
//
// The generated code lives under github.com/Adatage/ShardDNS/api and is
// produced by `make proto` (which invokes `buf generate`). Do not import
// this package before running the code generator.
package grpcserver

import (
	"context"
	"errors"

	dnsmgr "github.com/Adatage/ShardDNS/api"
	"github.com/Adatage/ShardDNS/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements dnsmgr.DNSManagerServer on top of *store.Store.
type Server struct {
	Store *store.Store
}

// New wires up a gRPC server backed by the given store.
func New(s *store.Store) *Server { return &Server{Store: s} }

// --------------------------------------------------------------------------
// Zone RPCs
// --------------------------------------------------------------------------

// CreateZone inserts a new zone and also creates its authoritative SOA
// record so that the server can answer negative queries immediately after
// zone creation.
func (s *Server) CreateZone(ctx context.Context, req *dnsmgr.CreateZoneRequest) (*dnsmgr.Zone, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	z := &store.Zone{
		Name:       req.GetName(),
		PrimaryNS:  defaultString(req.GetPrimaryNs(), "ns1."+req.GetName()+"."),
		AdminEmail: defaultString(req.GetAdminEmail(), "admin."+req.GetName()+"."),
		Refresh:    defaultInt32(req.GetRefresh(), 3600),
		Retry:      defaultInt32(req.GetRetry(), 900),
		Expire:     defaultInt32(req.GetExpire(), 604800),
		MinTTL:     defaultInt32(req.GetMinimumTtl(), 300),
	}
	if err := s.Store.CreateZone(ctx, z); err != nil {
		return nil, status.Errorf(codes.Internal, "create zone: %v", err)
	}

	// Materialize the SOA record so DNS negative responses work.
	soaText := formatSOA(z)
	soa := &store.Record{
		Zone:  z.Name,
		Name:  z.Name,
		Type:  "SOA",
		TTL:   z.MinTTL,
		RData: soaText,
	}
	if err := s.Store.CreateRecord(ctx, soa); err != nil {
		return nil, status.Errorf(codes.Internal, "create SOA: %v", err)
	}
	return zoneToProto(z), nil
}

// GetZone returns a single zone.
func (s *Server) GetZone(ctx context.Context, req *dnsmgr.GetZoneRequest) (*dnsmgr.Zone, error) {
	z, err := s.Store.GetZone(ctx, req.GetName())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "zone %q not found", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "get zone: %v", err)
	}
	return zoneToProto(z), nil
}

// UpdateZone updates mutable zone fields and re-writes the SOA rdata.
func (s *Server) UpdateZone(ctx context.Context, req *dnsmgr.UpdateZoneRequest) (*dnsmgr.Zone, error) {
	existing, err := s.Store.GetZone(ctx, req.GetName())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "zone %q not found", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "get zone: %v", err)
	}
	if v := req.GetPrimaryNs(); v != "" {
		existing.PrimaryNS = v
	}
	if v := req.GetAdminEmail(); v != "" {
		existing.AdminEmail = v
	}
	if v := req.GetRefresh(); v != 0 {
		existing.Refresh = v
	}
	if v := req.GetRetry(); v != 0 {
		existing.Retry = v
	}
	if v := req.GetExpire(); v != 0 {
		existing.Expire = v
	}
	if v := req.GetMinimumTtl(); v != 0 {
		existing.MinTTL = v
	}
	existing.Serial = 0 // Force UpdateZone to pick a fresh serial.
	if err := s.Store.UpdateZone(ctx, existing); err != nil {
		return nil, status.Errorf(codes.Internal, "update zone: %v", err)
	}
	// Rewrite the SOA record so DNS responses reflect the new metadata.
	soa := &store.Record{
		Zone:  existing.Name,
		Name:  existing.Name,
		Type:  "SOA",
		TTL:   existing.MinTTL,
		RData: formatSOA(existing),
	}
	if err := s.Store.CreateRecord(ctx, soa); err != nil {
		return nil, status.Errorf(codes.Internal, "update SOA: %v", err)
	}
	return zoneToProto(existing), nil
}

// DeleteZone removes the zone. Records are not cascaded; deleting the zone
// makes the DNS server return REFUSED for its names.
func (s *Server) DeleteZone(ctx context.Context, req *dnsmgr.DeleteZoneRequest) (*emptypb.Empty, error) {
	if err := s.Store.DeleteZone(ctx, req.GetName()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete zone: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListZones returns a page of zones. page_token is the opaque ScyllaDB
// paging state, serialized as raw bytes.
func (s *Server) ListZones(ctx context.Context, req *dnsmgr.ListZonesRequest) (*dnsmgr.ListZonesResponse, error) {
	zones, next, err := s.Store.ListZones(ctx, int(req.GetPageSize()), []byte(req.GetPageToken()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list zones: %v", err)
	}
	out := &dnsmgr.ListZonesResponse{
		Zones:         make([]*dnsmgr.Zone, 0, len(zones)),
		NextPageToken: string(next),
	}
	for _, z := range zones {
		out.Zones = append(out.Zones, zoneToProto(z))
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Record RPCs
// --------------------------------------------------------------------------

// CreateRecord adds a single record to a zone.
func (s *Server) CreateRecord(ctx context.Context, req *dnsmgr.CreateRecordRequest) (*dnsmgr.Record, error) {
	if req.GetZone() == "" || req.GetName() == "" || req.GetType() == "" {
		return nil, status.Error(codes.InvalidArgument, "zone, name and type are required")
	}
	r := &store.Record{
		Zone:  req.GetZone(),
		Name:  req.GetName(),
		Type:  req.GetType(),
		TTL:   defaultInt32(req.GetTtl(), 300),
		RData: req.GetRdata(),
	}
	if err := s.Store.CreateRecord(ctx, r); err != nil {
		return nil, status.Errorf(codes.Internal, "create record: %v", err)
	}
	return recordToProto(r), nil
}

// DeleteRecord removes a single record identified by zone/name/type/rdata.
func (s *Server) DeleteRecord(ctx context.Context, req *dnsmgr.DeleteRecordRequest) (*emptypb.Empty, error) {
	if err := s.Store.DeleteRecord(ctx, req.GetZone(), req.GetName(), req.GetType(), req.GetRdata()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete record: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListRecords returns every record in a zone (paged internally by the store).
func (s *Server) ListRecords(ctx context.Context, req *dnsmgr.ListRecordsRequest) (*dnsmgr.ListRecordsResponse, error) {
	records, err := s.Store.ListRecords(ctx, req.GetZone(), int(req.GetPageSize()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list records: %v", err)
	}
	out := &dnsmgr.ListRecordsResponse{
		Records: make([]*dnsmgr.Record, 0, len(records)),
	}
	for _, r := range records {
		out.Records = append(out.Records, recordToProto(r))
	}
	return out, nil
}

// GetRecords returns all records matching (zone, name, type).
func (s *Server) GetRecords(ctx context.Context, req *dnsmgr.GetRecordsRequest) (*dnsmgr.ListRecordsResponse, error) {
	records, err := s.Store.GetRecords(ctx, req.GetZone(), req.GetName(), req.GetType())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get records: %v", err)
	}
	out := &dnsmgr.ListRecordsResponse{
		Records: make([]*dnsmgr.Record, 0, len(records)),
	}
	for _, r := range records {
		out.Records = append(out.Records, recordToProto(r))
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Conversions and helpers
// --------------------------------------------------------------------------

func zoneToProto(z *store.Zone) *dnsmgr.Zone {
	return &dnsmgr.Zone{
		Name:       z.Name,
		PrimaryNs:  z.PrimaryNS,
		AdminEmail: z.AdminEmail,
		Serial:     z.Serial,
		Refresh:    z.Refresh,
		Retry:      z.Retry,
		Expire:     z.Expire,
		MinimumTtl: z.MinTTL,
		CreatedAt:  timestamppb.New(z.CreatedAt),
		UpdatedAt:  timestamppb.New(z.UpdatedAt),
	}
}

func recordToProto(r *store.Record) *dnsmgr.Record {
	return &dnsmgr.Record{
		Zone:      r.Zone,
		Name:      r.Name,
		Type:      r.Type,
		Ttl:       r.TTL,
		Rdata:     r.RData,
		CreatedAt: timestamppb.New(r.CreatedAt),
	}
}

func defaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func defaultInt32(v, def int32) int32 {
	if v == 0 {
		return def
	}
	return v
}

// formatSOA renders a zone's SOA record in the presentation format
// consumed by dns.BuildRData.
func formatSOA(z *store.Zone) string {
	primary := z.PrimaryNS
	if primary == "" {
		primary = "ns1." + z.Name + "."
	}
	admin := z.AdminEmail
	if admin == "" {
		admin = "admin." + z.Name + "."
	}
	return primary + " " + admin + " " +
		itoa64(z.Serial) + " " +
		itoa32(z.Refresh) + " " +
		itoa32(z.Retry) + " " +
		itoa32(z.Expire) + " " +
		itoa32(z.MinTTL)
}

func itoa32(v int32) string { return itoa64(int64(v)) }
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
