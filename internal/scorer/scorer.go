package scorer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/shawgichan/cv-screener-backend/internal/config"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type RequirementResult struct {
	Requirement string `json:"requirement"`
	Status      string `json:"status"`   // "matched", "unclear", "missing"
	Evidence    string `json:"evidence"` // AI-generated explanation
}

// MatchResult represents the structured response from the AI for a single CV.
type MatchResult struct {
	CandidateName   string              `json:"candidate_name"`
	Score           int                 `json:"score"` // 0-100
	Requirements    []RequirementResult `json:"requirements"`
	Summary         string              `json:"summary"`
	JDFormatWarning string              `json:"jd_format_warning,omitempty"`
}

// CVInput represents the input from the frontend.
type CVInput struct {
	Filename string `json:"filename"`
	Text     string `json:"cv_text"`
}

// ScoredCV combines the input and the result for sorting.
type ScoredCV struct {
	Filename string      `json:"filename"`
	Result   MatchResult `json:"result"`
	Error    string      `json:"error,omitempty"`
}

// ProcessBatch takes a JD and a slice of CVs, and scores them concurrently using errgroup.
func ProcessBatch(ctx context.Context, cfg config.Config, extractionLimiter *rate.Limiter, scoringLimiter *rate.Limiter, jd string, cvs []CVInput) []ScoredCV {
	if cfg.TypeSafeAPIKey == "" {
		slog.Warn("TYPESAFE_API_KEY is not set")
	}

	results := make([]ScoredCV, len(cvs))

	// Pre-process JD to extract requirements cleanly via AI
	reqs, err := ExtractJDRequirements(ctx, cfg, extractionLimiter, jd)
	if err != nil {
		slog.Warn("AI JD extraction failed, falling back to heuristic", "error", err)
		reqs = parseRequirements(jd)
	} else if len(reqs) == 0 {
		reqs = parseRequirements(jd)
	}

	// errgroup with context for fan-out
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.MaxConcurrency)

	for i, cv := range cvs {
		i, cv := i, cv
		g.Go(func() error {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in CV processing", "panic", r, "filename", cv.Filename)
					results[i].Error = fmt.Sprintf("internal error processing CV: %v", r)
					results[i].Filename = cv.Filename
				}
			}()

			res, err := scoreCV(gCtx, cfg, extractionLimiter, scoringLimiter, reqs, jd, cv.Text)

			results[i] = ScoredCV{
				Filename: cv.Filename,
			}

			if err != nil {
				results[i].Error = err.Error()
			} else {
				results[i].Result = res
			}
			return nil // handle errors in the results array
		})
	}

	_ = g.Wait()
	return results
}

// parseRequirements is a best-effort heuristic, not a robust parser.
// It relies on keyword sniffing and line length to extract bullet points.
// It will misparse a meaningful fraction of real-world JDs, but this is handled
// gracefully by the fallback flow in scoreCV which assesses the JD as a whole.
func parseRequirements(jd string) []string {
	var reqs []string
	lines := strings.Split(jd, "\n")

	jdLower := strings.ToLower(jd)
	hasStructuring := strings.Contains(jdLower, "requirement") ||
		strings.Contains(jdLower, "responsibilit") ||
		strings.Contains(jdLower, "qualification") ||
		strings.Contains(jdLower, "what you'll do")

	inRequirements := false

	for _, line := range lines {
		lowerLine := strings.ToLower(strings.TrimSpace(line))

		if strings.HasSuffix(lowerLine, ":") || (len(lowerLine) < 40 && !strings.HasPrefix(lowerLine, "-") && !strings.HasPrefix(lowerLine, "*")) {
			if strings.Contains(lowerLine, "requirement") || strings.Contains(lowerLine, "responsibilit") || strings.Contains(lowerLine, "qualification") || strings.Contains(lowerLine, "what you'll do") {
				inRequirements = true
			} else if strings.Contains(lowerLine, "benefit") || strings.Contains(lowerLine, "perk") || strings.Contains(lowerLine, "offer") || strings.Contains(lowerLine, "overview") || strings.Contains(lowerLine, "about us") {
				inRequirements = false
			}
		}

		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			if inRequirements || !hasStructuring {
				reqs = append(reqs, strings.TrimSpace(line[2:]))
			}
		}
	}
	return reqs
}

