package billing

import (
	"log/slog"

	"fusemomo-api/internal/models"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type BillingHandler struct {
	Service Service
	DB      DBPool
}

func NewBillingHandler(svc Service, db DBPool) *BillingHandler {
	return &BillingHandler{Service: svc, DB: db}
}

// CreateCheckoutHandler godoc
//
// @Summary      Create a Stripe Checkout Session
// @Description  Creates a Stripe-hosted Checkout Session for the authenticated tenant.
//
//	Validates the price_id against known plans. Returns a checkout_url to redirect the user.
//	The tenant's Stripe Customer is created automatically if it does not yet exist.
//
// @Tags         Billing
// @Security     SessionAuth
// @Accept       json
// @Produce      json
// @Param        request body  models.CheckoutRequest  true  "Price ID to subscribe to"
// @Success      200 {object} models.CheckoutResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/app/billing/checkout [post]
func (h *BillingHandler) CreateCheckoutHandler(c fiber.Ctx) error {
	tenantID, err := getTenantID(c)
	if err != nil {
		return err
	}

	var req models.CheckoutRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("invalid request body")
	}

	// Validate against the service's known price map to reject unknown IDs early.
	if !h.Service.IsValidPriceID(req.PriceID) {
		return utils.BadRequest("unrecognised price_id")
	}

	checkoutURL, err := h.Service.CreateCheckoutSession(c.Context(), tenantID, req.PriceID)
	if err != nil {
		slog.Error("billing: create checkout session failed", "tenant_id", tenantID, "err", err)
		return utils.InternalServerError("could not create checkout session")
	}

	return c.JSON(models.CheckoutResponse{CheckoutURL: checkoutURL})
}

// CreatePortalHandler godoc
// @Summary      Open the Stripe Customer Portal
// @Description  Creates a Stripe Customer Portal Session for the authenticated tenant.
//
//	Allows the tenant to manage their subscription, update payment methods, and view invoices.
//	Returns 400 if the tenant has no active billing account (subscribe first).
//
// @Tags         Billing
// @Security     SessionAuth
// @Produce      json
// @Success      200 {object} models.PortalResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/app/billing/portal [post]
func (h *BillingHandler) CreatePortalHandler(c fiber.Ctx) error {
	tenantID, err := getTenantID(c)
	if err != nil {
		return err
	}

	portalURL, err := h.Service.CreatePortalSession(c.Context(), tenantID)
	if err != nil {
		slog.Error("billing: create portal session failed", "tenant_id", tenantID, "err", err)
		// Surface a user-friendly error: the most common reason is no billing account yet.
		return utils.BadRequest("no billing account on this subscription — subscribe first.")
	}

	return c.JSON(models.PortalResponse{PortalURL: portalURL})
}

// GetBillingStatusHandler godoc
// @Summary      Get billing status
// @Description  Returns the tenant's current plan name, active subscription flag, and quota limits.
//
//	Used by the frontend payment success page to poll for webhook-driven plan activation.
//
// @Tags         Billing
// @Security     SessionAuth
// @Produce      json
// @Success      200 {object} models.BillingStatusResponse
// @Failure      401 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/app/billing/status [get]
func (h *BillingHandler) GetBillingStatusHandler(c fiber.Ctx) error {
	tenantID, err := getTenantID(c)
	if err != nil {
		return err
	}

	status, err := h.Service.GetStatus(c.Context(), tenantID)
	if err != nil {
		slog.Error("billing: get status failed", "tenant_id", tenantID, "err", err)
		return utils.InternalServerError("could not retrieve billing status")
	}

	return c.JSON(status)
}

// HandleStripeWebhook godoc
//
// @Summary      Stripe webhook receiver
// @Description  Receives and processes signed Stripe webhook events.
//
//	Authentication is via the Stripe-Signature header — no session cookie required.
//	Returns 400 on invalid signature, 500 on transient DB failure (triggers Stripe retry), 200 otherwise.
//	Handled events: checkout.session.completed, customer.subscription.updated,
//	customer.subscription.deleted, invoice.payment_failed.
//
// @Tags         Billing
// @Accept       json
// @Produce      plain
// @Success      200
// @Failure      400
// @Failure      500
// @Router       /v1/webhooks/stripe [post]
func (h *BillingHandler) HandleStripeWebhook(c fiber.Ctx) error {
	return StripeWebhookHandler(c, h.Service, h.DB)
}

// getTenantID extracts the tenant UUID set by the session middleware.
// Type-asserts directly to avoid fmt.Sprintf fragility.
func getTenantID(c fiber.Ctx) (uuid.UUID, error) {
	v := c.Locals("tenant_id")
	if v == nil {
		return uuid.Nil, utils.Unauthorized("not authenticated")
	}
	// Session middleware stores it as uuid.UUID directly.
	if id, ok := v.(uuid.UUID); ok {
		return id, nil
	}
	// Fallback: middleware may store it as a string (e.g. during testing).
	if s, ok := v.(string); ok {
		id, err := uuid.Parse(s)
		if err != nil {
			return uuid.Nil, utils.Unauthorized("invalid tenant id format")
		}
		return id, nil
	}
	return uuid.Nil, utils.Unauthorized("invalid tenant id type in context")
}
