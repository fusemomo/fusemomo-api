package utils

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func GetEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return strings.TrimSpace(value)
	}
	if fallback == "" {
		panic(fmt.Sprintf("required environment variable %s not set", key))
	}
	return fallback
}

func GetEnvAsInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		i, err := strconv.Atoi(value)
		if err != nil {
			return fallback
		}
		return i
	}
	return fallback
}

func GetEnvAsFloat(key string, fallback float64) float64 {
	if value, ok := os.LookupEnv(key); ok {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fallback
		}
		return f
	}
	return fallback
}
