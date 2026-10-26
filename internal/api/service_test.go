package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
	"github.com/Adatage/ShardDNS/api/sharddns/v1/sharddnsv1connect"
	"github.com/Adatage/ShardDNS/internal/api"
	"github.com/Adatage/ShardDNS/internal/store"

	"connectrpc.com/connect"
)

func setup(t *testing.T, token string) (sharddnsv1connect.DNSManagerClient, *[]string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url, store.Options{MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, z := range []string{"api.test", "other.test"} {
		_ = st.DeleteZone(ctx, z)
	}

	var mu sync.Mutex
	var notified []string
	svc := api.NewService(st, func(_ context.Context, kind sharddnsv1.ChangeEvent_Kind, zone, name string) {
		mu.Lock()
		defer mu.Unlock()
		notified = append(notified, kind.String()+":"+zone+":"+name)
	}, nil)

	var opts []connect.HandlerOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(api.NewAuthInterceptor(token)))
	}
	mux := http.NewServeMux()
	mux.Handle(sharddnsv1connect.NewDNSManagerHandler(svc, opts...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return sharddnsv1connect.NewDNSManagerClient(srv.Client(), srv.URL), &notified
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func TestService(t *testing.T) {
	c, notified := setup(t, "")
	ctx := context.Background()

	z, err := c.CreateZone(ctx, connect.NewRequest(&sharddnsv1.CreateZoneRequest{Name: "API.test."}))
	if err != nil {
		t.Fatal(err)
	}
	if z.Msg.GetName() != "api.test" || z.Msg.GetPrimaryNs() != "ns1.api.test." || z.Msg.GetMinimumTtl() != 300 {
		t.Fatalf("defaults: %+v", z.Msg)
	}
	if _, err := c.CreateZone(ctx, connect.NewRequest(&sharddnsv1.CreateZoneRequest{Name: "api.test"})); code(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate zone: %v", err)
	}
	if _, err := c.CreateZone(ctx, connect.NewRequest(&sharddnsv1.CreateZoneRequest{Name: "bad name"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid zone name: %v", err)
	}
	if _, err := c.GetZone(ctx, connect.NewRequest(&sharddnsv1.GetZoneRequest{Name: "missing.test"})); code(err) != connect.CodeNotFound {
		t.Fatalf("missing zone: %v", err)
	}

	up, err := c.UpdateZone(ctx, connect.NewRequest(&sharddnsv1.UpdateZoneRequest{Name: "api.test", MinimumTtl: 42}))
	if err != nil || up.Msg.GetMinimumTtl() != 42 || up.Msg.GetRefresh() != 3600 || up.Msg.GetSerial() <= z.Msg.GetSerial() {
		t.Fatalf("partial update: %+v %v", up, err)
	}

	bad := []*sharddnsv1.CreateRecordRequest{
		{Zone: "api.test", Name: "a", Type: "A", Rdata: "not-an-ip"},
		{Zone: "api.test", Name: "a", Type: "SOA", Rdata: "x"},
		{Zone: "api.test", Name: "a", Type: "BOGUS", Rdata: "x"},
		{Zone: "api.test", Name: "a b", Type: "A", Rdata: "1.2.3.4"},
		{Zone: "api.test", Name: "a", Type: "A", Rdata: "1.2.3.4", Ttl: -1},
		{Zone: "api.test", Name: "a", Type: "MX", Rdata: "mail.api.test"},
		{Name: "a", Type: "A"},
	}
	for i, req := range bad {
		if _, err := c.CreateRecord(ctx, connect.NewRequest(req)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("bad[%d]: expected InvalidArgument, got %v", i, err)
		}
	}
	if _, err := c.CreateRecord(ctx, connect.NewRequest(&sharddnsv1.CreateRecordRequest{Zone: "nozone.test", Name: "a", Type: "A", Rdata: "1.2.3.4"})); code(err) != connect.CodeNotFound {
		t.Fatalf("record in missing zone: %v", err)
	}

	rec, err := c.CreateRecord(ctx, connect.NewRequest(&sharddnsv1.CreateRecordRequest{Zone: "api.test", Name: "www", Type: "a", Rdata: "192.0.2.1"}))
	if err != nil || rec.Msg.GetName() != "www.api.test" || rec.Msg.GetTtl() != 300 || rec.Msg.GetType() != "A" {
		t.Fatalf("create record: %+v %v", rec, err)
	}
	if _, err := c.CreateRecord(ctx, connect.NewRequest(&sharddnsv1.CreateRecordRequest{Zone: "api.test", Name: "www", Type: "CNAME", Rdata: "x.api.test."})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CNAME conflict: %v", err)
	}

	got, err := c.GetRecords(ctx, connect.NewRequest(&sharddnsv1.GetRecordsRequest{Zone: "api.test", Name: "www", Type: "A"}))
	if err != nil || len(got.Msg.GetRecords()) != 1 {
		t.Fatalf("get records: %v", err)
	}
	list, err := c.ListRecords(ctx, connect.NewRequest(&sharddnsv1.ListRecordsRequest{Zone: "api.test"}))
	if err != nil || len(list.Msg.GetRecords()) != 1 {
		t.Fatalf("list records: %v", err)
	}
	if _, err := c.ListRecords(ctx, connect.NewRequest(&sharddnsv1.ListRecordsRequest{Zone: "missing.test"})); code(err) != connect.CodeNotFound {
		t.Fatalf("list in missing zone: %v", err)
	}

	zones, err := c.ListZones(ctx, connect.NewRequest(&sharddnsv1.ListZonesRequest{}))
	if err != nil || len(zones.Msg.GetZones()) == 0 {
		t.Fatalf("list zones: %v", err)
	}

	if _, err := c.DeleteRecord(ctx, connect.NewRequest(&sharddnsv1.DeleteRecordRequest{Zone: "api.test", Name: "www", Type: "A", Rdata: "192.0.2.1"})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteRecord(ctx, connect.NewRequest(&sharddnsv1.DeleteRecordRequest{Zone: "api.test", Name: "www", Type: "A", Rdata: "192.0.2.1"})); code(err) != connect.CodeNotFound {
		t.Fatalf("double delete: %v", err)
	}
	if _, err := c.DeleteZone(ctx, connect.NewRequest(&sharddnsv1.DeleteZoneRequest{Name: "api.test"})); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"KIND_ZONE:api.test:", "KIND_ZONE:api.test:", "KIND_RECORD:api.test:www.api.test",
		"KIND_RECORD:api.test:www.api.test", "KIND_ZONE:api.test:",
	}
	if len(*notified) != len(want) {
		t.Fatalf("notifications %v, want %v", *notified, want)
	}
	for i := range want {
		if (*notified)[i] != want[i] {
			t.Fatalf("notification %d = %q, want %q", i, (*notified)[i], want[i])
		}
	}
}

func TestAuth(t *testing.T) {
	c, _ := setup(t, "s3cret")
	ctx := context.Background()

	if _, err := c.ListZones(ctx, connect.NewRequest(&sharddnsv1.ListZonesRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("no token: %v", err)
	}
	req := connect.NewRequest(&sharddnsv1.ListZonesRequest{})
	req.Header().Set("Authorization", "Bearer wrong")
	if _, err := c.ListZones(ctx, req); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: %v", err)
	}
	req = connect.NewRequest(&sharddnsv1.ListZonesRequest{})
	req.Header().Set("Authorization", "Bearer s3cret")
	if _, err := c.ListZones(ctx, req); err != nil {
		t.Fatalf("valid token: %v", err)
	}
}
