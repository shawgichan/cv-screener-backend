package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Server
	Environment  string
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// CORS
	CORSAllowOrigin  string
	CORSAllowMethods string
	CORSAllowHeaders string

	// Provider
	TypeSafeAPIURL string
	TypeSafeAPIKey string
	ProviderID     string // "reseller" or "official" — selects the provider implementation

	// Limits
	MaxCVsPerRequest int
	MaxConcurrency   int
	RateLimitPerDay  int
	MaxBodyBytes     int64

	// AI call
	APITimeoutSecs   int
	MaxRetries       int
	RetryBaseDelayMs int

	// JD Extraction (LLM)
	ExtractionAPIKey         string
	ExtractionModel          string
	JDExtractionMaxRetries   int
	JDExtractionRetryDelayMs int

	// Global Throttle
	GlobalAPIThrottleMs int

	// Scoring
	MatchThresholdHigh float64 // >= this probability = "matched"
	MatchThresholdLow  float64 // <= this probability = "missing"
	ScoreMin           int     // floor for clamped score
	ScoreMax           int     // ceiling for clamped score
}

func getEnvInt(key string, def int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return def
}

func getEnvInt64(key string, def int64) int64 {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return def
}

func getEnvDuration(key string, defSecs int) time.Duration {
	return time.Duration(getEnvInt(key, defSecs)) * time.Second
}

func getEnv(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

func Load() Config {
	cfg := Config{
		// Server
		Environment:  getEnv("ENVIRONMENT", "development"),
		Port:         getEnv("PORT", "8083"),
		ReadTimeout:  getEnvDuration("SERVER_READ_TIMEOUT_SECS", 10),
		WriteTimeout: getEnvDuration("SERVER_WRITE_TIMEOUT_SECS", 120),
		IdleTimeout:  getEnvDuration("SERVER_IDLE_TIMEOUT_SECS", 120),

		// CORS — default to wildcard; narrow to the real frontend origin in production
		CORSAllowOrigin:  getEnv("CORS_ALLOW_ORIGIN", "*"),
		CORSAllowMethods: getEnv("CORS_ALLOW_METHODS", "POST, OPTIONS"),
		CORSAllowHeaders: getEnv("CORS_ALLOW_HEADERS", "Content-Type"),

		// Provider
		TypeSafeAPIURL: os.Getenv("TYPESAFE_API_URL"),
		TypeSafeAPIKey: os.Getenv("TYPESAFE_API_KEY"),
		ProviderID:     getEnv("PROVIDER_ID", "reseller"),

		// Limits
		MaxCVsPerRequest: getEnvInt("MAX_CVS_PER_REQUEST", 10),
		MaxConcurrency:   getEnvInt("MAX_CONCURRENCY", 10),
		RateLimitPerDay:  getEnvInt("RATE_LIMIT_PER_DAY", 5),
		MaxBodyBytes:     getEnvInt64("MAX_BODY_BYTES", 2<<20), // 2MB

		// AI call
		APITimeoutSecs:   getEnvInt("API_TIMEOUT_SECS", 60),
		MaxRetries:       getEnvInt("MAX_RETRIES", 3),
		RetryBaseDelayMs: getEnvInt("RETRY_BASE_DELAY_MS", 500),

		// JD Extraction (LLM)
		ExtractionAPIKey:         os.Getenv("EXTRACTION_API_KEY"),
		ExtractionModel:          getEnv("EXTRACTION_MODEL", "gemini-2.5-flash-lite"), // smallest and fastest
		JDExtractionMaxRetries:   getEnvInt("JD_EXTRACTION_MAX_RETRIES", 3),
		JDExtractionRetryDelayMs: getEnvInt("JD_EXTRACTION_RETRY_DELAY_MS", 200),

		// Global Throttle (Space out AI requests across the app)
		GlobalAPIThrottleMs: getEnvInt("GLOBAL_API_THROTTLE_MS", 100),

		// Scoring
		MatchThresholdHigh: getEnvFloat("MATCH_THRESHOLD_HIGH", 0.7),
		MatchThresholdLow:  getEnvFloat("MATCH_THRESHOLD_LOW", 0.3),
		ScoreMin:           0,
		ScoreMax:           100,
	}

	if cfg.TypeSafeAPIURL == "" {
		slog.Error("TYPESAFE_API_URL is required — set it in .env")
		os.Exit(1)
	}
	if cfg.TypeSafeAPIKey == "" {
		slog.Warn("TYPESAFE_API_KEY is not set — API calls will fail if the provider requires authentication")
	}

	// Fail loudly if CORS is too permissive in production
	if cfg.Environment == "production" && cfg.CORSAllowOrigin == "*" {
		slog.Error("CORS_ALLOW_ORIGIN cannot be '*' in production. Please set it to the specific frontend domain.")
		os.Exit(1)
	}

	return cfg
}
