package billing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fusemomo-api/internal/models"
	"fusemomo-api/internal/utils"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stripe/stripe-go/v76"
	portalsession "github.com/stripe/stripe-go/v76/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
)

// DBPool is the minimal pgx interface required by the billing package.
// *pgxpool.Pool satisfies this automatically — no adapter needed.
type DBPool interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SessionSyncer is implemented by *session.Store. Defined here as an interface
// so the billing package does not import the session package (avoiding any future
// circular dependency). When a billing webhook changes a tenant's plan, billing
// calls UpdatePlan so active in-memory sessions reflect the change immediately
// without requiring the user to re-login.
type SessionSyncer interface {
	UpdatePlan(tenantID, plan string)
}

// Service defines all billing operations. It is an interface so tests can mock it.
type Service interface {
	CreateCheckoutSession(ctx context.Context, tenantID uuid.UUID, priceID string) (string, error)
	CreatePortalSession(ctx context.Context, tenantID uuid.UUID) (string, error)
	GetStatus(ctx context.Context, tenantID uuid.UUID) (*models.BillingStatusResponse, error)
	ApplyPlan(ctx context.Context, stripeCustomerID, stripeSubscriptionID string, planName PlanName) error
	ClearPlan(ctx context.Context, stripeCustomerID string) error
	// ScheduleCancel records that a subscription will cancel at the given Unix timestamp.
	// Called when Stripe reports cancel_at_period_end=true on subscription.updated.
	ScheduleCancel(ctx context.Context, stripeCustomerID string, cancelAt int64) error
	// IsValidPriceID reports whether the given Stripe price ID is a known plan price.
	IsValidPriceID(priceID string) bool
	// PlanFromPriceID resolves a Stripe price ID to a plan name.
	// Returns (plan, true) on match, ("", false) when price is unknown.
	PlanFromPriceID(priceID string) (PlanName, bool)
	// PriceIDForPlan maps a semantic plan and interval to an active Stripe price ID.
	PriceIDForPlan(plan string, interval string) (string, error)
	// NotifyPaymentFailed is called when Stripe reports a failed invoice payment.
	// Writes payment_failed_at to the tenant row and logs a structured alert.
	NotifyPaymentFailed(ctx context.Context, stripeCustomerID string, attemptCount int64) error
}

type billingService struct {
	db            DBPool
	frontendURL   string
	// priceIDToPlan is populated at construction time from env — NOT at package init.
	priceIDToPlan map[string]PlanName
	// sessions is optional. When non-nil, plan changes are pushed to active sessions
	// immediately after the DB write so users see the new plan without re-logging in.
	sessions      SessionSyncer
}

// NewService constructs a billing service without session syncing.
// stripe.Key must be set by the caller (e.g. in server init) before any methods are called.
// Use NewServiceWithSessions in production so plan changes propagate to live sessions.
func NewService(db DBPool) Service {
	return newService(db, nil)
}

// NewServiceWithSessions constructs a billing service that propagates plan changes
// to the in-memory session store immediately after every DB write.
// This eliminates the "stale plan" window that would otherwise last until re-login.
func NewServiceWithSessions(db DBPool, sessions SessionSyncer) Service {
	return newService(db, sessions)
}

func newService(db DBPool, sessions SessionSyncer) Service {
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
		sessions:      sessions,
	}
}

func (s *billingService) IsValidPriceID(priceID string) bool {
	_, ok := s.priceIDToPlan[priceID]
	return ok
}

func (s *billingService) PlanFromPriceID(priceID string) (PlanName, bool) {
	name, ok := s.priceIDToPlan[priceID]
	return name, ok
}

