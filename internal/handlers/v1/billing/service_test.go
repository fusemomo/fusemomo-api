package billing

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"fusemomo-api/internal/models"
)

//  Mock Service

type mockService struct {
	mock.Mock
}

func (m *mockService) CreateCheckoutSession(ctx context.Context, tenantID uuid.UUID, priceID string) (string, error) {
	args := m.Called(ctx, tenantID, priceID)
	return args.String(0), args.Error(1)
}

func (m *mockService) CreatePortalSession(ctx context.Context, tenantID uuid.UUID) (string, error) {
	args := m.Called(ctx, tenantID)
	return args.String(0), args.Error(1)
}

func (m *mockService) GetStatus(ctx context.Context, tenantID uuid.UUID) (*models.BillingStatusResponse, error) {
	args := m.Called(ctx, tenantID)
	if v := args.Get(0); v != nil {
		return v.(*models.BillingStatusResponse), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mockService) ApplyPlan(ctx context.Context, stripeCustomerID, stripeSubscriptionID string, planName PlanName) error {
	return m.Called(ctx, stripeCustomerID, stripeSubscriptionID, planName).Error(0)
}

func (m *mockService) ClearPlan(ctx context.Context, stripeCustomerID string) error {
	return m.Called(ctx, stripeCustomerID).Error(0)
}

func (m *mockService) ScheduleCancel(ctx context.Context, stripeCustomerID string, cancelAt int64) error {
	return m.Called(ctx, stripeCustomerID, cancelAt).Error(0)
}

func (m *mockService) IsValidPriceID(priceID string) bool {
	return m.Called(priceID).Bool(0)
}

func (m *mockService) PlanFromPriceID(priceID string) (PlanName, bool) {
	args := m.Called(priceID)
	return args.Get(0).(PlanName), args.Bool(1)
}

func (m *mockService) PriceIDForPlan(plan string, interval string) (string, error) {
	args := m.Called(plan, interval)
	return args.String(0), args.Error(1)
}

func (m *mockService) NotifyPaymentFailed(ctx context.Context, stripeCustomerID string, attemptCount int64) error {
	return m.Called(ctx, stripeCustomerID, attemptCount).Error(0)
}

//  Plan Config Tests

func TestGetPlanLimits_Free(t *testing.T) {
	limits, ok := GetPlanLimits(PlanFree)
	assert.True(t, ok)
	assert.Equal(t, 1_000, limits.MonthlyResolutionLimit)
	assert.Equal(t, 10_000, limits.MonthlyInteractionLimit)
	assert.Equal(t, 1, limits.ConnectedAPILimit)
}

func TestGetPlanLimits_Builder(t *testing.T) {
	limits, ok := GetPlanLimits(PlanBuilder)
	assert.True(t, ok)
	assert.Equal(t, 50_000, limits.MonthlyResolutionLimit)
	assert.Equal(t, 500_000, limits.MonthlyInteractionLimit)
	assert.Equal(t, 10, limits.ConnectedAPILimit)
}

func TestGetPlanLimits_Enterprise(t *testing.T) {
	limits, ok := GetPlanLimits(PlanEnterprise)
	assert.True(t, ok)
	assert.Equal(t, Unlimited, limits.MonthlyResolutionLimit)
	assert.Equal(t, Unlimited, limits.MonthlyInteractionLimit)
	assert.Equal(t, Unlimited, limits.ConnectedAPILimit)
}

func TestGetPlanLimits_Unknown(t *testing.T) {
	_, ok := GetPlanLimits("nonexistent_plan")
	assert.False(t, ok)
}

func TestUnlimitedSentinel(t *testing.T) {
	// The sentinel must be negative so application-layer checks `limit < 0` work.
	assert.Less(t, Unlimited, 0)
}

//  IsValidPriceID

func TestIsValidPriceID_ValidAndInvalid(t *testing.T) {
	svc := &billingService{
		priceIDToPlan: map[string]PlanName{
			"price_monthly": PlanBuilder,
			"price_yearly":  PlanBuilder,
		},
	}
	assert.True(t, svc.IsValidPriceID("price_monthly"))
	assert.True(t, svc.IsValidPriceID("price_yearly"))
	assert.False(t, svc.IsValidPriceID("price_unknown"))
	assert.False(t, svc.IsValidPriceID(""))
}

//  Mock Service Interface Compliance

// Compile-time check: mockService satisfies the Service interface.
var _ Service = (*mockService)(nil)
