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
