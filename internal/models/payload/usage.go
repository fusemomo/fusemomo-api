package payload

type UsageResponse struct {
	Period              string  `json:"period"`
	ResolutionCount     int     `json:"resolution_count"`
	ResolutionLimit     int     `json:"resolution_limit"`
	InteractionCount    int     `json:"interaction_count"`
	InteractionLimit    int     `json:"interaction_limit"`
	RecommendationCount int     `json:"recommendation_count"`
	Plan                string  `json:"plan"`
	PercentageUsed      float64 `json:"percentage_used"`
}
