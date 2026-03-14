package ratelimit

import (
	"log"
	"time"
)

// startCleanupRoutine launches a background goroutine that periodically
// evicts expired request records and removes empty buckets. It increments
// the WaitGroup so Close() can wait for a clean exit.
func (s *MemoryStore) startCleanupRoutine() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ticker := time.NewTicker(s.config.CleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.cleanup()
			case <-s.stopChan:
				return
			}
		}
	}()
}

// cleanup iterates every bucket, evicts requests outside the current window,
// and deletes buckets that have been idle for more than 5 minutes.
//
// Locking strategy:
//   - Each bucket is locked individually (fine-grained).
//   - Buckets are deleted after the Range loop to avoid holding the map lock
//     while mutating it.
func (s *MemoryStore) cleanup() {
	now := time.Now().UTC()
	windowStart := now.Add(-s.config.Window)
	idleThreshold := now.Add(-5 * time.Minute)

	var keysToDelete []any

	s.buckets.Range(func(key, value any) bool {
		bucket := value.(*rateLimitBucket)

		bucket.mu.Lock()

		valid := make([]requestRecord, 0, len(bucket.Requests))
		var lastSeen time.Time
		for _, r := range bucket.Requests {
			if r.Timestamp.After(windowStart) {
				valid = append(valid, r)
				if r.Timestamp.After(lastSeen) {
					lastSeen = r.Timestamp
				}
			}
		}
		bucket.Requests = valid

		// Schedule deletion only for buckets that have been empty for >5 min.
		if len(bucket.Requests) == 0 && lastSeen.Before(idleThreshold) {
			keysToDelete = append(keysToDelete, key)
		}

		bucket.mu.Unlock()
		return true // continue iteration
	})

	for _, key := range keysToDelete {
		s.buckets.Delete(key)
	}

	if len(keysToDelete) > 0 {
		log.Printf("INFO: rate-limiter cleanup: removed %d idle buckets", len(keysToDelete))
	}
}
