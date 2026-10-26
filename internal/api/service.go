package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
	"github.com/Adatage/ShardDNS/internal/dns"
	"github.com/Adatage/ShardDNS/internal/store"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultPageSize = 100
	maxPageSize     = 1000
	defaultTTL      = 300
	maxTTL          = 1<<31 - 1
)

type ChangeNotifier func(ctx context.Context, kind sharddnsv1.ChangeEvent_Kind, zone, name string)

type Service struct {
	store  *store.Store
	notify ChangeNotifier
	logger *slog.Logger
}

func NewService(s *store.Store, notify ChangeNotifier, logger *slog.Logger) *Service {
	if notify == nil {
		notify = func(context.Context, sharddnsv1.ChangeEvent_Kind, string, string) {}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: s, notify: notify, logger: logger}
}

func (s *Service) CreateZone(ctx context.Context, req *connect.Request[sharddnsv1.CreateZoneRequest]) (*connect.Response[sharddnsv1.Zone], error) {
	m := req.Msg
	name := store.NormalizeZone(m.GetName())
	if err := dns.ValidateName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	z := &store.Zone{
		Name:       name,
		PrimaryNS:  orString(m.GetPrimaryNs(), "ns1."+name+"."),
		AdminEmail: orString(m.GetAdminEmail(), "hostmaster."+name+"."),
		Refresh:    orInt(m.GetRefresh(), 3600),
		Retry:      orInt(m.GetRetry(), 900),
		Expire:     orInt(m.GetExpire(), 604800),
		MinTTL:     orInt(m.GetMinimumTtl(), 300),
	}
	if err := validateSOA(z); err != nil {
		return nil, err
	}
	created, err := s.store.CreateZone(ctx, z)
	if err != nil {
		return nil, s.toError("create zone", err, connect.CodeAlreadyExists)
	}
	s.notify(ctx, sharddnsv1.ChangeEvent_KIND_ZONE, created.Name, "")
	return connect.NewResponse(zoneToProto(created)), nil
}

func (s *Service) GetZone(ctx context.Context, req *connect.Request[sharddnsv1.GetZoneRequest]) (*connect.Response[sharddnsv1.Zone], error) {
	z, err := s.store.GetZone(ctx, req.Msg.GetName())
	if err != nil {
		return nil, s.toError("get zone", err, connect.CodeInternal)
	}
	return connect.NewResponse(zoneToProto(z)), nil
}

func (s *Service) UpdateZone(ctx context.Context, req *connect.Request[sharddnsv1.UpdateZoneRequest]) (*connect.Response[sharddnsv1.Zone], error) {
	m := req.Msg
	z, err := s.store.GetZone(ctx, m.GetName())
	if err != nil {
		return nil, s.toError("update zone", err, connect.CodeInternal)
	}
	z.PrimaryNS = orString(m.GetPrimaryNs(), z.PrimaryNS)
	z.AdminEmail = orString(m.GetAdminEmail(), z.AdminEmail)
	z.Refresh = orInt(m.GetRefresh(), z.Refresh)
	z.Retry = orInt(m.GetRetry(), z.Retry)
	z.Expire = orInt(m.GetExpire(), z.Expire)
	z.MinTTL = orInt(m.GetMinimumTtl(), z.MinTTL)
	if err := validateSOA(z); err != nil {
		return nil, err
	}
	updated, err := s.store.UpdateZone(ctx, z)
	if err != nil {
		return nil, s.toError("update zone", err, connect.CodeInternal)
	}
	s.notify(ctx, sharddnsv1.ChangeEvent_KIND_ZONE, updated.Name, "")
	return connect.NewResponse(zoneToProto(updated)), nil
}

