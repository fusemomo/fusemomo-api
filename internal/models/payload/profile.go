package payload

import "time"

type UpdateProfileRequest struct {
	Name      *string `json:"name" validate:"omitempty,min=1,max=255"`
	AvatarURL *string `json:"avatar_url" validate:"omitempty,url"`
}

type ProfileResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	AvatarURL *string   `json:"avatar_url"`
	Plan      string    `json:"plan"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
