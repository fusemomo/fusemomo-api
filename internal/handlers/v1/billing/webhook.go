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

	event, err := webhook.ConstructEvent(payload, signatureHeader, webhookSecret)
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

	// Idempotency: claim this event before any processing.
	// Returns false (not an error) when event was already processed.
	claimed, err := claimWebhookEvent(ctx, db, event.ID, string(event.Type))
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
func claimWebhookEvent(ctx context.Context, db DBPool, eventID, eventType string) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO stripe_webhook_events (stripe_event_id, event_type)
		VALUES ($1, $2)
		ON CONFLICT (stripe_event_id) DO NOTHING
	`, eventID, eventType)
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
		// ClearPlan runs. NotifyPaymentFailed gives operators/users a warning.
		_ = svc.NotifyPaymentFailed(ctx, inv.Customer.ID, inv.AttemptCount)
		return nil

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
