package models

// CheckoutRequest is sent by the frontend when the user clicks Subscribe.
type CheckoutRequest struct {
	Plan     string `json:"plan" validate:"required"`
	Interval string `json:"interval" validate:"required,oneof=monthly yearly"`
}

// CheckoutResponse is returned to the frontend with the Stripe-hosted checkout URL.
type CheckoutResponse struct {
	CheckoutURL string `json:"checkout_url"`
}

// PortalResponse is returned to the frontend with the Stripe Customer Portal URL.
type PortalResponse struct {
	PortalURL string `json:"portal_url"`
}

// BillingStatusResponse represents the current billing state for a tenant.
type BillingStatusResponse struct {
	Plan                    string `json:"plan"`
	HasActiveSubscription   bool   `json:"has_active_subscription"`
	MonthlyResolutionLimit  int    `json:"monthly_resolution_limit"`
	MonthlyInteractionLimit int    `json:"monthly_interaction_limit"`
	ConnectedAPILimit       int    `json:"connected_api_limit"`
}
