package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url, Options{MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "TRUNCATE zones CASCADE"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestZoneLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	z, err := s.CreateZone(ctx, &Zone{Name: "Example.COM.", PrimaryNS: "ns1.example.com.", AdminEmail: "h.example.com.", Refresh: 1, Retry: 2, Expire: 3, MinTTL: 4})
	if err != nil {
		t.Fatal(err)
	}
	if z.Name != "example.com" || z.Serial == 0 {
		t.Fatalf("unexpected zone %+v", z)
	}
	if _, err := s.CreateZone(ctx, &Zone{Name: "example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}

	got, err := s.FindZone(ctx, "a.b.example.com")
	if err != nil || got.Name != "example.com" {
		t.Fatalf("FindZone: %v %v", got, err)
	}
	if _, err := s.FindZone(ctx, "example.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindZone miss: %v", err)
	}
	if _, err := s.FindZone(ctx, "notexample.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindZone lookalike: %v", err)
	}

	if _, err := s.CreateZone(ctx, &Zone{Name: "sub.example.com", PrimaryNS: "n.", AdminEmail: "a."}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FindZone(ctx, "x.sub.example.com")
	if got.Name != "sub.example.com" {
		t.Fatalf("most specific zone expected, got %s", got.Name)
	}

	upd, err := s.UpdateZone(ctx, &Zone{Name: "example.com", PrimaryNS: "ns9.example.com.", AdminEmail: "h.example.com.", Refresh: 9, Retry: 9, Expire: 9, MinTTL: 9})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Serial <= z.Serial || upd.PrimaryNS != "ns9.example.com." {
		t.Fatalf("update did not bump serial: %d -> %d", z.Serial, upd.Serial)
	}
	if _, err := s.UpdateZone(ctx, &Zone{Name: "missing.com"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	zones, next, err := s.ListZones(ctx, 1, "")
	if err != nil || len(zones) != 1 || next == "" {
		t.Fatalf("page 1: %v %q %v", zones, next, err)
	}
	zones, next, err = s.ListZones(ctx, 1, next)
	if err != nil || len(zones) != 1 || next != "" {
		t.Fatalf("page 2: %v %q %v", zones, next, err)
	}

	if err := s.DeleteZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteZone(ctx, "example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestRecords(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	z, err := s.CreateZone(ctx, &Zone{Name: "example.com", PrimaryNS: "ns.", AdminEmail: "a."})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateRecord(ctx, &Record{Zone: "nozone.com", Name: "a", Type: "A", TTL: 60, RData: "1.1.1.1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record in missing zone: %v", err)
	}

	r, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "WWW", Type: "a", TTL: 60, RData: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "www.example.com" || r.Type != "A" {
		t.Fatalf("normalization: %+v", r)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "www.example.com.", Type: "A", TTL: 120, RData: "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "www", Type: "A", TTL: 30, RData: "192.0.2.1"}); err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}
	got, err := s.GetRecords(ctx, "example.com", "www", "A")
	if err != nil || len(got) != 2 {
		t.Fatalf("expected 2 distinct records, got %d (%v)", len(got), err)
	}
	for _, g := range got {
		if g.TTL != 30 {
			t.Fatalf("RRset TTL not unified: %d", g.TTL)
		}
	}

	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "www", Type: "CNAME", TTL: 60, RData: "x.example.com."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("CNAME beside A: %v", err)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "alias", Type: "CNAME", TTL: 60, RData: "www.example.com."}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "alias", Type: "A", TTL: 60, RData: "192.0.2.9"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("A beside CNAME: %v", err)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "alias", Type: "CNAME", TTL: 60, RData: "other.example.com."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second CNAME: %v", err)
	}
	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "@", Type: "CNAME", TTL: 60, RData: "x.example.com."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("apex CNAME: %v", err)
	}

	if _, err := s.CreateRecord(ctx, &Record{Zone: "example.com", Name: "_sip._tcp", Type: "SRV", TTL: 60, RData: "1 1 5060 sip.example.com."}); err != nil {
		t.Fatal(err)
	}
	set, err := s.LookupName(ctx, "example.com", "_tcp.example.com")
	if err != nil || len(set.Records) != 0 || !set.HasChildren {
		t.Fatalf("empty non-terminal: %+v %v", set, err)
	}
	set, _ = s.LookupName(ctx, "example.com", "nothing.example.com")
	if set.Exists() {
		t.Fatalf("nonexistent name reported as existing")
	}
	set, _ = s.LookupName(ctx, "example.com", "example.com")
	if !set.HasChildren {
		t.Fatalf("apex has descendants")
	}
	set, _ = s.LookupName(ctx, "example.com", "www.example.com")
	if len(set.Records) != 2 {
		t.Fatalf("LookupName records: %d", len(set.Records))
	}

	page, next, err := s.ListRecords(ctx, "example.com", 2, "")
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("list page 1: %d %q %v", len(page), next, err)
	}
	var total = len(page)
	for next != "" {
		page, next, err = s.ListRecords(ctx, "example.com", 2, next)
		if err != nil {
			t.Fatal(err)
		}
		total += len(page)
	}
	if total != 4 {
		t.Fatalf("listed %d records", total)
	}
	if _, _, err := s.ListRecords(ctx, "example.com", 2, "garbage"); err == nil {
		t.Fatal("bad token must fail")
	}

	if err := s.DeleteRecord(ctx, "example.com", "www", "A", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRecord(ctx, "example.com", "www", "A", "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	after, _ := s.GetZone(ctx, "example.com")
	if after.Serial <= z.Serial {
		t.Fatalf("serial not bumped by record changes")
	}
	if !after.UpdatedAt.After(time.Time{}) {
		t.Fatal("updated_at not set")
	}

	if err := s.DeleteZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	left, _, _ := s.ListRecords(ctx, "example.com", 10, "")
	if len(left) != 0 {
		t.Fatalf("records must cascade, %d left", len(left))
	}
}

func TestNormalizeName(t *testing.T) {
	cases := [][3]string{
		{"example.com", "@", "example.com"},
		{"example.com", "", "example.com"},
		{"example.com", "www", "www.example.com"},
		{"example.com", "WWW.Example.com.", "www.example.com"},
		{"example.com", "example.com", "example.com"},
		{"example.com", "a.b", "a.b.example.com"},
	}
	for _, c := range cases {
		if got := NormalizeName(c[0], c[1]); got != c[2] {
			t.Errorf("NormalizeName(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
