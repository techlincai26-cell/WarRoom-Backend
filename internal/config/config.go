package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port          string
	DatabaseURL   string
	JWTSecret     string
	GeminiAPIKey  string
	AIModel       string
	RunMigrations bool
}

func LoadConfig() *Config {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	return &Config{
		Port: getEnv("PORT", "8080"),
		// Force local SQLite for reliable development
		DatabaseURL:   getEnv("DATABASE_URL", "warroom.db"),
		JWTSecret:     getEnv("JWT_SECRET", "super-secret-key"),
		GeminiAPIKey:  getEnv("GEMINI_API_KEY", ""),
		AIModel:       getEnv("AI_MODEL", "gemini-2.0-flash-lite-preview-02-05"),
		RunMigrations: getEnv("RUN_MIGRATIONS", "false") == "true",
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
