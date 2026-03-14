package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

//  helpers

func newTestStore(window time.Duration, interval time.Duration) *MemoryStore {
	cfg := &RateLimitConfig{
		Window:          window,
		CleanupInterval: interval,
	}
	return NewMemoryStore(cfg)
}

//  Unit Tests

func TestRateLimitBasic(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const limit = 5
	for i := 0; i < limit; i++ {
		res, err := s.CheckRateLimit("tenant:basic", limit, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// The (limit+1)th request must be rejected.
	res, _ := s.CheckRateLimit("tenant:basic", limit, time.Minute)
	if res.Allowed {
		t.Fatal("6th request should have been denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("remaining should be 0, got %d", res.Remaining)
	}
}

func TestRateLimitBurst(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	// 100 base + 20 burst = 120 total
	limit := TierLimit{RequestsPerWindow: 100, BurstAllowance: 20}
	max := limit.GetMaxRequests() // 120

	for i := 0; i < max; i++ {
		res, _ := s.CheckRateLimit("tenant:burst", max, time.Minute)
		if !res.Allowed {
			t.Fatalf("request %d of %d should be allowed", i+1, max)
		}
	}

	res, _ := s.CheckRateLimit("tenant:burst", max, time.Minute)
	if res.Allowed {
		t.Fatalf("request %d should have been denied (burst exceeded)", max+1)
	}
}

func TestRateLimitSlidingWindow(t *testing.T) {
	// Very short window so we can simulate expiry fast.
	s := newTestStore(200*time.Millisecond, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const limit = 3
	const key = "tenant:sliding"

	// Fill window.
	for i := 0; i < limit; i++ {
		s.CheckRateLimit(key, limit, 200*time.Millisecond) //nolint:errcheck
	}

	// Window not yet expired — next should be denied.
	res, _ := s.CheckRateLimit(key, limit, 200*time.Millisecond)
	if res.Allowed {
		t.Fatal("should be denied while window is active")
	}

	// Wait for window to expire.
	time.Sleep(250 * time.Millisecond)

	// Now should be allowed again.
	res, _ = s.CheckRateLimit(key, limit, 200*time.Millisecond)
	if !res.Allowed {
		t.Fatal("should be allowed after window expires")
	}
}

func TestRateLimitReset(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const limit = 2
	const key = "tenant:reset"

	// Exhaust the limit.
	for i := 0; i < limit; i++ {
		s.CheckRateLimit(key, limit, time.Minute) //nolint:errcheck
	}
	res, _ := s.CheckRateLimit(key, limit, time.Minute)
	if res.Allowed {
		t.Fatal("should be at limit before reset")
	}

	// Reset and verify requests are allowed again.
	if err := s.ResetLimit(key); err != nil {
		t.Fatalf("ResetLimit error: %v", err)
	}
	res, _ = s.CheckRateLimit(key, limit, time.Minute)
	if !res.Allowed {
		t.Fatal("should be allowed after reset")
	}
}

func TestRateLimitGetCurrentCount(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const key = "tenant:count"
	for i := 0; i < 5; i++ {
		s.CheckRateLimit(key, 100, time.Minute) //nolint:errcheck
	}

	count, err := s.GetCurrentCount(key, time.Minute)
	if err != nil {
		t.Fatalf("GetCurrentCount error: %v", err)
	}
	if count != 5 {
		t.Fatalf("expected count=5, got %d", count)
	}
}

func TestRateLimitConcurrency(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const (
		goroutines = 10
		perG       = 20
		limit      = 1000 // high enough that all succeed
	)

	var wg sync.WaitGroup
	errors := make(chan string, goroutines*perG)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("tenant:concurrent:%d", id)
			for i := 0; i < perG; i++ {
				res, err := s.CheckRateLimit(key, limit, time.Minute)
				if err != nil {
					errors <- fmt.Sprintf("error in goroutine %d: %v", id, err)
				}
				if !res.Allowed {
					errors <- fmt.Sprintf("goroutine %d request %d denied unexpectedly", id, i)
				}
			}
		}(g)
	}

	wg.Wait()
	close(errors)

	for msg := range errors {
		t.Error(msg)
	}
}

func TestRateLimitSharedKeyConcurrency(t *testing.T) {
	// Multiple goroutines hammering the SAME key — verifies no race conditions.
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	const (
		goroutines = 20
		perG       = 50
		limit      = 2000
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				s.CheckRateLimit("shared:key", limit, time.Minute) //nolint:errcheck
			}
		}()
	}
	wg.Wait()

	count, _ := s.GetCurrentCount("shared:key", time.Minute)
	if count != goroutines*perG {
		t.Fatalf("expected %d, got %d (possible race condition)", goroutines*perG, count)
	}
}

