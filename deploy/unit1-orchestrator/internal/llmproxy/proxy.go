package llmproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BuildUsage tracks accumulated token telemetry and rate limit retries for a specific build.
type BuildUsage struct {
	BuildID            string `json:"build_id"`
	PromptTokens       int    `json:"prompt_tokens"`
	CompletionTokens   int    `json:"completion_tokens"`
	TotalTokens        int    `json:"total_tokens"`
	RateLimitedRetries int    `json:"rate_limited_retries"`
}

// LLMProxy is an in-process OpenAI-compatible proxy with dynamic TPM/RPM rate limiting,
// transparent 429 adaptive backoff retries, and token accounting per build.
type LLMProxy struct {
	upstreamURL      string
	realAPIKey       string
	limiter          *TPMRateLimiter
	throttleCallback func(delta int)

	client      *http.Client
	baseBackoff time.Duration
	maxRetries  int

	mu             sync.Mutex
	buildUsage     map[string]*BuildUsage
	consecutive429 int
	total429Count  int
}

// NewLLMProxy creates a new LLMProxy instance.
func NewLLMProxy(upstreamURL, realAPIKey string, limiter *TPMRateLimiter, throttleCallback func(delta int)) *LLMProxy {
	if upstreamURL == "" {
		upstreamURL = "https://api.openai.com/v1"
	}
	if limiter == nil {
		limiter = NewTPMRateLimiter(0, 0)
	}
	return &LLMProxy{
		upstreamURL:      strings.TrimSuffix(upstreamURL, "/"),
		realAPIKey:       realAPIKey,
		limiter:          limiter,
		throttleCallback: throttleCallback,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		baseBackoff: 5 * time.Second,
		maxRetries:  4,
		buildUsage:  make(map[string]*BuildUsage),
	}
}

// SetBaseBackoff sets the base backoff duration for 429 retries (useful for tests).
func (p *LLMProxy) SetBaseBackoff(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.baseBackoff = d
}

// SetMaxRetries configures the maximum retry attempts for 429/503.
func (p *LLMProxy) SetMaxRetries(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxRetries = n
}

// SetTransport sets the HTTP transport (useful for mocking without real network).
func (p *LLMProxy) SetTransport(rt http.RoundTripper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client.Transport = rt
}

// GetLimiter returns the underlying TPMRateLimiter.
func (p *LLMProxy) GetLimiter() *TPMRateLimiter {
	return p.limiter
}

// Get429Count returns the total number of 429 responses observed.
func (p *LLMProxy) Get429Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total429Count
}

func (p *LLMProxy) getOrCreateUsage(buildID string) *BuildUsage {
	if u, exists := p.buildUsage[buildID]; exists {
		return u
	}
	u := &BuildUsage{BuildID: buildID}
	p.buildUsage[buildID] = u
	return u
}

// PopBuildUsage retrieves and deletes the accumulated usage for a build.
func (p *LLMProxy) PopBuildUsage(buildID string) (BuildUsage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u, ok := p.buildUsage[buildID]
	if !ok {
		return BuildUsage{BuildID: buildID}, false
	}
	delete(p.buildUsage, buildID)
	return *u, true
}

// GetBuildUsage inspects usage for a build without removing it.
func (p *LLMProxy) GetBuildUsage(buildID string) (BuildUsage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u, ok := p.buildUsage[buildID]
	if !ok {
		return BuildUsage{BuildID: buildID}, false
	}
	return *u, true
}

// AddTokens adds tokens to a build's telemetry record.
func (p *LLMProxy) AddTokens(buildID string, prompt, completion, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if buildID == "" {
		return
	}
	u := p.getOrCreateUsage(buildID)
	u.PromptTokens += prompt
	u.CompletionTokens += completion
	u.TotalTokens += total
}

func (p *LLMProxy) record429(buildID string) {
	p.mu.Lock()
	p.total429Count++
	p.consecutive429++
	if buildID != "" {
		u := p.getOrCreateUsage(buildID)
		u.RateLimitedRetries++
	}
	consec := p.consecutive429
	p.mu.Unlock()

	if consec >= 3 && p.throttleCallback != nil {
		log.Printf("[LLMProxy] Upstream 429 triggered %d consecutive times; invoking auto-throttle callback", consec)
		p.throttleCallback(-1)
	}
}

func (p *LLMProxy) reset429() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consecutive429 = 0
}

