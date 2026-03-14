package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// requestRecord represents a single HTTP request in the sliding window.
type requestRecord struct {
	Timestamp time.Time
	Count     int // Always 1 for individual requests; reserved for future batching.
}

// rateLimitBucket holds all recorded requests for one tenant key.
type rateLimitBucket struct {
	Requests []requestRecord
	mu       sync.RWMutex
}

// MemoryStore is a thread-safe, in-memory rate limit store.
// It uses sync.Map for lock-free bucket lookup and per-bucket mutexes
// for fine-grained concurrent access.
type MemoryStore struct {
	buckets  sync.Map
	config   *RateLimitConfig
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// RateLimitResult contains the outcome of a rate limit check.
type RateLimitResult struct {
	// Allowed is true when the request should be permitted.
	Allowed bool
	// Limit is the maximum requests allowed in the window.
	Limit int
	// Remaining is how many more requests can be made before hitting the limit.
	Remaining int
	// ResetAt is the UTC time when the oldest in-window request expires.
	ResetAt time.Time
	// RetryAfter is seconds until the client may retry.
	RetryAfter int64
	// CurrentCount is the number of requests counted in the current window.
	CurrentCount int
}

// NewMemoryStore creates and initialises a MemoryStore, starting the
// background cleanup goroutine immediately.
func NewMemoryStore(config *RateLimitConfig) *MemoryStore {
	s := &MemoryStore{
		config:   config,
		stopChan: make(chan struct{}),
	}
	s.startCleanupRoutine()
	return s
}

// CheckRateLimit implements the sliding-window rate limit algorithm.
// It is safe for concurrent use by multiple goroutines.
//
// Algorithm:
//  1. Load or create the bucket for the key.
//  2. Evict records older than the window start.
//  3. Allow the request when count < limit; reject otherwise.
//  4. Return metadata needed for HTTP headers and 429 responses.
func (s *MemoryStore) CheckRateLimit(key string, limit int, window time.Duration) (*RateLimitResult, error) {
	now := time.Now().UTC()
	windowStart := now.Add(-window)

	// LoadOrStore is atomic: returns existing or newly stored bucket.
	raw, _ := s.buckets.LoadOrStore(key, &rateLimitBucket{
		Requests: make([]requestRecord, 0, limit*2),
	})
	bucket := raw.(*rateLimitBucket)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	// Evict expired records — allocate fresh slice to reclaim memory.
	valid := make([]requestRecord, 0, len(bucket.Requests))
	for _, r := range bucket.Requests {
		if r.Timestamp.After(windowStart) {
			valid = append(valid, r)
		}
	}
	bucket.Requests = valid

	currentCount := len(bucket.Requests)
	allowed := currentCount < limit

	if allowed {
		bucket.Requests = append(bucket.Requests, requestRecord{
			Timestamp: now,
			Count:     1,
		})
		currentCount++
	}

	// Reset time = when the oldest in-window request expires.
	resetAt := now.Add(window)
	if len(bucket.Requests) > 0 {
		resetAt = bucket.Requests[0].Timestamp.Add(window)
	}

	remaining := limit - currentCount
	if remaining < 0 {
		remaining = 0
	}

	return &RateLimitResult{
		Allowed:      allowed,
		Limit:        limit,
		Remaining:    remaining,
		ResetAt:      resetAt,
		RetryAfter:   max0(int64(time.Until(resetAt).Seconds())),
		CurrentCount: currentCount,
	}, nil
}

// ResetLimit deletes the rate limit bucket for the given key, effectively
// resetting the tenant's request counter.
func (s *MemoryStore) ResetLimit(key string) error {
	s.buckets.Delete(key)
	return nil
}

// GetCurrentCount returns the number of requests recorded for key within window.
// It evicts stale records but does NOT add a new entry.
func (s *MemoryStore) GetCurrentCount(key string, window time.Duration) (int, error) {
	raw, ok := s.buckets.Load(key)
	if !ok {
		return 0, nil
	}
	bucket := raw.(*rateLimitBucket)

	windowStart := time.Now().UTC().Add(-window)
	bucket.mu.RLock()
	defer bucket.mu.RUnlock()

	count := 0
	for _, r := range bucket.Requests {
		if r.Timestamp.After(windowStart) {
			count++
		}
	}
	return count, nil
}

// Close signals the cleanup goroutine to stop and waits for it to exit.
// It must be called once when the rate limiter is no longer needed.
func (s *MemoryStore) Close() error {
	close(s.stopChan)
	s.wg.Wait()
	// Clear all buckets to assist GC.
	s.buckets.Range(func(k, _ any) bool {
		s.buckets.Delete(k)
		return true
	})
	return nil
}

// HealthCheck returns an error if the number of active buckets appears
// excessive (>10 000), which could indicate a memory pressure issue.
func (s *MemoryStore) HealthCheck() error {
	count := 0
	s.buckets.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count > 10_000 {
		return fmt.Errorf("ratelimit: too many active buckets (%d)", count)
	}
	return nil
}

// max0 returns n if n >= 0, otherwise 0.
func max0(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
