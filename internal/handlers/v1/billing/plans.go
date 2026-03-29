package billing

type PlanName string

const (
	PlanFree       PlanName = "free"
	PlanBuilder    PlanName = "builder"
	PlanEnterprise PlanName = "enterprise"

	Unlimited = -1
)

// PlanLimits holds the numeric quotas for a given plan.
type PlanLimits struct {
	MonthlyResolutionLimit  int
	MonthlyInteractionLimit int
	ConnectedAPILimit       int
}

// planConfig is unexported — access only via GetPlanLimits. Prevents mutation.
var planConfig = map[PlanName]PlanLimits{
	PlanFree: {
		MonthlyResolutionLimit:  1_000,
		MonthlyInteractionLimit: 10_000,
		ConnectedAPILimit:       1,
	},
	PlanBuilder: {
		MonthlyResolutionLimit:  50_000,
		MonthlyInteractionLimit: 500_000,
		ConnectedAPILimit:       10,
	},
	PlanEnterprise: {
		MonthlyResolutionLimit:  Unlimited,
		MonthlyInteractionLimit: Unlimited,
		ConnectedAPILimit:       Unlimited,
	},
}

// GetPlanLimits returns the quota limits for a given plan name.
// Returns (zero, false) for unknown plan names.
func GetPlanLimits(name PlanName) (PlanLimits, bool) {
	l, ok := planConfig[name]
	return l, ok
}
