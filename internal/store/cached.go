package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Adatage/ShardDNS/internal/cache"

	"golang.org/x/sync/singleflight"
)

type zoneResult struct {
	zone *Zone
}

type CachedResolver struct {
	next  Resolver
	zones *cache.Cache[zoneResult]
	names *cache.Cache[*NameSet]
	group singleflight.Group
}

func NewCachedResolver(next Resolver, maxEntries int, ttl time.Duration) *CachedResolver {
	return &CachedResolver{
		next:  next,
		zones: cache.New[zoneResult](maxEntries/8+64, ttl),
		names: cache.New[*NameSet](maxEntries, ttl),
	}
}

func (c *CachedResolver) FindZone(ctx context.Context, qname string) (*Zone, error) {
	if r, ok := c.zones.Get(qname); ok {
		if r.zone == nil {
			return nil, ErrNotFound
		}
		return r.zone, nil
	}
	v, err, _ := c.group.Do("z\x00"+qname, func() (any, error) {
		epoch := c.zones.Epoch()
		lctx, cancel := loadContext(ctx)
		defer cancel()
		z, err := c.next.FindZone(lctx, qname)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		c.zones.Set(qname, "", zoneResult{zone: z}, epoch)
		return z, nil
	})
	if err != nil {
		return nil, err
	}
	z, _ := v.(*Zone)
	if z == nil {
		return nil, ErrNotFound
	}
	return z, nil
}

func (c *CachedResolver) LookupName(ctx context.Context, zone, name string) (*NameSet, error) {
	key := zone + "\x00" + name
	if set, ok := c.names.Get(key); ok {
		return set, nil
	}
	v, err, _ := c.group.Do("n\x00"+key, func() (any, error) {
		epoch := c.names.Epoch()
		lctx, cancel := loadContext(ctx)
		defer cancel()
		set, err := c.next.LookupName(lctx, zone, name)
		if err != nil {
			return nil, err
		}
		c.names.Set(key, zone, set, epoch)
		return set, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*NameSet), nil
}

func (c *CachedResolver) Invalidate(zone, name string) {
	c.zones.Clear()
	if name == "" {
		c.names.DeleteGroup(zone)
		return
	}
	for {
		c.names.Delete(zone + "\x00" + name)
		if name == zone {
			return
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return
		}
		name = name[i+1:]
	}
}

func (c *CachedResolver) Stats() (hits, misses uint64, entries int) {
	zh, zm := c.zones.Stats()
	nh, nm := c.names.Stats()
	return zh + nh, zm + nm, c.zones.Len() + c.names.Len()
}

func loadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (c *CachedResolver) InvalidateAll() {
	c.zones.Clear()
	c.names.Clear()
}
