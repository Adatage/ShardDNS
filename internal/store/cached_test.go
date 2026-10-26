package store

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingResolver struct {
	zoneCalls atomic.Int32
	nameCalls atomic.Int32
	records   atomic.Int32
}

func (c *countingResolver) FindZone(_ context.Context, q string) (*Zone, error) {
	c.zoneCalls.Add(1)
	if q == "example.com" || q == "www.example.com" {
		return &Zone{Name: "example.com"}, nil
	}
	return nil, ErrNotFound
}

func (c *countingResolver) LookupName(_ context.Context, zone, name string) (*NameSet, error) {
	c.nameCalls.Add(1)
	set := &NameSet{}
	for i := int32(0); i < c.records.Load(); i++ {
		set.Records = append(set.Records, &Record{Name: name})
	}
	return set, nil
}

func TestCachedResolver(t *testing.T) {
	next := &countingResolver{}
	next.records.Store(1)
	c := NewCachedResolver(next, 1000, time.Minute)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if z, err := c.FindZone(ctx, "www.example.com"); err != nil || z.Name != "example.com" {
			t.Fatal(z, err)
		}
		if _, err := c.FindZone(ctx, "x.org"); err != ErrNotFound {
			t.Fatal(err)
		}
		if set, _ := c.LookupName(ctx, "example.com", "www.example.com"); len(set.Records) != 1 {
			t.Fatal("bad set")
		}
	}
	if next.zoneCalls.Load() != 2 || next.nameCalls.Load() != 1 {
		t.Fatalf("expected negative+positive caching, got zone=%d name=%d", next.zoneCalls.Load(), next.nameCalls.Load())
	}

	next.records.Store(2)
	c.Invalidate("example.com", "www.example.com")
	if set, _ := c.LookupName(ctx, "example.com", "www.example.com"); len(set.Records) != 2 {
		t.Fatal("record invalidation did not refresh")
	}

	c.LookupName(ctx, "example.com", "a.example.com")
	c.LookupName(ctx, "example.com", "example.com")
	before := next.nameCalls.Load()
	c.Invalidate("example.com", "b.a.example.com")
	c.LookupName(ctx, "example.com", "a.example.com")
	c.LookupName(ctx, "example.com", "example.com")
	if next.nameCalls.Load() != before+2 {
		t.Fatal("ancestors must be invalidated for empty-non-terminal correctness")
	}

	c.Invalidate("example.com", "")
	n := next.nameCalls.Load()
	c.LookupName(ctx, "example.com", "www.example.com")
	if next.nameCalls.Load() != n+1 {
		t.Fatal("zone invalidation must purge names")
	}
}
