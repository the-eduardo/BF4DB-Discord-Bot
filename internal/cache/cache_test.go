package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestGetSetAndExpiry(t *testing.T) {
	c := New[string](time.Minute, 10)
	now := time.Now()
	c.now = func() time.Time { return now }

	if _, ok := c.Get("missing"); ok {
		t.Error("empty cache returned a value")
	}

	c.Set("k", "v")
	if got, ok := c.Get("k"); !ok || got != "v" {
		t.Errorf("Get = %q, %v", got, ok)
	}

	now = now.Add(59 * time.Second)
	if _, ok := c.Get("k"); !ok {
		t.Error("entry expired early")
	}

	now = now.Add(2 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Error("expired entry still returned")
	}
	if c.Len() != 0 {
		t.Errorf("expired entry not dropped: %d", c.Len())
	}
}

func TestEvictionKeepsCacheBounded(t *testing.T) {
	c := New[int](time.Minute, 3)
	now := time.Now()
	c.now = func() time.Time { return now }

	for i := range 10 {
		c.Set(strconv.Itoa(i), i)
		now = now.Add(time.Second) // later entries expire later
	}
	if c.Len() > 3 {
		t.Errorf("cache holds %d entries, want at most 3", c.Len())
	}
	if _, ok := c.Get("9"); !ok {
		t.Error("most recent entry was evicted")
	}
}

func TestReplacingAKeyDoesNotEvict(t *testing.T) {
	c := New[int](time.Minute, 2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("a", 3)

	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
	if got, _ := c.Get("a"); got != 3 {
		t.Errorf("a = %d, want the replacement 3", got)
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("b was evicted by a replacement")
	}
}

func TestConcurrentUse(t *testing.T) {
	c := New[int](time.Minute, 50)
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := strconv.Itoa(i % 10)
			c.Set(key, i)
			c.Get(key)
		}()
	}
	wg.Wait()
}
