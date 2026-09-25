package scorer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shawgichan/cv-screener-backend/internal/config"
	"golang.org/x/time/rate"
)

// ExtractJDRequirements uses the LLM API to parse an unstructured JD into a clean list of core requirements.
func ExtractJDRequirements(ctx context.Context, cfg config.Config, limiter *rate.Limiter, jd string) ([]string, error) {
	if cfg.ExtractionAPIKey == "" {
		return nil, fmt.Errorf("EXTRACTION_API_KEY is not configured")
	}

	prompt := "Extract a list of 5-10 core technical and professional requirements from this Job Description. " +
		"Return ONLY a JSON array of strings. Do not include benefits, perks, or generic company descriptions.\n\nJD:\n" + jd

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"response_mime_type": "application/json",
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode LLM request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", cfg.ExtractionModel, cfg.ExtractionAPIKey)
	client := &http.Client{Timeout: time.Duration(cfg.APITimeoutSecs) * time.Second}

	var lastErr error
	for attempt := 0; attempt <= cfg.JDExtractionMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(cfg.JDExtractionRetryDelayMs) * time.Millisecond * time.Duration(1<<uint(attempt-1))
			slog.Info("retrying LLM extraction call", "attempt", attempt, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		// Wait on the global rate limiter before making the outbound call
		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create LLM request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("LLM api request failed: %w", err)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBody))
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				continue // retryable errors
			}
			return nil, lastErr // fatal error (e.g. 400 Bad Request)
		}

		// Parse the LLM response
		var apiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, fmt.Errorf("failed to decode LLM response: %w", err)
		}

		if len(apiResp.Candidates) == 0 || len(apiResp.Candidates[0].Content.Parts) == 0 {
			return nil, fmt.Errorf("LLM returned no content")
		}

		textResponse := apiResp.Candidates[0].Content.Parts[0].Text
		var reqs []string
		if err := json.Unmarshal([]byte(textResponse), &reqs); err != nil {
			return nil, fmt.Errorf("failed to decode extracted JSON requirements: %w. Raw text: %s", err, textResponse)
		}

		return reqs, nil
	}

	return nil, lastErr
}

// RequirementStatus holds the Jev verdict passed to the justification LLM
type RequirementStatus struct {
	Requirement string `json:"requirement"`
	Status      string `json:"status"` // matched, missing, unclear
}

// JustifyJevDecisions asks the LLM to justify the strict boolean decisions made by Jev.
func JustifyJevDecisions(ctx context.Context, cfg config.Config, limiter *rate.Limiter, cvText string, statuses []RequirementStatus) (map[string]string, error) {
	if cfg.ExtractionAPIKey == "" {
		return nil, fmt.Errorf("EXTRACTION_API_KEY is not configured")
	}

	statusJSON, _ := json.Marshal(statuses)
	prompt := fmt.Sprintf(`A primary classification AI has evaluated a candidate's CV against job requirements and provided verdicts (matched, missing, unclear).
Your task is to justify these verdicts by finding ONE sentence of evidence from the CV.
If the verdict is 'missing' or 'unclear' and no evidence exists, simply write "No explicit evidence found in the CV."

CV Text:
%s

Verdicts to justify:
%s

Return ONLY a JSON object where the keys are the exact requirement strings, and the values are your 1-sentence evidence justification.`, cvText, string(statusJSON))

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"response_mime_type": "application/json",
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode LLM request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", cfg.ExtractionModel, cfg.ExtractionAPIKey)
	client := &http.Client{Timeout: time.Duration(cfg.APITimeoutSecs) * time.Second}

	var lastErr error
	for attempt := 0; attempt <= cfg.JDExtractionMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(cfg.JDExtractionRetryDelayMs) * time.Millisecond * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create LLM request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBody))
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				continue
			}
			return nil, lastErr
		}

		var apiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, err
		}
		if len(apiResp.Candidates) == 0 || len(apiResp.Candidates[0].Content.Parts) == 0 {
			return nil, fmt.Errorf("LLM returned no content")
		}

		var justifications map[string]string
		if err := json.Unmarshal([]byte(apiResp.Candidates[0].Content.Parts[0].Text), &justifications); err != nil {
			return nil, err
		}

		return justifications, nil
	}

	return nil, lastErr
}