func (s *billingService) NotifyPaymentFailed(ctx context.Context, stripeCustomerID string, attemptCount int64) error {
	var email, tenantID string
	if err := s.db.QueryRow(ctx,
		"SELECT id::text, email FROM tenants WHERE stripe_customer_id = $1 AND deleted_at IS NULL",
		stripeCustomerID,
	).Scan(&tenantID, &email); err != nil {
		return fmt.Errorf("notify payment failed: resolve tenant: %w", err)
	}

	// Write the failure timestamp so the frontend can show a banner.
	if _, err := s.db.Exec(ctx,
		"UPDATE tenants SET payment_failed_at = NOW(), updated_at = NOW() WHERE stripe_customer_id = $1",
		stripeCustomerID,
	); err != nil {
		// Non-fatal — log and continue. The warning below still fires.
		slog.Error("billing: failed to write payment_failed_at", "tenant_id", tenantID, "err", err)
	}

	slog.Warn("billing: payment failed — action required",
		"tenant_id", tenantID,
		"email", email,
		"attempt_count", attemptCount,
		"alert", "payment_failed_action_required",
		// Future: trigger transactional email (Resend, Postmark, etc.)
		// Future: write to notifications table for in-app banner
	)
	return nil
}

// ScheduleCancel records that a subscription is cancelling at period end.
// Sets subscription_cancel_at so the frontend can warn the user, and clears
// payment_failed_at (a cancelling subscription means billing is resolved).
func (s *billingService) ScheduleCancel(ctx context.Context, stripeCustomerID string, cancelAt int64) error {
	cancelTime := time.Unix(cancelAt, 0).UTC()
	_, err := s.db.Exec(ctx, `
		UPDATE tenants
		SET subscription_cancel_at = $1,
		    payment_failed_at      = NULL,
		    updated_at             = NOW()
		WHERE stripe_customer_id = $2
	`, cancelTime, stripeCustomerID)
	if err != nil {
		return fmt.Errorf("schedule cancel: %w", err)
	}
	slog.Info("billing: subscription cancellation scheduled",
		"customer_id", stripeCustomerID,
		"cancel_at", cancelTime,
	)
	return nil
}

// PriceIDForPlan resolves a string plan and interval into the exact Stripe Price ID configured.
func (s *billingService) PriceIDForPlan(plan string, interval string) (string, error) {
	if plan != string(PlanBuilder) {
		return "", fmt.Errorf("unsupported plan configured for self-serve checkout: %s", plan)
	}

	var priceID string
	if interval == "monthly" {
		priceID = utils.GetEnv("STRIPE_PRICE_BUILDER_MONTHLY", "")
	} else if interval == "yearly" {
		priceID = utils.GetEnv("STRIPE_PRICE_BUILDER_YEARLY", "")
	} else {
		return "", fmt.Errorf("invalid billing interval: %s", interval)
	}

	if priceID == "" {
		return "", fmt.Errorf("pricing is not configured on the server")
	}

	return priceID, nil
}

// GetStatus returns the current billing state for a tenant.
func (s *billingService) GetStatus(ctx context.Context, tenantID uuid.UUID) (*models.BillingStatusResponse, error) {
	var plan string
	var subID *string
	var resLimit, intLimit, apiLimit int
	var cancelAt *time.Time
	var paymentFailedAt *time.Time

	const q = `
		SELECT plan, stripe_subscription_id,
		       monthly_resolution_limit, monthly_interaction_limit, connected_api_limit,
		       subscription_cancel_at, payment_failed_at
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`
	err := s.db.QueryRow(ctx, q, tenantID.String()).Scan(
		&plan, &subID, &resLimit, &intLimit, &apiLimit,
		&cancelAt, &paymentFailedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("query billing status: %w", err)
	}

	return &models.BillingStatusResponse{
		Plan:                    plan,
		HasActiveSubscription:   subID != nil && *subID != "",
		MonthlyResolutionLimit:  resLimit,
		MonthlyInteractionLimit: intLimit,
		ConnectedAPILimit:       apiLimit,
		CancelAt:                cancelAt,
		PaymentFailed:           paymentFailedAt != nil,
	}, nil
}

