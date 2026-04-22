package utils

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// Context key constants — single source of truth for all auth middlewares and handlers.
// sets exactly these keys; every handler reads exactly these keys.
const (
	CtxKeyTenantID   = "tenant_id"
	CtxKeyAuthUserID = "auth_user_id"
	CtxKeyRole       = "role"
	CtxKeyPlan       = "plan"
	CtxKeyAPIKeyID   = "api_key_id"
)

// Plan name constants — match the tenants.plan DB enum exactly.
// Use these everywhere instead of bare string literals.
const (
	PlanFree       = "free"
	PlanBuilder    = "builder"
	PlanEnterprise = "enterprise"
)

// TenantIDFromCtx extracts and parses the tenant_id UUID from Fiber's request context.
// Returns Unauthorized when the key is missing or the value is not a valid UUID.
// Replaces the repeated uuid.Parse(fmt.Sprintf("%v", c.Locals("tenant_id"))) pattern.
func TenantIDFromCtx(c fiber.Ctx) (uuid.UUID, error) {
	v := c.Locals(CtxKeyTenantID)
	if v == nil {
		return uuid.Nil, Unauthorized("Missing tenant context")
	}
	id, err := uuid.Parse(fmt.Sprintf("%v", v))
	if err != nil {
		return uuid.Nil, Unauthorized("Invalid tenant ID format")
	}
	return id, nil
}

// PlanFromCtx reads the current tenant plan from the request context.
// All three auth middlewares (session, JWT, API key) guarantee this is set.
// Falls back to PlanFree when absent — safe because free is the most restrictive tier.
func PlanFromCtx(c fiber.Ctx) string {
	v := c.Locals(CtxKeyPlan)
	if v == nil {
		return PlanFree
	}
	return fmt.Sprintf("%v", v)
}

// RoleFromCtx reads the current tenant role from the request context.
func RoleFromCtx(c fiber.Ctx) string {
	v := c.Locals(CtxKeyRole)
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// PlanAtLeast reports whether `have` meets or exceeds the `need` tier.
// Tier order: free(1) < builder(2) < enterprise(3).
// Unknown plans rank 0 — below free — so unrecognised values are always denied.
func PlanAtLeast(have, need string) bool {
	return planRank(have) >= planRank(need)
}

// RequirePlan writes a 402 PaymentRequired response and returns true when the
// tenant's plan is below `need`. Handlers should return nil immediately when
// true is returned — the response has already been written.
//
// Usage:
//
//	if utils.RequirePlan(c, utils.PlanBuilder) {
//	    return nil
//	}
func RequirePlan(c fiber.Ctx, need string) bool {
	have := PlanFromCtx(c)
	if PlanAtLeast(have, need) {
		return false // plan is sufficient — caller continues normally
	}
	_ = c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
		"error":         "plan_upgrade_required",
		"message":       fmt.Sprintf("This feature requires the %s plan or above.", need),
		"current_plan":  have,
		"required_plan": need,
		"upgrade_url":   "https://fusemomo.com/upgrade",
	})
	return true // response sent — caller returns nil
}

// planRank converts a plan name to an integer so PlanAtLeast can do a single comparison.
func planRank(plan string) int {
	switch plan {
	case PlanEnterprise:
		return 3
	case PlanBuilder:
		return 2
	case PlanFree:
		return 1
	default:
		return 0 // unknown plan — deny everything
	}
}
