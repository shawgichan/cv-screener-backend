# CV Screener — Backend

High-performance Go backend for the Batch CV Screener tool.

This service receives extracted plain-text CVs and Job Descriptions from the frontend, formats them into strictly typed schemas, and concurrently evaluates them against TypeSafe AI APIs.

## Key Features
- **Data Normalization Pipeline**: Uses a small, fast LLM to parse and extract strict technical requirements from messy Job Descriptions before grading.
- **Concurrency**: Uses `errgroup` to process up to 10 CVs simultaneously, drastically reducing batch evaluation time.
- **Global Throttling**: Implements an app-wide Token Bucket rate limiter (`golang.org/x/time/rate`) to space out outbound AI requests and prevent rate-limit 429 errors during concurrent bursts.
- **Provider Agnostic**: Configurable `PROVIDER_ID` supports both the official TypeSafe AI endpoints and reseller endpoints.
- **Resilience**: Built-in exponential backoff retries for handling upstream errors and network timeouts.

## Configuration
All application logic is driven by environment variables (no magic values). See `.env.example` for all configurable limits, scoring bounds, CORS settings, and server timeouts.

## Commands (Makefile)
- `make build` - Compiles the binary
- `make test` - Runs unit tests
- `make run` - Runs the API locally on port 8083
- `make lint` - Runs `go vet`
