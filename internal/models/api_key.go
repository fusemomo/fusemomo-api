package models

import (
	"time"

	"github.com/google/uuid"
)

type APIKeyStatus string

const (
	APIKeyStatusActive  APIKeyStatus = "active"
	APIKeyStatusRevoked APIKeyStatus = "revoked"
	APIKeyStatusExpired APIKeyStatus = "expired"
)

type APIKey struct {
	ID         uuid.UUID    `json:"id" db:"id"`
	TenantID   uuid.UUID    `json:"tenant_id" db:"tenant_id"`
	Name       string       `json:"name" db:"name"`
	KeyPrefix  string       `json:"key_prefix" db:"key_prefix"`
	KeyHash    string       `json:"key_hash" db:"key_hash"`
	Status     APIKeyStatus `json:"status" db:"status"`
	ExpiresAt  *time.Time   `json:"expires_at,omitempty" db:"expires_at"`
	LastUsedAt *time.Time   `json:"last_used_at,omitempty" db:"last_used_at"`
	LastUsedIP *string      `json:"last_used_ip,omitempty" db:"last_used_ip"`
	CreatedAt  time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at" db:"updated_at"`
}