// CreateCheckoutSession creates or reuses a Stripe Customer for the tenant, then
// returns a Stripe-hosted Checkout URL. Uses a transaction-scoped advisory lock
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
		// Recovery mechanism: if the test customer was hard-deleted in the Stripe dashboard,
		// Stripe returns a 'resource_missing' error for the 'customer' param.
		// We wipe our local reference and retry to automatically self-heal.
		if stripeErr, ok := err.(*stripe.Error); ok && string(stripeErr.Code) == "resource_missing" && stripeErr.Param == "customer" {
			slog.Warn("billing: stripe customer missing from Stripe, clearing local db and retrying", "tenant_id", tenantID)

			_, dbErr := s.db.Exec(ctx, "UPDATE tenants SET stripe_customer_id = NULL WHERE id = $1", tenantID.String())
			if dbErr != nil {
				return "", fmt.Errorf("stripe checkout session recovery failed: %w", dbErr)
			}

			// Retry exactly once (ensureStripeCustomer will now generate a fresh customer).
			return s.CreateCheckoutSession(ctx, tenantID, priceID)
		}

		return "", fmt.Errorf("stripe checkout session: %w", err)
	}
	return sess.URL, nil
}

// ensureStripeCustomer returns the existing stripe_customer_id for the tenant,
// or creates a new Stripe Customer inside a transaction with a
// transaction-scoped advisory lock to prevent concurrent duplicate creation.
// pg_advisory_xact_lock is safe with pgxpool: the lock is tied to the
// transaction lifetime, not the connection.
func (s *billingService) ensureStripeCustomer(ctx context.Context, tenantID uuid.UUID) (string, error) {
	// Fast path — no lock needed if customer already exists.
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

	// Slow path — begin a transaction and acquire a transaction-scoped
	// advisory lock keyed on the tenant UUID.
	// pg_advisory_xact_lock BLOCKS until the lock is available (no busy-spin,
	// no sleep) and releases automatically when the transaction ends —
	// safe with pgxpool regardless of connection assignment.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx for customer creation: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	lockKey := uuidToLockKey(tenantID)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return "", fmt.Errorf("acquire xact advisory lock: %w", err)
	}

	// Re-read inside the transaction — another request may have created
	// and written the customer between the fast-path read and lock acquisition.
	if err := tx.QueryRow(ctx,
		"SELECT stripe_customer_id FROM tenants WHERE id = $1",
		tenantID.String(),
	).Scan(&existingID); err != nil {
		return "", fmt.Errorf("re-fetch under lock: %w", err)
	}
	if existingID != nil && *existingID != "" {
		return *existingID, nil // another request won the race — use their customer
	}

	// Create the Stripe customer — exactly once, guaranteed by the lock.
	params := &stripe.CustomerParams{
		Params: stripe.Params{Context: ctx},
		Email:  stripe.String(email),
	}
	params.AddMetadata("tenant_id", tenantID.String())

	c, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("create stripe customer: %w", err)
	}

	// Write — no COALESCE needed, we hold the lock exclusively.
	var finalID string
	if err = tx.QueryRow(ctx, `
		UPDATE tenants
		SET    stripe_customer_id = $1,
		       updated_at         = NOW()
		WHERE  id = $2
		RETURNING stripe_customer_id
	`, c.ID, tenantID.String()).Scan(&finalID); err != nil {
		return "", fmt.Errorf("persist stripe customer id: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit customer creation: %w", err)
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
// Uses a single atomic conditional UPDATE with IS DISTINCT FROM to prevent
// concurrent retries from executing redundant writes.
// Also clears subscription_cancel_at (reactivation) and payment_failed_at (payment recovered).
func (s *billingService) ApplyPlan(ctx context.Context, stripeCustomerID, stripeSubscriptionID string, planName PlanName) error {
	limits, ok := GetPlanLimits(planName)
	if !ok {
		return fmt.Errorf("unknown plan: %q", planName)
	}

	const q = `
		UPDATE tenants SET
			plan                      = $1,
			stripe_subscription_id    = $2,
			monthly_resolution_limit  = $3,
			monthly_interaction_limit = $4,
			connected_api_limit       = $5,
			subscription_cancel_at    = NULL,
			payment_failed_at         = NULL,
			updated_at                = NOW()
		WHERE stripe_customer_id = $6
		  AND (
		        stripe_subscription_id IS DISTINCT FROM $2
		        OR plan::text IS DISTINCT FROM $1
		      )
		RETURNING id
	`
	var returnedID *string
	err := s.db.QueryRow(ctx, q,
		string(planName),
		stripeSubscriptionID,
		limits.MonthlyResolutionLimit,
		limits.MonthlyInteractionLimit,
		limits.ConnectedAPILimit,
		stripeCustomerID,
	).Scan(&returnedID)

	if err != nil && !isNoRows(err) {
		return fmt.Errorf("apply plan to db: %w", err)
	}
	if returnedID == nil {
		// IS DISTINCT FROM conditions were false — already up to date, skip.
		slog.Info("billing: ApplyPlan skipped (already current)",
			"customer_id", stripeCustomerID,
			"plan", planName,
		)
		return nil
	}

	// Propagate plan change to any live in-memory sessions owned by this tenant.
	// returnedID already holds the tenant UUID from the RETURNING clause — no extra DB call.
	if s.sessions != nil && returnedID != nil {
		s.sessions.UpdatePlan(*returnedID, string(planName))
	}

	slog.Info("billing: plan applied",
		"customer_id", stripeCustomerID,
		"subscription_id", stripeSubscriptionID,
		"plan", planName,
		"tenant_id", func() string {
			if returnedID != nil { return *returnedID }
			return ""
		}(),
	)
	return nil
}

// ClearPlan resets the tenant back to the free plan on subscription cancellation.
// Clears subscription_cancel_at and payment_failed_at as all billing state is now resolved.
func (s *billingService) ClearPlan(ctx context.Context, stripeCustomerID string) error {
	limits, _ := GetPlanLimits(PlanFree)

	const q = `
		UPDATE tenants SET
			plan                      = $1,
			stripe_subscription_id    = NULL,
			subscription_cancel_at    = NULL,
			payment_failed_at         = NULL,
			monthly_resolution_limit  = $2,
			monthly_interaction_limit = $3,
			connected_api_limit       = $4,
			updated_at                = NOW()
		WHERE stripe_customer_id = $5
		RETURNING id::text
	`
	var tenantID string
	err := s.db.QueryRow(ctx, q,
		string(PlanFree),
		limits.MonthlyResolutionLimit,
		limits.MonthlyInteractionLimit,
		limits.ConnectedAPILimit,
		stripeCustomerID,
	).Scan(&tenantID)
	if err != nil && !isNoRows(err) {
		return fmt.Errorf("clear plan in db: %w", err)
	}

	// Propagate downgrade to any live in-memory sessions immediately.
	if s.sessions != nil && tenantID != "" {
		s.sessions.UpdatePlan(tenantID, string(PlanFree))
	}

	slog.Info("billing: plan cleared (downgraded to free)",
		"customer_id", stripeCustomerID,
		"tenant_id", tenantID,
	)
	return nil
}

// isNoRows returns true for pgx "no rows in result set" errors.
// Used to distinguish "nothing matched the WHERE clause" from real DB errors.
func isNoRows(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no rows")
}

// uuidToLockKey converts a UUID to an int64 for PostgreSQL advisory locks.
// Uses the first 8 bytes of the UUID (big-endian). Consistent across calls
// for the same UUID — safe as a deterministic lock key.
func uuidToLockKey(id uuid.UUID) int64 {
	b := id[:]
	return int64(b[0])<<56 | int64(b[1])<<48 | int64(b[2])<<40 | int64(b[3])<<32 |
		int64(b[4])<<24 | int64(b[5])<<16 | int64(b[6])<<8 | int64(b[7])
}
