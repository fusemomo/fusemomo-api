package config

import (
	"fusemomo-api/internal/utils"
	"log"

	"github.com/joho/godotenv"
)

type Config struct {
	API_PORT                    int
	DATABASE_URL                string
	ENV                         string
	SUPABASE_JWT_JWK            string
	JWT_EXPIRATION              int64
	REFRESH_TOKEN_EXPIRATION    int64
	PUBLISH_KEY                 string
	SECRET_KEY                  string
	SUPABASE_URL                string
	FRONTEND_URL                string
	WEIGHT_SUCCESS_RATE         float64
	WEIGHT_RECENCY              float64
	WEIGHT_FEEDBACK             float64
	RECENCY_DECAY_LAMBDA        float64
	MIN_INTERACTIONS_CONFIDENCE int
	FEEDBACK_WEIGHT_MIN         float64
	FEEDBACK_WEIGHT_MAX         float64
}

var Envs = initConfig()

func initConfig() Config {
	if err := godotenv.Overload(); err != nil {
		log.Printf("INFO: Could not load .env file: %v. This is normal if running in CI/Prod where env vars are set directly.", err)
	}

	return Config{
		API_PORT:                    utils.GetEnvAsInt("PORT", 8000),
		DATABASE_URL:                utils.GetEnv("DATABASE_URL", ""),
		ENV:                         utils.GetEnv("ENV", "dev"),
		SUPABASE_JWT_JWK:            utils.GetEnv("SUPABASE_JWT_JWK", "super-secret-key-change-me"),
		JWT_EXPIRATION:              utils.GetEnvAsInt64("JWT_EXPIRATION", 15*60),                 // 15 minutes default
		REFRESH_TOKEN_EXPIRATION:    utils.GetEnvAsInt64("REFRESH_TOKEN_EXPIRATION", 30*24*60*60), // 30 days default
		PUBLISH_KEY:                 utils.GetEnv("PUBLISH_KEY", ""),
		SECRET_KEY:                  utils.GetEnv("SECRET_KEY", ""),
		SUPABASE_URL:                utils.GetEnv("SUPABASE_URL", ""),
		FRONTEND_URL:                utils.GetEnv("FRONTEND_URL", "http://localhost:5200"),
		WEIGHT_SUCCESS_RATE:         utils.GetEnvAsFloat("FM_SCORE_WEIGHT_SUCCESS_RATE", 0.55),
		WEIGHT_RECENCY:              utils.GetEnvAsFloat("FM_SCORE_WEIGHT_RECENCY", 0.30),
		WEIGHT_FEEDBACK:             utils.GetEnvAsFloat("FM_SCORE_WEIGHT_FEEDBACK", 0.15),
		RECENCY_DECAY_LAMBDA:        utils.GetEnvAsFloat("FM_RECENCY_DECAY_LAMBDA", 0.02),
		MIN_INTERACTIONS_CONFIDENCE: utils.GetEnvAsInt("FM_MIN_INTERACTIONS_CONFIDENCE", 5),
		FEEDBACK_WEIGHT_MIN:         utils.GetEnvAsFloat("FM_FEEDBACK_WEIGHT_MIN", 0.5),
		FEEDBACK_WEIGHT_MAX:         utils.GetEnvAsFloat("FM_FEEDBACK_WEIGHT_MAX", 1.5),
	}
}
