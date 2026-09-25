package scorer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shawgichan/cv-screener-backend/internal/config"
	"golang.org/x/time/rate"
)

func TestParseRequirements(t *testing.T) {
	jd := `Job Title: Software Engineer
Requirements:
- 5+ years of Go experience
* Strong knowledge of microservices
- Familiarity with AWS`

	reqs := parseRequirements(jd)
	expected := []string{"5+ years of Go experience", "Strong knowledge of microservices", "Familiarity with AWS"}

	if len(reqs) != len(expected) {
		t.Fatalf("Expected %d requirements, got %d", len(expected), len(reqs))
	}

	for i, req := range expected {
		if reqs[i] != req {
			t.Errorf("Expected %q, got %q", req, reqs[i])
		}
	}
}

func TestEmptyRequirements(t *testing.T) {
	jd := `We are looking for a great developer. You must have Go and AWS experience. No bullet points.`
	reqs := parseRequirements(jd)
	if len(reqs) != 0 {
		t.Fatalf("Expected 0 requirements for unstructured JD, got %d", len(reqs))
	}
}

func TestScoreCV(t *testing.T) {
	envData, err := os.ReadFile("../../.env")
	if err != nil {
		t.Skip("Skipping integration test: .env not found")
	}

	var apiKey string
	for _, line := range strings.Split(string(envData), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TYPESAFE_API_KEY=") {
			apiKey = strings.TrimPrefix(line, "TYPESAFE_API_KEY=")
			break
		}
	}

	if apiKey == "" {
		t.Skip("Skipping integration test: TYPESAFE_API_KEY not found in .env")
	}

	cfg := config.Load()
	cfg.TypeSafeAPIKey = apiKey
	if cfg.TypeSafeAPIURL == "" {
		cfg.TypeSafeAPIURL = "https://jevtypesafeai.com/api/v1/decide"
	}
	// Need Gemini configs if the test calls it, but we can bypass the AI extraction by just passing parseRequirements directly to scoreCV

	jd := `Job Title: Backend Developer
Requirements:
- Strong Go experience
- Experience with Docker`

	cv := `Backend Developer with 6 years of experience.
I have strong Go experience and I use Docker daily in production.`

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	limiter := rate.NewLimiter(rate.Every(1*time.Millisecond), 1)
	reqs := parseRequirements(jd)

	res, err := scoreCV(ctx, cfg, limiter, reqs, jd, cv)
	if err != nil {
		t.Fatalf("scoreCV returned error: %v", err)
	}

	if res.Score < 0 || res.Score > 100 {
		t.Errorf("Expected score between 0 and 100, got %d", res.Score)
	}

	if res.Summary == "" {
		t.Error("Expected summary to not be empty")
	}

	matchedCount := 0
	for _, req := range res.Requirements {
		if req.Status == "matched" {
			matchedCount++
		}
	}
	if matchedCount == 0 {
		t.Errorf("Expected at least 1 matched skill, got %v", res.Requirements)
	}
}