// scoreCV makes the actual HTTP call to the AI provider.
func scoreCV(ctx context.Context, cfg config.Config, extractionLimiter *rate.Limiter, scoringLimiter *rate.Limiter, reqs []string, jd, cvText string) (MatchResult, error) {
	stateText := fmt.Sprintf("Job Description:\n%s\n\nCandidate CV:\n%s", jd, cvText)

	// Payload shape differs between official and reseller
	isOfficial := cfg.ProviderID == "official"

	choiceKey := "criteria"
	if isOfficial {
		choiceKey = "options"
	}

	scoreKey := "criteria"
	if isOfficial {
		scoreKey = "levels"
	}

	questions := map[string]interface{}{
		"fit_score": map[string]interface{}{
			"type":         "score",
			"instructions": "Overall fit score based on requirements.",
			scoreKey:       []string{"0", "20", "40", "60", "80", "100"},
		},
		"summary_choice": map[string]interface{}{
			"type":         "choice",
			"instructions": "Summarize the candidate's fit.",
		},
	}

	if isOfficial {
		questions["summary_choice"].(map[string]interface{})[choiceKey] = []string{"strong", "partial", "poor"}
	} else {
		questions["summary_choice"].(map[string]interface{})[choiceKey] = map[string]string{
			"strong":  "Strong match with core requirements.",
			"partial": "Matches some requirements but missing others.",
			"poor":    "Does not meet core requirements.",
		}
	}

	if len(reqs) == 0 {
		// Fallback for unstructured JDs - we can't extract free-text evidence,
		// but we still want an overall score and summary.
	} else {
		for i, req := range reqs {
			questions[fmt.Sprintf("req_%d", i)] = map[string]interface{}{
				// "noul" is not a typo; it is the TypeSafe API primitive representing a probabilistic boolean.
				"type":         "noul",
				"instructions": fmt.Sprintf("Does the CV meet this requirement: %s", req),
			}
		}
	}

	payload := map[string]interface{}{
		"state":     stateText,
		"questions": questions,
	}

	if isOfficial {
		payload["model"] = "jev-latest"
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return MatchResult{}, fmt.Errorf("failed to encode request: %w", err)
	}

	var apiResp struct {
		Answers map[string]map[string]interface{} `json:"answers"`
	}

	client := &http.Client{Timeout: time.Duration(cfg.APITimeoutSecs) * time.Second}

	// Retrying here: the interim provider occasionally times out under load
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(cfg.RetryBaseDelayMs) * time.Millisecond * time.Duration(1<<uint(attempt-1))
			slog.Info("retrying API call", "attempt", attempt, "backoff", backoff)
			select {
			case <-ctx.Done():
				return MatchResult{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		if err := scoringLimiter.Wait(ctx); err != nil {
			return MatchResult{}, fmt.Errorf("rate limiter wait failed: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TypeSafeAPIURL, bytes.NewBuffer(bodyBytes))
		if err != nil {
			return MatchResult{}, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		if cfg.TypeSafeAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.TypeSafeAPIKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("api request failed: %w", err)
			continue // network error, retry
		}

		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(body))
			slog.Error("api call failed", "attempt", attempt, "status", resp.StatusCode, "err", lastErr)
			continue // 5xx error, retry
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return MatchResult{}, fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(body))
		}

		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			resp.Body.Close()
			return MatchResult{}, fmt.Errorf("failed to decode api response: %w", err)
		}

		resp.Body.Close()
		lastErr = nil // success
		break
	}

	if lastErr != nil {
		return MatchResult{}, lastErr
	}

	var match MatchResult
	if len(reqs) == 0 {
		match.JDFormatWarning = "No structured requirements found in the job description. For best results, use a bulleted list of requirements."
	}

	var summaryChoice string
	if ans, ok := apiResp.Answers["summary_choice"]; ok {
		if choice, ok := ans["choice"].(string); ok {
			summaryChoice = choice
		}
	}
	switch summaryChoice {
	case "strong":
		match.Summary = "Strong match with core requirements."
	case "partial":
		match.Summary = "Matches some requirements but missing others."
	default:
		match.Summary = "Does not meet core requirements."
	}

	var scoreFloat float64
	if ans, ok := apiResp.Answers["fit_score"]; ok {
		if score, ok := ans["score"].(float64); ok {
			scoreFloat = score
		}
	}

	// The API returns a continuous float corresponding to the index of the score criteria array.
	// We passed 6 levels: ["0", "20", "40", "60", "80", "100"].
	// The returned score is 0.0 to 5.0. We map this back to a 0-100 percentage.
	match.Score = int(math.Round((scoreFloat / 5.0) * 100.0))
	if match.Score > cfg.ScoreMax {
		match.Score = cfg.ScoreMax
	} else if match.Score < cfg.ScoreMin {
		match.Score = cfg.ScoreMin
	}

	var statusesToJustify []RequirementStatus

	for i, requirement := range reqs {
		ans, ok := apiResp.Answers[fmt.Sprintf("req_%d", i)]
		if !ok {
			continue
		}

		var status string
		prob, _ := ans["noul"].(float64)
		if prob >= cfg.MatchThresholdHigh {
			status = "matched"
		} else if prob <= cfg.MatchThresholdLow {
			status = "missing"
		} else {
			status = "unclear"
		}

		match.Requirements = append(match.Requirements, RequirementResult{
			Requirement: requirement,
			Status:      status,
			Evidence:    "", // to be filled
		})

		statusesToJustify = append(statusesToJustify, RequirementStatus{
			Requirement: requirement,
			Status:      status,
		})
	}

	// Call Path C justification LLM
	justifications, err := JustifyJevDecisions(ctx, cfg, extractionLimiter, cvText, statusesToJustify)
	if err != nil {
		slog.Warn("LLM justification failed, falling back to empty evidence", "error", err)
	} else {
		// Map justifications back to results
		for i, reqRes := range match.Requirements {
			if ev, ok := justifications[reqRes.Requirement]; ok {
				match.Requirements[i].Evidence = ev
			}
		}
	}

	return match, nil
}
