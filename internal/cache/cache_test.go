package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewLRU[string, int](2, time.Hour)
	c.Put("a", 1)
	c.Put("b", 2)

	if _, ok := c.Get("a"); !ok { // refresh a
		t.Fatal("a missing")
	}
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("a = %v,%v", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Errorf("c = %v,%v", v, ok)
	}
	if c.Len() != 2 {
		t.Errorf("len = %d, want 2", c.Len())
	}
}

func TestLRUExpires(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewLRU[string, int](4, time.Minute)
	c.SetClock(func() time.Time { return now })

	c.Put("a", 1)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should be live")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Error("a should be expired")
	}
	if c.Len() != 0 {
		t.Errorf("len = %d, want 0 after expiry", c.Len())
	}
}

func TestLRUReplacesWithoutGrowing(t *testing.T) {
	c := NewLRU[string, int](2, 0)
	c.Put("a", 1)
	c.Put("a", 2)
	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}
	if v, _ := c.Get("a"); v != 2 {
		t.Errorf("a = %d, want 2", v)
	}

	c.Delete("a")
	if c.Len() != 0 {
		t.Errorf("len = %d after delete", c.Len())
	}
}

func TestGroupCollapsesConcurrentCalls(t *testing.T) {
	var g Group[string, int]
	var calls atomic.Int32

	release := make(chan struct{})
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	}

	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := g.Do(context.Background(), "key", fn)
			if err != nil {
				t.Errorf("Do: %v", err)
			}
			results[i] = v
		}(i)
	}

	time.Sleep(20 * time.Millisecond) // let the goroutines join
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
	for i, v := range results {
		if v != 42 {
			t.Errorf("results[%d] = %d", i, v)
		}
	}
}
