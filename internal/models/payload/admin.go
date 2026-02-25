package payload

import "time"

type AdminTenantUsage struct {
	Resolutions  int `json:"resolutions"`
	Interactions int `json:"interactions"`
}

type AdminTenantInfo struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Email     string           `json:"email"`
	Plan      string           `json:"plan"`
	CreatedAt time.Time        `json:"created_at"`
	Usage     AdminTenantUsage `json:"usage"`
}

type AdminTenantsResponse struct {
	Tenants []AdminTenantInfo `json:"tenants"`
	Total   int               `json:"total"`
}

type UpdateTenantPlanRequest struct {
	Plan                   string `json:"plan" validate:"required,oneof=free builder enterprise"`
	MonthlyResolutionLimit int    `json:"monthly_resolution_limit" validate:"required"`
}

type GlobalUsageResponse struct {
	TotalTenants              int            `json:"total_tenants"`
	TotalEntities             int            `json:"total_entities"`
	TotalInteractions         int            `json:"total_interactions"`
	TotalResolutionsThisMonth int            `json:"total_resolutions_this_month"`
	ByPlan                    map[string]int `json:"by_plan"`
}
