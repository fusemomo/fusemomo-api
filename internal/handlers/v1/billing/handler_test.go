package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
)

// testWebhookSecret is used to construct valid Stripe signatures in tests.
const testWebhookSecret = "whsec_test_secret_for_unit_tests"

//  Helpers

// signPayload serialises the event and returns both the raw body and a valid
// Stripe-Signature header value using ComputeSignature (the same HMAC used in
// production).
func signPayload(t *testing.T, event stripe.Event) (body []byte, sig string) {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal stripe event: %v", err)
	}
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: body,
		Secret:  testWebhookSecret,
	})
	return body, sp.Header
}

// mockDBPool is a minimal DBPool stub for webhook handler tests.
// Exec always reports 1 row affected (event claimed) so the idempotency
// INSERT path succeeds and the handler reaches the event dispatcher.
type mockDBPool struct{}

func (m *mockDBPool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	// Simulate INSERT ... ON CONFLICT DO NOTHING claiming the row (1 row affected).
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
type mockRow struct{}

func (m mockRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if s, ok := dest[0].(*string); ok {
			*s = "00000000-0000-0000-0000-000000000000"
		}
	}
	return nil
}

func (m *mockDBPool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return mockRow{} }
func (m *mockDBPool) Begin(_ context.Context) (pgx.Tx, error)               { return nil, nil }

// setupApp returns a Fiber app with a single POST /webhook route wired to the
// billing handler backed by the provided (mock) service.
func setupApp(svc Service) *fiber.App {
	app := fiber.New()
	bh := NewBillingHandler(svc, &mockDBPool{})
	app.Post("/webhook", bh.HandleStripeWebhook)
	return app
}

// doPost fires a POST to the test app's /webhook route.
func doPost(t *testing.T, app *fiber.App, body []byte, sigHeader string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

//  Event factories

func checkoutCompletedEvent(customerID, subscriptionID string) stripe.Event {
	sess := stripe.CheckoutSession{
		Mode:         stripe.CheckoutSessionModeSubscription,
		Customer:     &stripe.Customer{ID: customerID},
		Subscription: &stripe.Subscription{ID: subscriptionID},
	}
	raw, _ := json.Marshal(sess)
	return stripe.Event{
		Type:       "checkout.session.completed",
		APIVersion: "2023-10-16",
		Data:       &stripe.EventData{Raw: raw},
	}
}

func subscriptionDeletedEvent(customerID string) stripe.Event {
	sub := stripe.Subscription{Customer: &stripe.Customer{ID: customerID}}
	raw, _ := json.Marshal(sub)
	return stripe.Event{
		Type:       "customer.subscription.deleted",
		APIVersion: "2023-10-16",
		Data:       &stripe.EventData{Raw: raw},
	}
}

func paymentFailedEvent(customerID string) stripe.Event {
	inv := stripe.Invoice{
		Customer:         &stripe.Customer{ID: customerID},
		HostedInvoiceURL: "https://invoice.stripe.com/i/test",
	}
	raw, _ := json.Marshal(inv)
	return stripe.Event{
		Type:       "invoice.payment_failed",
		APIVersion: "2023-10-16",
		Data:       &stripe.EventData{Raw: raw},
	}
}

//  Tests

// TestWebhook_InvalidSignature_Returns400 verifies that a tampered or missing
// Stripe signature is rejected with 400 before any service methods are called.
func TestWebhook_InvalidSignature_Returns400(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	app := setupApp(svc)

	body := []byte(`{"type":"checkout.session.completed","data":{"object":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=0,v1=badhashvalue")

	resp, _ := app.Test(req)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	svc.AssertNotCalled(t, "ApplyPlan")
	svc.AssertNotCalled(t, "ClearPlan")
}

// TestWebhook_CheckoutCompleted_CallsApplyPlanBuilder verifies that a valid
// checkout.session.completed event triggers ApplyPlan with PlanBuilder.
func TestWebhook_CheckoutCompleted_CallsApplyPlanBuilder(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	svc.On("PlanFromPriceID", mock.Anything).Return(PlanBuilder, true)
	svc.On("ApplyPlan", mock.Anything, "cus_test123", "sub_test456", PlanBuilder).Return(nil)

	app := setupApp(svc)
	body, sig := signPayload(t, checkoutCompletedEvent("cus_test123", "sub_test456"))

	resp := doPost(t, app, body, sig)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	svc.AssertCalled(t, "ApplyPlan", mock.Anything, "cus_test123", "sub_test456", PlanBuilder)
}

// TestWebhook_SubscriptionDeleted_CallsClearPlan verifies that a
// customer.subscription.deleted event triggers ClearPlan with the correct
// customer ID.
func TestWebhook_SubscriptionDeleted_CallsClearPlan(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	svc.On("ClearPlan", mock.Anything, "cus_delete999").Return(nil)

	app := setupApp(svc)
	body, sig := signPayload(t, subscriptionDeletedEvent("cus_delete999"))

	resp := doPost(t, app, body, sig)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	svc.AssertCalled(t, "ClearPlan", mock.Anything, "cus_delete999")
}

// TestWebhook_PaymentFailed_Returns200_NoPlanChange verifies that an
// invoice.payment_failed event is logged and acknowledged (200) without
// downgrading the plan. Stripe's Smart Retries will eventually fire
// customer.subscription.deleted if needed.
func TestWebhook_PaymentFailed_Returns200_NoPlanChange(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	svc.On("NotifyPaymentFailed", mock.Anything, "cus_failed777", mock.AnythingOfType("int64")).Return(nil)

	app := setupApp(svc)
	body, sig := signPayload(t, paymentFailedEvent("cus_failed777"))

	resp := doPost(t, app, body, sig)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	svc.AssertNotCalled(t, "ApplyPlan")
	svc.AssertNotCalled(t, "ClearPlan")
}

// TestWebhook_UnhandledEventType_Returns200 ensures that an unknown event type
// is silently acknowledged so Stripe marks it as delivered.
func TestWebhook_UnhandledEventType_Returns200(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	app := setupApp(svc)

	event := stripe.Event{
		Type:       "customer.created", // not handled by our router
		APIVersion: "2023-10-16",
		Data:       &stripe.EventData{Raw: json.RawMessage(`{}`)},
	}
	body, sig := signPayload(t, event)

	resp := doPost(t, app, body, sig)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	svc.AssertNotCalled(t, "ApplyPlan")
	svc.AssertNotCalled(t, "ClearPlan")
}

// TestWebhook_CheckoutCompleted_Idempotent verifies that the webhook layer
// forwards both calls to ApplyPlan — idempotency is enforced inside ApplyPlan
// itself (DB-level check). The handler must never swallow duplicate events.
func TestWebhook_CheckoutCompleted_Idempotent(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", testWebhookSecret)

	svc := &mockService{}
	svc.On("PlanFromPriceID", mock.Anything).Return(PlanBuilder, true)
	svc.On("ApplyPlan", mock.Anything, "cus_idem", "sub_idem", PlanBuilder).Return(nil)

	app := setupApp(svc)
	event := checkoutCompletedEvent("cus_idem", "sub_idem")

	// First delivery
	body1, sig1 := signPayload(t, event)
	resp1 := doPost(t, app, body1, sig1)
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	// Second delivery (Stripe retry) — new signature with the same payload
	body2, sig2 := signPayload(t, event)
	resp2 := doPost(t, app, body2, sig2)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	// Both are forwarded; the service's idempotency check is responsible for
	// deduplication, not the handler.
	svc.AssertNumberOfCalls(t, "ApplyPlan", 2)
}
