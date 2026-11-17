package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port              string
	DatabaseURL       string
	SupabaseURL       string
	SupabaseJWTSecret string
	DevAuthSecret     string // enables the dev-only phone login; empty in production

	// Logo uploads. Leave the service key empty to disable POST /v1/uploads/logo.
	SupabaseServiceKey string
	StorageURL         string // default: <SUPABASE_URL>/storage/v1

	TrialDays int // length of the free trial for a new shop (TRIAL_DAYS, default 14)
}

func Load() Config {
	_ = godotenv.Load() // reads .env in local dev; ignored in production

	c := Config{
		Port:              getenv("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		SupabaseURL:       os.Getenv("SUPABASE_URL"),
		SupabaseJWTSecret: os.Getenv("SUPABASE_JWT_SECRET"),
		DevAuthSecret:     os.Getenv("DEV_AUTH_SECRET"),

		SupabaseServiceKey: os.Getenv("SUPABASE_SERVICE_KEY"),
		StorageURL:         os.Getenv("SUPABASE_STORAGE_URL"),
	}
	c.TrialDays = 14
	if n, err := strconv.Atoi(os.Getenv("TRIAL_DAYS")); err == nil && n > 0 {
		c.TrialDays = n
	}
	if c.DatabaseURL == "" || c.SupabaseURL == "" {
		log.Fatal("DATABASE_URL and SUPABASE_URL are required")
	}
	if c.StorageURL == "" {
		c.StorageURL = strings.TrimRight(c.SupabaseURL, "/") + "/storage/v1"
	}
	return c
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
