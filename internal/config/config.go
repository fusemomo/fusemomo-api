package config

import (
	"fusemomo-api/internal/utils"
	"log"

	"github.com/joho/godotenv"
)

type Config struct {
	API_PORT                 int
	DATABASE_URL             string
	ENV                      string
	SUPABASE_JWT_JWK         string
	JWT_EXPIRATION           int64
	REFRESH_TOKEN_EXPIRATION int64
	PUBLISH_KEY              string
	SECRET_KEY               string
	SUPABASE_URL             string
	FRONTEND_URL             string
}

var Envs = initConfig()

func initConfig() Config {
	if err := godotenv.Overload(); err != nil {
		log.Printf("INFO: Could not load .env file: %v. This is normal if running in CI/Prod where env vars are set directly.", err)
	}

	return Config{
		API_PORT:                 utils.GetEnvAsInt("PORT", 8000),
		DATABASE_URL:             utils.GetEnv("DATABASE_URL", ""),
		ENV:                      utils.GetEnv("ENV", "dev"),
		SUPABASE_JWT_JWK:         utils.GetEnv("SUPABASE_JWT_JWK", "super-secret-key-change-me"),
		JWT_EXPIRATION:           utils.GetEnvAsInt64("JWT_EXPIRATION", 15*60),                 // 15 minutes default
		REFRESH_TOKEN_EXPIRATION: utils.GetEnvAsInt64("REFRESH_TOKEN_EXPIRATION", 30*24*60*60), // 30 days default
		PUBLISH_KEY:              utils.GetEnv("PUBLISH_KEY", ""),
		SECRET_KEY:               utils.GetEnv("SECRET_KEY", ""),
		SUPABASE_URL:             utils.GetEnv("SUPABASE_URL", ""),
		FRONTEND_URL:             utils.GetEnv("FRONTEND_URL", "http://localhost:5200"),
	}
}
