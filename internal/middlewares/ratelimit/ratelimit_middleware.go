package ratelimit

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
)

// RateLimiter is the top-level middleware that combines a MemoryStore with
// tier-based configuration to enforce per-tenant request limits.
type RateLimiter struct {
	config *RateLimitConfig
	store  *MemoryStore
}

// TenantInfo holds the rate-limit-relevant fields extracted from the
// request context set by the auth middleware.
type TenantInfo struct {
	TenantID string
	Tier     RateLimitTier
	IsAdmin  bool
}

// NewRateLimiter creates a RateLimiter with the given config.
// If config is nil, DefaultConfig() is used.
func NewRateLimiter(config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = DefaultConfig()
	}
	return &RateLimiter{
		config: config,
		store:  NewMemoryStore(config),
	}
}

// Middleware returns a Fiber handler that enforces rate limits.
//
// Request flow:
//  1. Skip if rate limiting is disabled.
//  2. Skip excluded paths (/health, /metrics, …).
//  3. Extract tenant from c.Locals — skip if none (unauthenticated, handled elsewhere).
//  4. Admin bypass — set "unlimited" headers and pass.
//  5. Unlimited tier (Enterprise) — set "unlimited" headers and pass.
//  6. Sliding-window check via MemoryStore.
//  7. Set X-RateLimit-* headers.
//  8. Allow or return 429.
func (rl *RateLimiter) Middleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		// 1. Global off switch.
		if !rl.config.Enabled {
			return c.Next()
		}

		// 2. Excluded paths.
		if rl.config.IsPathExcluded(c.Path()) {
			return c.Next()
		}

		// 3. Extract tenant info (reads locals set by auth middleware).
		tenant, err := rl.extractTenantInfo(c)
		if err != nil {
			// No tenant in context — let the auth middleware return 401.
			return c.Next()
		}

		// 4. Admin bypass.
		if rl.config.AdminBypass && tenant.IsAdmin {
			c.Set(rl.config.HeaderPrefix+"Limit", "unlimited")
			c.Set(rl.config.HeaderPrefix+"Remaining", "unlimited")
			return c.Next()
		}

		// 5. Look up tier limits — fall back to Free on unknown tier.
		tierLimit, exists := rl.config.GetLimit(tenant.Tier)
		if !exists {
			log.Printf("WARN: ratelimit: unknown tier %q for tenant %s — using Free tier",
				tenant.Tier, tenant.TenantID)
			tierLimit = rl.config.TierLimits[TierFree]
		}

		// 6. Unlimited tier (Enterprise).
		if tierLimit.Unlimited {
			c.Set(rl.config.HeaderPrefix+"Limit", "unlimited")
			c.Set(rl.config.HeaderPrefix+"Remaining", "unlimited")
			return c.Next()
		}

		// 7. Build the bucket key and run the sliding-window check.
		key := fmt.Sprintf("%s:%s", tenant.Tier, tenant.TenantID)
		result, err := rl.store.CheckRateLimit(key, tierLimit.GetMaxRequests(), rl.config.Window)
		if err != nil {
			// Should never happen in the in-memory implementation — fail open.
			log.Printf("ERROR: ratelimit: CheckRateLimit error for tenant %s: %v", tenant.TenantID, err)
			return c.Next()
		}

		// 8. Set standard rate limit headers.
		rl.setRateLimitHeaders(c, result, tierLimit)

		// 9. Enforce or allow.
		if !result.Allowed {
			log.Printf("RATE_LIMIT: tenant %s (%s) exceeded limit: %d/%d",
				tenant.TenantID, tenant.Tier, result.CurrentCount, result.Limit)
			return rl.handleRateLimitExceeded(c, result, tenant)
		}

		// Warn when approaching limit (>80% consumed).
		if result.Remaining < result.Limit/5 {
			log.Printf("WARN: ratelimit: tenant %s approaching limit: %d remaining of %d",
				tenant.TenantID, result.Remaining, result.Limit)
		}

		return c.Next()
	}
}

// extractTenantInfo reads the context locals set by the auth middlewares.
// The FuseMomo auth middleware sets:
//   - "tenant_id" (string)
//   - "plan"      (string: "free" | "builder" | "enterprise")
//   - "role"      (string: "admin" | "user")
//
// Returns an error when tenant_id is absent (unauthenticated request).
func (rl *RateLimiter) extractTenantInfo(c fiber.Ctx) (*TenantInfo, error) {
	tenantID, ok := c.Locals("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("ratelimit: no tenant_id in context")
	}

	// "plan" local — default to Free when missing.
	plan, _ := c.Locals("plan").(string)
	tier := RateLimitTier(plan)
	if tier == "" {
		tier = TierFree
	}

	// "role" local — map "admin" to the IsAdmin flag.
	role, _ := c.Locals("role").(string)
	isAdmin := role == "admin"

	return &TenantInfo{
		TenantID: tenantID,
		Tier:     tier,
		IsAdmin:  isAdmin,
	}, nil
}

