package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shawgichan/cv-screener-backend/internal/config"
	"github.com/shawgichan/cv-screener-backend/internal/ratelimit"
	"github.com/shawgichan/cv-screener-backend/internal/scorer"
	"golang.org/x/time/rate"
)

type EvaluateRequest struct {
	JobDescription string           `json:"jd_text"`
	CVs            []scorer.CVInput `json:"cvs"`
}

func main() {
	// Initialize structured logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()
	userLimiter := ratelimit.NewRateLimiter(cfg.RateLimitPerDay)

	// Split API limiters so JD extraction doesn't block CV scoring
	throttleDur := time.Duration(cfg.GlobalAPIThrottleMs) * time.Millisecond
	extractionLimiter := rate.NewLimiter(rate.Every(throttleDur), 1)
	scoringLimiter := rate.NewLimiter(rate.Every(throttleDur), 1)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	})

	mux.HandleFunc("/api/evaluate", rateLimitMiddleware(userLimiter, cfg, evaluateHandler(cfg, extractionLimiter, scoringLimiter)))

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	slog.Info("server starting", "port", cfg.Port)
	if err := server.ListenAndServe(); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func rateLimitMiddleware(limiter *ratelimit.RateLimiter, cfg config.Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", cfg.CORSAllowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", cfg.CORSAllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", cfg.CORSAllowHeaders)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			} else {
				ip = host
			}
		} else {
			// Proxies append to the end, so the right-most IP is the one from the trusted proxy
			ips := strings.Split(ip, ",")
			ip = strings.TrimSpace(ips[len(ips)-1])
		}

		if !limiter.Allow(ip) {
			http.Error(w, fmt.Sprintf("Rate limit exceeded. Max %d requests per day.", cfg.RateLimitPerDay), http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func evaluateHandler(cfg config.Config, extractionLimiter *rate.Limiter, scoringLimiter *rate.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req EvaluateRequest

		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request: "+err.Error(), http.StatusBadRequest)
			return
		}

		if len(req.CVs) > cfg.MaxCVsPerRequest {
			http.Error(w, fmt.Sprintf("Max %d CVs allowed per request", cfg.MaxCVsPerRequest), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.APITimeoutSecs)*time.Second)
		defer cancel()

		results := scorer.ProcessBatch(ctx, cfg, extractionLimiter, scoringLimiter, req.JobDescription, req.CVs)

		sort.Slice(results, func(i, j int) bool {
			return results[i].Result.Score > results[j].Result.Score
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(results); err != nil {
			slog.Error("Error encoding response", "error", err)
		}
	}
}
