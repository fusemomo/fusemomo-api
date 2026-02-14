package config

import (
	"fusemomo-api/internal/utils"
	"log"

	"github.com/joho/godotenv"
)

type Config struct {
	API_PORT     int
	DATABASE_URL string
	ENV          string
}

var Envs = initConfig()

func initConfig() Config {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	return Config{
		API_PORT:     utils.GetEnvAsInt("PORT", 8000),
		DATABASE_URL: utils.GetEnv("DATABASE_URL", ""),
		ENV:          utils.GetEnv("ENV", "dev"),
	}
}