func TestRateLimitCleanup(t *testing.T) {
	const window = 100 * time.Millisecond
	const interval = 150 * time.Millisecond

	s := newTestStore(window, interval)
	defer s.Close() //nolint:errcheck

	// Add requests inside the window.
	for i := 0; i < 5; i++ {
		s.CheckRateLimit("tenant:cleanup", 100, window) //nolint:errcheck
	}

	// Wait for window to fully expire + cleanup to run.
	time.Sleep(400 * time.Millisecond)

	count, _ := s.GetCurrentCount("tenant:cleanup", window)
	if count != 0 {
		t.Fatalf("expected 0 after window expiry, got %d", count)
	}
}

func TestRateLimitHealthCheck(t *testing.T) {
	s := newTestStore(time.Minute, 10*time.Minute)
	defer s.Close() //nolint:errcheck

	// Empty store should be healthy.
	if err := s.HealthCheck(); err != nil {
		t.Fatalf("unexpected health check error: %v", err)
	}
}

//  Config Tests

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("DefaultConfig should be enabled")
	}
	if cfg.Window != time.Minute {
		t.Errorf("expected 1m window, got %v", cfg.Window)
	}
	if cfg.TierLimits[TierFree].GetMaxRequests() != 120 {
		t.Errorf("Free max should be 120, got %d", cfg.TierLimits[TierFree].GetMaxRequests())
	}
	if cfg.TierLimits[TierBuilder].GetMaxRequests() != 1200 {
		t.Errorf("Builder max should be 1200, got %d", cfg.TierLimits[TierBuilder].GetMaxRequests())
	}
	if !cfg.TierLimits[TierEnterprise].Unlimited {
		t.Error("Enterprise should be unlimited")
	}
}

func TestIsPathExcluded(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		path     string
		excluded bool
	}{
		{"/ping", true},
		{"/health", true},
		{"/metrics", true},
		{"/v1/health", true},
		{"/v1/entities", false},
		{"/v1/dashboard", false},
	}
	for _, tc := range cases {
		if got := cfg.IsPathExcluded(tc.path); got != tc.excluded {
			t.Errorf("IsPathExcluded(%q) = %v, want %v", tc.path, got, tc.excluded)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "less than a second"},
		{30 * time.Second, "30 seconds"},
		{90 * time.Second, "1 minutes and 30 seconds"},
		{time.Minute, "1 minutes"},
		{90 * time.Minute, "1 hours and 30 minutes"},
		{2 * time.Hour, "2 hours"},
	}
	for _, tc := range cases {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

//  Benchmark

func BenchmarkCheckRateLimit(b *testing.B) {
	cfg := DefaultConfig()
	s := NewMemoryStore(cfg)
	defer s.Close() //nolint:errcheck

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.CheckRateLimit("bench-key", 1000, time.Minute) //nolint:errcheck
		}
	})
}

func BenchmarkCheckRateLimitDistinctKeys(b *testing.B) {
	cfg := DefaultConfig()
	s := NewMemoryStore(cfg)
	defer s.Close() //nolint:errcheck

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("tenant:%d", i%1000)
		s.CheckRateLimit(key, 1000, time.Minute) //nolint:errcheck
	}
}
