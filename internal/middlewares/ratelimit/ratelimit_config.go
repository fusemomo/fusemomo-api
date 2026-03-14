// Package ratelimit provides in-memory sliding-window rate limiting middleware
// for FuseMomo API. It supports tier-based limits (Free, Builder, Enterprise)
// and automatic cleanup of stale data without requiring Redis.
//
// Example usage:
//
//	rl := ratelimit.NewRateLimiter(ratelimit.DefaultConfig())
//	defer rl.Close()
//	app.Use(rl.Middleware())
//
// Thread Safety: All public methods are thread-safe.
//
// Memory Management: A background goroutine cleans up expired entries every
// CleanupInterval. Call Close() to stop it gracefully.
package ratelimit

import (
	"strings"
	"time"
)

// RateLimitTier identifies the billing tier of a tenant.
type RateLimitTier string

const (
	TierFree       RateLimitTier = "free"
	TierBuilder    RateLimitTier = "builder"
	TierEnterprise RateLimitTier = "enterprise"
)

// TierLimit defines rate limit parameters for a single billing tier.
type TierLimit struct {
	// RequestsPerWindow is the base request allowance within the window.
	RequestsPerWindow int
	// BurstAllowance is extra requests allowed on top of the base limit.
	BurstAllowance int
	// Unlimited skips rate limiting entirely when true.
	Unlimited bool
}

// GetMaxRequests returns the total effective limit (base + burst).
func (t TierLimit) GetMaxRequests() int {
	return t.RequestsPerWindow + t.BurstAllowance
}

// GetBaseLimit returns the base request allowance without burst.
func (t TierLimit) GetBaseLimit() int {
	return t.RequestsPerWindow
}

// RateLimitConfig holds all configuration for the rate limiter.
type RateLimitConfig struct {
	// Enabled is a global on/off switch. When false, all requests pass through.
	Enabled bool
	// Window is the sliding time window for counting requests.
	Window time.Duration
	// TierLimits maps each billing tier to its request allowance.
	TierLimits map[RateLimitTier]TierLimit
	// AdminBypass skips rate limiting for tenants with role "admin".
	AdminBypass bool
	// ExcludedPaths lists URL paths that bypass rate limiting.
	ExcludedPaths []string
	// HeaderPrefix is prepended to all HTTP rate limit response headers.
	HeaderPrefix string
	// CleanupInterval controls how often the cleanup goroutine runs.
	CleanupInterval time.Duration
}

// DefaultConfig returns a production-ready RateLimitConfig.
//
// Tier limits:
//   - Free:       100 req/min + 20 burst = 120 total
//   - Builder:    1000 req/min + 200 burst = 1200 total
//   - Enterprise: Unlimited
func DefaultConfig() *RateLimitConfig {
	return &RateLimitConfig{
		Enabled: true,
		Window:  1 * time.Minute,
		TierLimits: map[RateLimitTier]TierLimit{
			TierFree: {
				RequestsPerWindow: 100,
				BurstAllowance:    20,
			},
			TierBuilder: {
				RequestsPerWindow: 1000,
				BurstAllowance:    200,
			},
			TierEnterprise: {
				Unlimited: true,
			},
		},
		AdminBypass:     true,
		ExcludedPaths:   []string{"/ping", "/health", "/v1/health", "/metrics"},
		HeaderPrefix:    "X-RateLimit-",
		CleanupInterval: 1 * time.Minute,
	}
}

// GetLimit returns the TierLimit for the given tier and whether it was found.
// Falls back to the Free tier if the tier is unknown.
func (c *RateLimitConfig) GetLimit(tier RateLimitTier) (TierLimit, bool) {
	limit, ok := c.TierLimits[tier]
	return limit, ok
}

// IsPathExcluded reports whether the given path should bypass rate limiting.
// Matching is prefix-based and case-insensitive.
func (c *RateLimitConfig) IsPathExcluded(path string) bool {
	lPath := strings.ToLower(path)
	for _, excluded := range c.ExcludedPaths {
		if strings.HasPrefix(lPath, strings.ToLower(excluded)) {
			return true
		}
	}
	return false
}