// setRateLimitHeaders writes standard rate limit response headers.
// Additional burst headers are included when BurstAllowance > 0.
func (rl *RateLimiter) setRateLimitHeaders(c fiber.Ctx, result *RateLimitResult, tierLimit TierLimit) {
	pfx := rl.config.HeaderPrefix
	c.Set(pfx+"Limit", strconv.Itoa(result.Limit))
	c.Set(pfx+"Remaining", strconv.Itoa(result.Remaining))
	c.Set(pfx+"Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	c.Set(pfx+"Window", rl.config.Window.String())

	if tierLimit.BurstAllowance > 0 {
		c.Set(pfx+"Limit-Base", strconv.Itoa(tierLimit.GetBaseLimit()))
		c.Set(pfx+"Limit-Burst", strconv.Itoa(tierLimit.BurstAllowance))
	}
}

// handleRateLimitExceeded writes a 429 JSON response with actionable details.
// Free-tier tenants also receive an upgrade prompt.
func (rl *RateLimiter) handleRateLimitExceeded(c fiber.Ctx, result *RateLimitResult, tenant *TenantInfo) error {
	c.Set("Retry-After", strconv.FormatInt(result.RetryAfter, 10))

	msg := fmt.Sprintf(
		"Rate limit exceeded. You have made %d requests in the last %s. "+
			"Limit for %s tier is %d requests per %s. Please retry after %s.",
		result.CurrentCount,
		formatDuration(rl.config.Window),
		tenant.Tier,
		result.Limit,
		formatDuration(rl.config.Window),
		formatDuration(time.Duration(result.RetryAfter)*time.Second),
	)

	details := fiber.Map{
		"limit":       result.Limit,
		"current":     result.CurrentCount,
		"remaining":   result.Remaining,
		"reset_at":    result.ResetAt.UTC().Format(time.RFC3339),
		"retry_after": result.RetryAfter,
		"tier":        string(tenant.Tier),
		"window":      rl.config.Window.String(),
	}

	body := fiber.Map{
		"error": fiber.Map{
			"code":    "RATE_LIMIT_EXCEEDED",
			"message": msg,
			"details": details,
		},
	}

	// Provide upgrade hint to Free-tier tenants.
	if tenant.Tier == TierFree {
		body["error"].(fiber.Map)["upgrade_url"] = "/pricing"
		body["error"].(fiber.Map)["upgrade_message"] =
			"Consider upgrading to Builder tier for higher limits (1,000 requests/minute)."
	}

	return c.Status(fiber.StatusTooManyRequests).JSON(body)
}

// ResetTenantLimit clears the rate limit bucket for the given tenant and tier.
// Useful for admin operations or testing.
func (rl *RateLimiter) ResetTenantLimit(tenantID string, tier RateLimitTier) error {
	key := fmt.Sprintf("%s:%s", tier, tenantID)
	return rl.store.ResetLimit(key)
}

// GetTenantUsage returns the number of requests the tenant has made within
// the configured window.
func (rl *RateLimiter) GetTenantUsage(tenantID string, tier RateLimitTier) (int, error) {
	key := fmt.Sprintf("%s:%s", tier, tenantID)
	return rl.store.GetCurrentCount(key, rl.config.Window)
}

// Close shuts down the background cleanup goroutine gracefully.
// It must be called once, typically via defer.
func (rl *RateLimiter) Close() error {
	return rl.store.Close()
}

// formatDuration converts a time.Duration to a human-readable string.
//
//	< 1s  → "less than a second"
//	< 1m  → "X seconds"
//	< 1h  → "X minutes and Y seconds"
//	>= 1h → "X hours and Y minutes"
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "less than a second"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		if secs == 0 {
			return fmt.Sprintf("%d minutes", mins)
		}
		return fmt.Sprintf("%d minutes and %d seconds", mins, secs)
	default:
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins == 0 {
			return fmt.Sprintf("%d hours", hours)
		}
		return fmt.Sprintf("%d hours and %d minutes", hours, mins)
	}
}
