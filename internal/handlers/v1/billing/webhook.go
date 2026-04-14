package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
)

// webhookHandlerTimeout is set to 20s — gives the DB breathing room under
// load while leaving Stripe 10s to mark the delivery failed and retry.
// Stripe's total delivery timeout is 30s.
const webhookHandlerTimeout = 20 * time.Second

// StripeWebhookHandler verifies the Stripe signature, deduplicates the event,
// then dispatches to the appropriate handler.
//
// Return codes:
//
//	400 — invalid signature (do not retry)
//	500 — transient DB failure (Stripe will retry)
//	200 — success or intentionally ignored event
func StripeWebhookHandler(c fiber.Ctx, svc Service, db DBPool) error {
	payload := c.Request().Body()
	signatureHeader := c.Get("Stripe-Signature")
	webhookSecret := utils.GetEnv("STRIPE_WEBHOOK_SECRET", "")

	event, err := webhook.ConstructEventWithOptions(payload, signatureHeader, webhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		slog.Warn("billing webhook: signature verification failed", "err", err)
		return c.Status(http.StatusBadRequest).SendString("invalid stripe signature")
	}

	// Hard timeout — prevents slow DB from holding the Stripe connection open
	// indefinitely, which would cause Stripe to timeout and retry while the
	// first handler is still running.
	ctx, cancel := context.WithTimeout(context.Background(), webhookHandlerTimeout)
	defer cancel()

	start := time.Now()

	// 1. Extract internal Customer ID from the event payload.
	customerID := extractCustomerIDFromEvent(event)
	// If it's an unhandled event, just acknowledge it.
	if customerID == "" {
		return c.SendStatus(http.StatusOK)
	}

	// 2. Resolve the matching tenant_id from our database.
	var tenantID string
	err = db.QueryRow(ctx, "SELECT id FROM tenants WHERE stripe_customer_id = $1", customerID).Scan(&tenantID)
	if err != nil {
		// If no tenant exists for this customer in our DB, we cannot process this.
		// Return 200 so Stripe stops retrying an impossible event.
		if err.Error() == "no rows in result set" || err.Error() == "pgx: no rows in result set" {
			slog.Warn("billing webhook: no tenant found for customer",
				"event_id", event.ID,
				"customer_id", customerID,
			)
			return c.SendStatus(http.StatusOK)
		}
		slog.Error("billing webhook: failed to query tenant by customer", "err", err)
		return c.Status(http.StatusInternalServerError).SendString("db error looking up tenant")
	}

	// 3. Idempotency: claim this event before processing, now with a known tenant.
	claimed, err := claimWebhookEvent(ctx, db, event.ID, string(event.Type), tenantID)
	if err != nil {
		slog.Error("billing webhook: idempotency check failed",
			"event_id", event.ID,
			"err", err,
		)
		return c.Status(http.StatusInternalServerError).SendString("idempotency check failed")
	}
	if !claimed {
		slog.Info("billing webhook: duplicate event ignored",
			"event_id", event.ID,
			"type", event.Type,
		)
		return c.SendStatus(http.StatusOK)
	}

	handlerErr := handleWebhookEvent(ctx, event, svc)

	// Release the claim on handler error OR context timeout so the next
	// Stripe retry attempt is processed rather than silently skipped.
	// Use context.Background() — the original ctx may already be expired.
	if handlerErr != nil || ctx.Err() != nil {
		releaseWebhookClaim(context.Background(), db, event.ID)

		if ctx.Err() != nil {
			slog.Error("billing webhook: handler timed out",
				"event_id", event.ID,
				"event_type", event.Type,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			return c.Status(http.StatusInternalServerError).SendString("handler timeout")
		}

		slog.Error("billing webhook: event handler failed",
			"event_id", event.ID,
			"event_type", event.Type,
			"err", handlerErr,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return c.Status(http.StatusInternalServerError).SendString("handler error")
	}

	return c.SendStatus(http.StatusOK)
}

// claimWebhookEvent inserts the event ID into the idempotency table.
// Returns (true, nil)  — event claimed, proceed with processing.
// Returns (false, nil) — event already processed, skip (duplicate).
// Returns (false, err) — DB error, return 500 to trigger Stripe retry.
func claimWebhookEvent(ctx context.Context, db DBPool, eventID, eventType, tenantID string) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO stripe_webhook_events (stripe_event_id, event_type, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (stripe_event_id) DO NOTHING
	`, eventID, eventType, tenantID)
	if err != nil {
		return false, fmt.Errorf("claim webhook event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// releaseWebhookClaim deletes the idempotency row so the event can be
// reprocessed on the next Stripe delivery attempt.
// Always called with context.Background() — caller's ctx may be expired.
func releaseWebhookClaim(ctx context.Context, db DBPool, eventID string) {
	if _, err := db.Exec(ctx,
		"DELETE FROM stripe_webhook_events WHERE stripe_event_id = $1",
		eventID,
	); err != nil {
		slog.Error("billing webhook: failed to release claim",
			"event_id", eventID,
			"err", err,
		)
	}
}

// handleWebhookEvent routes a verified, deduplicated Stripe event.
// Returns non-nil error only when a DB write fails (signals Stripe to retry).
// Malformed payloads return nil — retrying a malformed payload never helps.
func handleWebhookEvent(ctx context.Context, event stripe.Event, svc Service) error {
	switch event.Type {

	//  checkout.session.completed
	case "checkout.session.completed":
		var sess stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
			slog.Error("billing webhook: unmarshal checkout.session.completed", "err", err)
			return nil // malformed — retrying never helps
		}
		if sess.Mode != stripe.CheckoutSessionModeSubscription {
			return nil
		}

		customerID := extractCustomerID(sess.Customer)
		subID := extractSubscriptionID(sess.Subscription)

		// Attempt to resolve plan from the checkout session's line items.
		// Line items are not expanded by default in checkout.session.completed —
		// if PlanFromPriceID returns false, default to Builder (the only plan
		// currently sold via Checkout). customer.subscription.updated fires
		// immediately after and will correct the plan if needed.
		planName, ok := svc.PlanFromPriceID(extractCheckoutPriceID(sess))
		if !ok {
			planName = PlanBuilder
			slog.Warn("billing webhook: checkout price not in map, defaulting to builder",
				"customer_id", customerID,
			)
		}

		slog.Info("billing webhook: checkout completed",
			"customer_id", customerID,
			"subscription_id", subID,
			"plan", planName,
		)
		return svc.ApplyPlan(ctx, customerID, subID, planName)

	//  customer.subscription.updated
	case "customer.subscription.updated":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			slog.Error("billing webhook: unmarshal subscription.updated", "err", err)
			return nil
		}
		if len(sub.Items.Data) == 0 {
			return nil
		}

		customerID := sub.Customer.ID

		// User cancelled via the Portal — subscription stays active until period end.
		// Write the cancel date so the frontend can warn the user.
		if sub.CancelAtPeriodEnd && sub.CancelAt > 0 {
			slog.Info("billing webhook: subscription cancellation scheduled",
				"customer_id", customerID,
				"cancel_at", sub.CancelAt,
			)
			return svc.ScheduleCancel(ctx, customerID, sub.CancelAt)
		}

		// User reactivated a previously-cancelled subscription, or plan changed.
		// Resolve the plan from the price and apply it (also clears cancel / payment_failed columns).
		priceID := sub.Items.Data[0].Price.ID
		planName, ok := svc.PlanFromPriceID(priceID)
		if !ok {
			slog.Warn("billing webhook: subscription updated with unknown price",
				"price_id", priceID,
				"customer_id", customerID,
			)
			return nil
		}

		slog.Info("billing webhook: subscription updated",
			"customer_id", customerID,
			"plan", planName,
			"cancel_at_period_end", sub.CancelAtPeriodEnd,
		)
		return svc.ApplyPlan(ctx, customerID, sub.ID, planName)

	//  customer.subscription.deleted
	case "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			slog.Error("billing webhook: unmarshal subscription.deleted", "err", err)
			return nil
		}

		// Guard: cancel_at_period_end means the user cancelled but has paid
		// through the end of the billing period. Do NOT downgrade immediately.
		// Stripe fires this event again at actual period end — ClearPlan runs then.
		if sub.CancelAtPeriodEnd {
			slog.Info("billing webhook: subscription cancel_at_period_end — deferring downgrade",
				"customer_id", sub.Customer.ID,
			)
			return nil
		}

		slog.Info("billing webhook: subscription deleted — downgrading to free",
			"customer_id", sub.Customer.ID,
		)
		return svc.ClearPlan(ctx, sub.Customer.ID)

	//  invoice.payment_failed
	case "invoice.payment_failed":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
			slog.Error("billing webhook: unmarshal invoice.payment_failed", "err", err)
			return nil
		}

		// Do NOT downgrade here — Stripe Smart Retries handles recovery.
		// If all retries exhaust, customer.subscription.deleted fires and
		// ClearPlan runs. NotifyPaymentFailed writes payment_failed_at for the in-app banner.
		_ = svc.NotifyPaymentFailed(ctx, inv.Customer.ID, inv.AttemptCount)
		return nil

	//  invoice.payment_succeeded
	// Fires when a retry succeeds after a previous failure, or on the first
	// successful payment. Clears payment_failed_at so the in-app banner disappears.
	case "invoice.payment_succeeded":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
			slog.Error("billing webhook: unmarshal invoice.payment_succeeded", "err", err)
			return nil
		}
		if inv.Subscription == nil || inv.Customer == nil {
			return nil
		}
		// Determine plan from subscription price — needed for ApplyPlan which clears payment_failed_at.
		priceID := ""
		for _, line := range inv.Lines.Data {
			if line.Price != nil {
				priceID = line.Price.ID
				break
			}
		}
		planName, ok := svc.PlanFromPriceID(priceID)
		if !ok {
			// Price unknown — can't call ApplyPlan; clear payment_failed_at directly via ScheduleCancel trick.
			// This is a no-op path: checkout.session.completed already fired for initial payments.
			slog.Info("billing webhook: payment_succeeded with unknown price — skipping plan apply",
				"customer_id", inv.Customer.ID)
			return nil
		}
		slog.Info("billing webhook: payment succeeded — clearing payment failure flag",
			"customer_id", inv.Customer.ID,
		)
		// ApplyPlan always clears payment_failed_at (and subscription_cancel_at if reactivated).
		return svc.ApplyPlan(ctx, inv.Customer.ID, inv.Subscription.ID, planName)

	default:
		// Unhandled event — acknowledge so Stripe marks it delivered.
		return nil
	}
}

//  Stripe object extraction helpers

func extractCustomerID(c *stripe.Customer) string {
	if c == nil {
		return ""
	}
	return c.ID
}

func extractSubscriptionID(s *stripe.Subscription) string {
	if s == nil {
		return ""
	}
	return s.ID
}

// extractCheckoutPriceID returns the price ID from a checkout session's line
// items if they were expanded. Returns empty string when not available —
// caller should fall back to a default plan.
func extractCheckoutPriceID(sess stripe.CheckoutSession) string {
	if sess.LineItems == nil || len(sess.LineItems.Data) == 0 {
		return ""
	}
	if sess.LineItems.Data[0].Price == nil {
		return ""
	}
	return sess.LineItems.Data[0].Price.ID
}

// extractCustomerIDFromEvent extracts the Stripe customer ID early for tenant resolution.
func extractCustomerIDFromEvent(event stripe.Event) string {
	switch event.Type {
	case "checkout.session.completed":
		var sess stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sess); err == nil && sess.Customer != nil {
			return sess.Customer.ID
		}
	case "customer.subscription.updated", "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err == nil && sub.Customer != nil {
			return sub.Customer.ID
		}
	case "invoice.payment_failed", "invoice.payment_succeeded":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err == nil && inv.Customer != nil {
			return inv.Customer.ID
		}
	}
	return ""
}