func (p *LLMProxy) computeBackoff(attempt int, header http.Header) time.Duration {
	p.mu.Lock()
	base := p.baseBackoff
	p.mu.Unlock()

	// Check Retry-After header
	if h := header.Get("Retry-After"); h != "" {
		if sec, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	if h := header.Get("x-ratelimit-reset-requests"); h != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(h)); err == nil && d > 0 {
			return d
		}
	}

	backoff := base * (1 << attempt)
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	jitter := time.Duration(rand.Intn(50)) * time.Millisecond
	return backoff + jitter
}

// ServeHTTP handles incoming OpenAI-compatible proxy requests.
func (p *LLMProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse BuildID from Authorization: Bearer ow-build-<buildID>
	authHeader := r.Header.Get("Authorization")
	var buildID string
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if strings.HasPrefix(token, "ow-build-") {
			buildID = strings.TrimPrefix(token, "ow-build-")
		}
	}

	// Read request body
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
			return
		}
	}

	// Token estimation
	estTokens := len(bodyBytes) / 4
	if len(bodyBytes) > 0 && estTokens == 0 {
		estTokens = 1
	}

	// Rate limiter acquire
	if err := p.limiter.Acquire(r.Context(), estTokens); err != nil {
		http.Error(w, fmt.Sprintf("rate limit acquire error: %v", err), http.StatusTooManyRequests)
		return
	}

	// Build target URL
	subPath := strings.TrimPrefix(r.URL.Path, "/internal/llm/v1")
	if !strings.HasPrefix(subPath, "/") && subPath != "" {
		subPath = "/" + subPath
	}
	targetURL := p.upstreamURL + subPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Upstream call with retry loop for 429/503
	var lastResp *http.Response
	maxRetries := p.maxRetries

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
		if err != nil {
			http.Error(w, fmt.Sprintf("create upstream request: %v", err), http.StatusInternalServerError)
			return
		}

		// Copy headers
		for k, vv := range r.Header {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}

		// Inject real upstream API key if provided
		if p.realAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.realAPIKey)
		} else if strings.HasPrefix(authHeader, "Bearer ow-build-") {
			req.Header.Del("Authorization")
		}

		resp, err := p.client.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			http.Error(w, fmt.Sprintf("upstream proxy error: %v", err), http.StatusBadGateway)
			return
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			p.record429(buildID)
			lastResp = resp
			if attempt == maxRetries {
				break
			}
			backoff := p.computeBackoff(attempt, resp.Header)
			resp.Body.Close()

			select {
			case <-r.Context().Done():
				return
			case <-time.After(backoff):
				continue
			}
		}

		// Non-429 response
		p.reset429()
		p.forwardResponse(w, resp, buildID, estTokens)
		return
	}

	// Max retries exceeded on 429
	if lastResp != nil {
		p.forwardResponse(w, lastResp, buildID, estTokens)
	}
}

func (p *LLMProxy) forwardResponse(w http.ResponseWriter, resp *http.Response, buildID string, estTokens int) {
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	// 1. Streaming response (SSE)
	if strings.Contains(contentType, "text/event-stream") {
		p.forwardSSE(w, resp, buildID, estTokens)
		return
	}

	// 2. Non-streaming JSON or other response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read upstream response: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract tokens if JSON
	if strings.Contains(contentType, "application/json") && resp.StatusCode == http.StatusOK {
		var tokenData struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &tokenData); err == nil && tokenData.Usage.TotalTokens > 0 {
			p.AddTokens(buildID, tokenData.Usage.PromptTokens, tokenData.Usage.CompletionTokens, tokenData.Usage.TotalTokens)
			p.limiter.RecordRealUsage(estTokens, tokenData.Usage.TotalTokens)
		}
	}

	// Copy headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (p *LLMProxy) forwardSSE(w http.ResponseWriter, resp *http.Response, buildID string, estTokens int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	for k, vv := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	scanner := bufio.NewScanner(resp.Body)
	var streamTotalTokens int
	var streamPromptTokens int
	var streamCompletionTokens int

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err == nil && chunk.Usage != nil {
				if chunk.Usage.TotalTokens > 0 {
					streamPromptTokens = chunk.Usage.PromptTokens
					streamCompletionTokens = chunk.Usage.CompletionTokens
					streamTotalTokens = chunk.Usage.TotalTokens
				}
			}
		}
	}

	if streamTotalTokens > 0 {
		p.AddTokens(buildID, streamPromptTokens, streamCompletionTokens, streamTotalTokens)
		p.limiter.RecordRealUsage(estTokens, streamTotalTokens)
	}
}
