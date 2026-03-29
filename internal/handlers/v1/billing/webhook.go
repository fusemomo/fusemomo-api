package billing

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
)

// handleWebhookEvent routes a verified Stripe event to the appropriate handler.
// Returns a non-nil error only when the DB write fails — this signals Stripe to retry.
func handleWebhookEvent(ctx context.Context, event stripe.Event, svc Service) error {
	switch event.Type {

	case "checkout.session.completed":
		var sess stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
			slog.Error("billing webhook: unmarshal checkout.session.completed", "err", err)
			return nil // malformed payload — don't ask Stripe to retry
		}

		if sess.Mode != stripe.CheckoutSessionModeSubscription {
			return nil // one-time payment, not our concern
		}

		customerID := ""
		if sess.Customer != nil {
			customerID = sess.Customer.ID
		}
		subID := ""
		if sess.Subscription != nil {
			subID = sess.Subscription.ID
		}

		slog.Info("billing webhook: checkout completed", "customer_id", customerID, "subscription_id", subID)
		// checkout.session.completed does not carry items — we know it's Builder
		// because that's the only plan sold via Checkout. If we add more plans later,
		// read the subscription's items via customer.subscription.updated instead.
		return svc.ApplyPlan(ctx, customerID, subID, PlanBuilder)

	case "customer.subscription.updated":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			slog.Error("billing webhook: unmarshal subscription.updated", "err", err)
			return nil
		}

		customerID := sub.Customer.ID
		if len(sub.Items.Data) == 0 {
			return nil
		}

		priceID := sub.Items.Data[0].Price.ID
		planName, ok := svc.(*billingService).priceIDToPlan[priceID] // safe: webhook handler and service live in same package
		if !ok {
			slog.Warn("billing webhook: subscription updated with unknown price", "price_id", priceID)
			return nil
		}

		slog.Info("billing webhook: subscription updated", "customer_id", customerID, "plan", planName)
		return svc.ApplyPlan(ctx, customerID, sub.ID, planName)

	case "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			slog.Error("billing webhook: unmarshal subscription.deleted", "err", err)
			return nil
		}

		slog.Info("billing webhook: subscription deleted", "customer_id", sub.Customer.ID)
		return svc.ClearPlan(ctx, sub.Customer.ID)

	case "invoice.payment_failed":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
			slog.Error("billing webhook: unmarshal invoice.payment_failed", "err", err)
			return nil
		}

		// Payment failed — Stripe Smart Retries will handle it.
		// If all retries exhaust, `customer.subscription.deleted` fires.
		slog.Warn("billing webhook: payment failed",
			"customer_id", inv.Customer.ID,
			"invoice_url", inv.HostedInvoiceURL,
		)
		return nil

	default:
		// Unhandled event type — acknowledge silently so Stripe marks it delivered.
		return nil
	}
}

// StripeWebhookHandler verifies the Stripe signature then dispatches the event.
// Returns HTTP 400 on invalid signature, HTTP 500 on DB write failure (triggers Stripe retry),
// HTTP 200 on success or for unhandled event types.
func StripeWebhookHandler(c fiber.Ctx, svc Service) error {
	payload := c.Request().Body()
	signatureHeader := c.Get("Stripe-Signature")
	webhookSecret := utils.GetEnv("STRIPE_WEBHOOK_SECRET", "")

	event, err := webhook.ConstructEvent(payload, signatureHeader, webhookSecret)
	if err != nil {
		slog.Warn("billing webhook: signature verification failed", "err", err)
		return c.Status(http.StatusBadRequest).SendString("invalid stripe signature")
	}

	if err := handleWebhookEvent(context.Background(), event, svc); err != nil {
		// DB write failed — return 500 so Stripe retries delivery.
		slog.Error("billing webhook: event handler failed", "event_type", event.Type, "err", err)
		return c.Status(http.StatusInternalServerError).SendString("handler error")
	}

	return c.SendStatus(http.StatusOK)
}
