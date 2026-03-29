package billing

import (
	"context"
	"fmt"
	"log/slog"

	"fusemomo-api/internal/models"
	"fusemomo-api/internal/utils"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v76"
	portalsession "github.com/stripe/stripe-go/v76/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
)

// Service defines all billing operations. It is an interface so tests can mock it.
type Service interface {
	CreateCheckoutSession(ctx context.Context, tenantID uuid.UUID, priceID string) (string, error)
	CreatePortalSession(ctx context.Context, tenantID uuid.UUID) (string, error)
	GetStatus(ctx context.Context, tenantID uuid.UUID) (*models.BillingStatusResponse, error)
	ApplyPlan(ctx context.Context, stripeCustomerID, stripeSubscriptionID string, planName PlanName) error
	ClearPlan(ctx context.Context, stripeCustomerID string) error
	// IsValidPriceID reports whether the given Stripe price ID is a known plan price.
	IsValidPriceID(priceID string) bool
}

type billingService struct {
	db          *pgxpool.Pool
	frontendURL string
	// priceIDToPlan is populated at construction time from env — NOT at package init.
	priceIDToPlan map[string]PlanName
}

// NewService constructs a billing service.
// stripe.Key must be set by the caller (e.g. in server init) before any methods are called.
func NewService(db *pgxpool.Pool) Service {
	monthlyPrice := utils.GetEnv("STRIPE_PRICE_BUILDER_MONTHLY", "")
	yearlyPrice := utils.GetEnv("STRIPE_PRICE_BUILDER_YEARLY", "")
	frontendURL := utils.GetEnv("FRONTEND_URL", "https://fusemomo.com")

	priceMap := make(map[string]PlanName, 2)
	if monthlyPrice != "" {
		priceMap[monthlyPrice] = PlanBuilder
	}
	if yearlyPrice != "" {
		priceMap[yearlyPrice] = PlanBuilder
	}

	return &billingService{
		db:            db,
		frontendURL:   frontendURL,
		priceIDToPlan: priceMap,
	}
}

func (s *billingService) IsValidPriceID(priceID string) bool {
	_, ok := s.priceIDToPlan[priceID]
	return ok
}

// GetStatus returns the current billing state for a tenant.
func (s *billingService) GetStatus(ctx context.Context, tenantID uuid.UUID) (*models.BillingStatusResponse, error) {
	var plan string
	var subID *string
	var resLimit, intLimit, apiLimit int

	const q = `
		SELECT plan, stripe_subscription_id,
		       monthly_resolution_limit, monthly_interaction_limit, connected_api_limit
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`
	err := s.db.QueryRow(ctx, q, tenantID.String()).Scan(&plan, &subID, &resLimit, &intLimit, &apiLimit)
	if err != nil {
		return nil, fmt.Errorf("query billing status: %w", err)
	}

	return &models.BillingStatusResponse{
		Plan:                    plan,
		HasActiveSubscription:   subID != nil && *subID != "",
		MonthlyResolutionLimit:  resLimit,
		MonthlyInteractionLimit: intLimit,
		ConnectedAPILimit:       apiLimit,
	}, nil
}

// CreateCheckoutSession creates or reuses a Stripe Customer for the tenant, then
// returns a Stripe-hosted Checkout URL. It uses a DB-level serialisation strategy
// to prevent TOCTOU races under concurrent requests.
func (s *billingService) CreateCheckoutSession(ctx context.Context, tenantID uuid.UUID, priceID string) (string, error) {
	customerID, err := s.ensureStripeCustomer(ctx, tenantID)
	if err != nil {
		return "", err
	}

	params := &stripe.CheckoutSessionParams{
		Params:   stripe.Params{Context: ctx},
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(s.frontendURL + "/payment/success?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:  stripe.String(s.frontendURL + "/upgrade"),
	}
	params.AddMetadata("tenant_id", tenantID.String())
	params.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{}
	params.SubscriptionData.AddMetadata("tenant_id", tenantID.String())

	sess, err := checkoutsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe checkout session: %w", err)
	}
	return sess.URL, nil
}

