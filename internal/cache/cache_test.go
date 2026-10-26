package cache

import (
	"fmt"
	"testing"
	"time"
)

func TestSetGetExpire(t *testing.T) {
	c := New[string](1024, 20*time.Millisecond)
	c.Set("a", "g", "1", c.Epoch())
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("got %q %v", v, ok)
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("entry should have expired")
	}
}

func TestEvictionBound(t *testing.T) {
	c := New[int](shardCount*4, time.Minute)
	for i := 0; i < 10000; i++ {
		c.Set(fmt.Sprint(i), "g", i, c.Epoch())
	}
	if n := c.Len(); n > shardCount*4 {
		t.Fatalf("cache grew to %d", n)
	}
}

func TestDeleteGroupAndStaleSet(t *testing.T) {
	c := New[int](1024, time.Minute)
	e := c.Epoch()
	c.Set("a", "z1", 1, e)
	c.Set("b", "z1", 2, e)
	c.Set("c", "z2", 3, e)
	c.DeleteGroup("z1")
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be gone")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should remain")
	}
	c.Set("d", "z2", 4, e)
	if _, ok := c.Get("d"); ok {
		t.Fatal("set with stale epoch must be dropped")
	}
}
