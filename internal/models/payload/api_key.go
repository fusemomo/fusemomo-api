package payload

import "time"

type CreateAPIKeyRequest struct {
	Name      string `json:"name" validate:"required,min=3,max=255"`
	ExpiresIn string `json:"expires_in" validate:"required,oneof='30' '90' 'forever'"`
}

type CreateAPIKeyResponse struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"` // Keep secret, full key
	KeyPrefix string     `json:"key_prefix"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type DeleteAPIKeyRequest struct {
	ID string `json:"id" validate:"required,uuid"`
}

type APIKeyInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"key_hash"`
	Status     string     `json:"status"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}