// ensureStripeCustomer returns the existing stripe_customer_id for the tenant, or
// creates a new Stripe Customer and atomically writes it back.
// Uses a single UPDATE ... WHERE stripe_customer_id IS NULL to prevent concurrent
// duplicate customer creation (TOCTOU race).
func (s *billingService) ensureStripeCustomer(ctx context.Context, tenantID uuid.UUID) (string, error) {
	// Fast path: customer already exists.
	var existingID *string
	var email string
	err := s.db.QueryRow(ctx,
		"SELECT email, stripe_customer_id FROM tenants WHERE id = $1 AND deleted_at IS NULL",
		tenantID.String(),
	).Scan(&email, &existingID)
	if err != nil {
		return "", fmt.Errorf("fetch tenant: %w", err)
	}
	if existingID != nil && *existingID != "" {
		return *existingID, nil
	}

	// Slow path: create customer in Stripe.
	params := &stripe.CustomerParams{
		Params: stripe.Params{Context: ctx},
		Email:  stripe.String(email),
	}
	params.AddMetadata("tenant_id", tenantID.String())

	c, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("create stripe customer: %w", err)
	}

	// Atomic write: only update if no other concurrent request already set it.
	// If a race occurred, we get back the winner's ID and discard ours (orphan in Stripe).
	var finalID string
	err = s.db.QueryRow(ctx, `
		UPDATE tenants
		SET    stripe_customer_id = COALESCE(stripe_customer_id, $1),
		       updated_at         = NOW()
		WHERE  id = $2
		RETURNING stripe_customer_id
	`, c.ID, tenantID.String()).Scan(&finalID)
	if err != nil {
		return "", fmt.Errorf("persist stripe customer id: %w", err)
	}
	return finalID, nil
}

// CreatePortalSession creates a Stripe Customer Portal URL for the tenant.
func (s *billingService) CreatePortalSession(ctx context.Context, tenantID uuid.UUID) (string, error) {
	var stripeCustomerID *string
	err := s.db.QueryRow(ctx,
		"SELECT stripe_customer_id FROM tenants WHERE id = $1 AND deleted_at IS NULL",
		tenantID.String(),
	).Scan(&stripeCustomerID)
	if err != nil {
		return "", fmt.Errorf("fetch tenant: %w", err)
	}
	if stripeCustomerID == nil || *stripeCustomerID == "" {
		return "", fmt.Errorf("tenant has no billing account")
	}

	params := &stripe.BillingPortalSessionParams{
		Params:    stripe.Params{Context: ctx},
		Customer:  stripe.String(*stripeCustomerID),
		ReturnURL: stripe.String(s.frontendURL + "/dashboard/settings"),
	}
	sess, err := portalsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe portal session: %w", err)
	}
	return sess.URL, nil
}

// ApplyPlan updates the tenant's plan, subscription ID, and usage limits in the DB.
// It is idempotent: a no-op if the subscription and plan are already current.
func (s *billingService) ApplyPlan(ctx context.Context, stripeCustomerID, stripeSubscriptionID string, planName PlanName) error {
	limits, ok := GetPlanLimits(planName)
	if !ok {
		return fmt.Errorf("unknown plan: %q", planName)
	}

	// Idempotency check: skip write if already up to date.
	var currentSub *string
	var currentPlan string
	err := s.db.QueryRow(ctx,
		"SELECT stripe_subscription_id, plan FROM tenants WHERE stripe_customer_id = $1",
		stripeCustomerID,
	).Scan(&currentSub, &currentPlan)
	if err != nil {
		return fmt.Errorf("query current plan: %w", err)
	}
	if currentSub != nil && *currentSub == stripeSubscriptionID && currentPlan == string(planName) {
		slog.Info("billing: ApplyPlan skipped (already current)",
			"customer_id", stripeCustomerID,
			"plan", planName,
		)
		return nil
	}

	const q = `
		UPDATE tenants SET
			plan                     = $1,
			stripe_subscription_id   = $2,
			monthly_resolution_limit  = $3,
			monthly_interaction_limit = $4,
			connected_api_limit       = $5,
			updated_at               = NOW()
		WHERE stripe_customer_id = $6
	`
	_, err = s.db.Exec(ctx, q,
		string(planName),
		stripeSubscriptionID,
		limits.MonthlyResolutionLimit,
		limits.MonthlyInteractionLimit,
		limits.ConnectedAPILimit,
		stripeCustomerID,
	)
	if err != nil {
		return fmt.Errorf("apply plan to db: %w", err)
	}

	slog.Info("billing: plan applied",
		"customer_id", stripeCustomerID,
		"subscription_id", stripeSubscriptionID,
		"plan", planName,
	)
	return nil
}

// ClearPlan resets the tenant back to the free plan on subscription cancellation.
func (s *billingService) ClearPlan(ctx context.Context, stripeCustomerID string) error {
	limits, _ := GetPlanLimits(PlanFree)

	const q = `
		UPDATE tenants SET
			plan                     = $1,
			stripe_subscription_id   = NULL,
			monthly_resolution_limit  = $2,
			monthly_interaction_limit = $3,
			connected_api_limit       = $4,
			updated_at               = NOW()
		WHERE stripe_customer_id = $5
	`
	_, err := s.db.Exec(ctx, q,
		string(PlanFree),
		limits.MonthlyResolutionLimit,
		limits.MonthlyInteractionLimit,
		limits.ConnectedAPILimit,
		stripeCustomerID,
	)
	if err != nil {
		return fmt.Errorf("clear plan in db: %w", err)
	}

	slog.Info("billing: plan cleared (downgraded to free)", "customer_id", stripeCustomerID)
	return nil
}