func (s *Service) DeleteZone(ctx context.Context, req *connect.Request[sharddnsv1.DeleteZoneRequest]) (*connect.Response[emptypb.Empty], error) {
	name := store.NormalizeZone(req.Msg.GetName())
	if err := s.store.DeleteZone(ctx, name); err != nil {
		return nil, s.toError("delete zone", err, connect.CodeInternal)
	}
	s.notify(ctx, sharddnsv1.ChangeEvent_KIND_ZONE, name, "")
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) ListZones(ctx context.Context, req *connect.Request[sharddnsv1.ListZonesRequest]) (*connect.Response[sharddnsv1.ListZonesResponse], error) {
	zones, next, err := s.store.ListZones(ctx, pageSize(req.Msg.GetPageSize()), req.Msg.GetPageToken())
	if err != nil {
		return nil, s.toError("list zones", err, connect.CodeInternal)
	}
	out := &sharddnsv1.ListZonesResponse{Zones: make([]*sharddnsv1.Zone, 0, len(zones)), NextPageToken: next}
	for _, z := range zones {
		out.Zones = append(out.Zones, zoneToProto(z))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) CreateRecord(ctx context.Context, req *connect.Request[sharddnsv1.CreateRecordRequest]) (*connect.Response[sharddnsv1.Record], error) {
	m := req.Msg
	zone := store.NormalizeZone(m.GetZone())
	if zone == "" || m.GetName() == "" || m.GetType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("zone, name and type are required"))
	}
	rtype := store.NormalizeType(m.GetType())
	code := dns.TypeCode(rtype)
	if rtype == "SOA" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("SOA is derived from the zone; use UpdateZone"))
	}
	if !dns.SupportedType(code) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported record type "+rtype))
	}
	name := store.NormalizeName(zone, m.GetName())
	if err := dns.ValidateName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	rdata := strings.TrimSpace(m.GetRdata())
	if _, err := dns.BuildRData(code, rdata); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ttl := m.GetTtl()
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < 0 || ttl > maxTTL {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ttl out of range"))
	}

	rec, err := s.store.CreateRecord(ctx, &store.Record{Zone: zone, Name: name, Type: rtype, TTL: ttl, RData: rdata})
	if err != nil {
		return nil, s.toError("create record", err, connect.CodeFailedPrecondition)
	}
	s.notify(ctx, sharddnsv1.ChangeEvent_KIND_RECORD, zone, name)
	return connect.NewResponse(recordToProto(rec)), nil
}

func (s *Service) DeleteRecord(ctx context.Context, req *connect.Request[sharddnsv1.DeleteRecordRequest]) (*connect.Response[emptypb.Empty], error) {
	m := req.Msg
	zone := store.NormalizeZone(m.GetZone())
	name := store.NormalizeName(zone, m.GetName())
	if err := s.store.DeleteRecord(ctx, zone, name, m.GetType(), strings.TrimSpace(m.GetRdata())); err != nil {
		return nil, s.toError("delete record", err, connect.CodeInternal)
	}
	s.notify(ctx, sharddnsv1.ChangeEvent_KIND_RECORD, zone, name)
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) ListRecords(ctx context.Context, req *connect.Request[sharddnsv1.ListRecordsRequest]) (*connect.Response[sharddnsv1.ListRecordsResponse], error) {
	m := req.Msg
	if m.GetZone() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("zone is required"))
	}
	if _, err := s.store.GetZone(ctx, m.GetZone()); err != nil {
		return nil, s.toError("list records", err, connect.CodeInternal)
	}
	records, next, err := s.store.ListRecords(ctx, m.GetZone(), pageSize(m.GetPageSize()), m.GetPageToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	out := &sharddnsv1.ListRecordsResponse{Records: make([]*sharddnsv1.Record, 0, len(records)), NextPageToken: next}
	for _, r := range records {
		out.Records = append(out.Records, recordToProto(r))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) GetRecords(ctx context.Context, req *connect.Request[sharddnsv1.GetRecordsRequest]) (*connect.Response[sharddnsv1.GetRecordsResponse], error) {
	m := req.Msg
	records, err := s.store.GetRecords(ctx, m.GetZone(), m.GetName(), m.GetType())
	if err != nil {
		return nil, s.toError("get records", err, connect.CodeInternal)
	}
	out := &sharddnsv1.GetRecordsResponse{Records: make([]*sharddnsv1.Record, 0, len(records))}
	for _, r := range records {
		out.Records = append(out.Records, recordToProto(r))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) toError(op string, err error, conflictCode connect.Code) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New(op+": not found"))
	case errors.Is(err, store.ErrConflict):
		return connect.NewError(conflictCode, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	s.logger.Error(op+" failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New(op+" failed"))
}

func NewAuthInterceptor(token string) connect.Interceptor {
	want := []byte("Bearer " + token)
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			got := []byte(req.Header().Get("Authorization"))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing bearer token"))
			}
			return next(ctx, req)
		}
	})
}

func validateSOA(z *store.Zone) error {
	for _, n := range []string{z.PrimaryNS, strings.Replace(z.AdminEmail, "@", ".", 1)} {
		if _, err := dns.PackName(nil, strings.ToLower(n)); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	if z.Refresh < 0 || z.Retry < 0 || z.Expire < 0 || z.MinTTL < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("SOA timers must not be negative"))
	}
	return nil
}

func pageSize(n int32) int {
	switch {
	case n <= 0:
		return defaultPageSize
	case n > maxPageSize:
		return maxPageSize
	}
	return int(n)
}

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v, def int32) int32 {
	if v == 0 {
		return def
	}
	return v
}

func zoneToProto(z *store.Zone) *sharddnsv1.Zone {
	return &sharddnsv1.Zone{
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

func recordToProto(r *store.Record) *sharddnsv1.Record {
	return &sharddnsv1.Record{
		Zone:      r.Zone,
		Name:      r.Name,
		Type:      r.Type,
		Ttl:       r.TTL,
		Rdata:     r.RData,
		CreatedAt: timestamppb.New(r.CreatedAt),
	}
}
