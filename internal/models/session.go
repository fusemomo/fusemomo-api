package models

import "time"

// Session holds the identity context resolved from a valid Supabase JWT.
type Session struct {
	ID         string
	TenantID   string
	AuthUserID string
	Role       string
	Plan       string
	ExpiresAt  time.Time
}
