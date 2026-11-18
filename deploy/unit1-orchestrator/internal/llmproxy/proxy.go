package llmproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultLLMTimeout is the default 10-minute timeout awaiting upstream response headers.
	DefaultLLMTimeout = 600 * time.Second
	// DefaultMaxRetries is the default maximum retry count for retryable upstream failures.
	DefaultMaxRetries = 6
	// DefaultMaxTransportRetries limits retries on network/timeout/502/504 errors to prevent excessive stalling.
	DefaultMaxTransportRetries = 2
	// defaultThrottleWindow is the sliding window duration for counting 429 events.
	defaultThrottleWindow = 60 * time.Second
	// defaultThrottleThreshold is the number of 429s within the window needed to trigger auto-throttle.
	defaultThrottleThreshold = 3
	// defaultThrottleCooldown prevents cascading multiple step-downs from a single concurrent burst.
	defaultThrottleCooldown = 30 * time.Second
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
// transparent 429/502/timeout adaptive backoff retries, and token accounting per build.
type LLMProxy struct {
	upstreamURL      string
	realAPIKey       string
	limiter          *TPMRateLimiter
	throttleCallback func(delta int)

	client                *http.Client
	responseHeaderTimeout time.Duration
	streamIdleTimeout     time.Duration
	baseBackoff           time.Duration
	maxRetries            int
	maxTransportRetries   int
	throttleWindow        time.Duration
	throttleThreshold     int
	throttleCooldown      time.Duration
	nowFunc               func() time.Time

	mu             sync.Mutex
	buildUsage     map[string]*BuildUsage
	consecutive429 int
	events429      []time.Time
	lastThrottleAt time.Time
	total429Count  int
	total502Count  int
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
		upstreamURL:           strings.TrimSuffix(upstreamURL, "/"),
		realAPIKey:            realAPIKey,
		limiter:               limiter,
		throttleCallback:      throttleCallback,
		client:                &http.Client{Timeout: 0},
		responseHeaderTimeout: DefaultLLMTimeout,
		streamIdleTimeout:     180 * time.Second,
		baseBackoff:           5 * time.Second,
		maxRetries:            DefaultMaxRetries,
		maxTransportRetries:   DefaultMaxTransportRetries,
		throttleWindow:        defaultThrottleWindow,
		throttleThreshold:     defaultThrottleThreshold,
		throttleCooldown:      defaultThrottleCooldown,
		nowFunc:               time.Now,
		buildUsage:            make(map[string]*BuildUsage),
	}
}

// SetTimeout sets the per-attempt timeout while awaiting upstream response headers.
// Streaming response bodies are allowed to continue beyond this once headers arrive.
func (p *LLMProxy) SetTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responseHeaderTimeout = d
}

// GetTimeout returns the configured per-attempt response header timeout.
func (p *LLMProxy) GetTimeout() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.responseHeaderTimeout <= 0 {
		return DefaultLLMTimeout
	}
	return p.responseHeaderTimeout
}

// SetStreamIdleTimeout sets the idle timeout for streaming/reading upstream response bodies.
func (p *LLMProxy) SetStreamIdleTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamIdleTimeout = d
}

// GetStreamIdleTimeout returns the configured idle timeout for response body reads (default 180s).
func (p *LLMProxy) GetStreamIdleTimeout() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamIdleTimeout <= 0 {
		return 180 * time.Second
	}
	return p.streamIdleTimeout
}

// SetBaseBackoff sets the base backoff duration for retries (useful for tests).
func (p *LLMProxy) SetBaseBackoff(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.baseBackoff = d
}

// SetMaxRetries configures the maximum retry attempts for retryable upstream failures.
func (p *LLMProxy) SetMaxRetries(n int) {
	if n < 0 {
		n = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxRetries = n
}

// GetMaxRetries returns the configured maximum retry attempts.
func (p *LLMProxy) GetMaxRetries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxRetries
}

// SetMaxTransportRetries configures the maximum retry attempts for 502/504/network/timeout errors.
func (p *LLMProxy) SetMaxTransportRetries(n int) {
	if n < 0 {
		n = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxTransportRetries = n
}

// SetThrottleCooldown overrides the cooldown between consecutive auto-throttle callbacks.
func (p *LLMProxy) SetThrottleCooldown(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.throttleCooldown = d
}

// SetNowFunc overrides the time source (useful for tests).
func (p *LLMProxy) SetNowFunc(fn func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nowFunc = fn
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

// Get429Count returns the total number of 429/503 rate-limit responses observed.
func (p *LLMProxy) Get429Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total429Count
}

// Get502Count returns the total number of upstream transport/502/504 errors observed.
func (p *LLMProxy) Get502Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total502Count
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

func (p *LLMProxy) now() time.Time {
	if p.nowFunc != nil {
		return p.nowFunc()
	}
	return time.Now()
}

func (p *LLMProxy) record429(buildID string) {
	p.mu.Lock()
	now := p.now()
	p.total429Count++
	p.consecutive429++
	if buildID != "" {
		u := p.getOrCreateUsage(buildID)
		u.RateLimitedRetries++
	}

	window := p.throttleWindow
	if window <= 0 {
		window = defaultThrottleWindow
	}
	threshold := p.throttleThreshold
	if threshold <= 0 {
		threshold = defaultThrottleThreshold
	}
	cooldown := p.throttleCooldown
	if cooldown < 0 {
		cooldown = 0
	}

	cutoff := now.Add(-window)
	idx := 0
	for idx < len(p.events429) && p.events429[idx].Before(cutoff) {
		idx++
	}
	if idx > 0 {
		p.events429 = p.events429[idx:]
	}
	p.events429 = append(p.events429, now)

	shouldThrottle := false
	windowCount := len(p.events429)
	if windowCount >= threshold && p.throttleCallback != nil {
		if p.lastThrottleAt.IsZero() || now.Sub(p.lastThrottleAt) >= cooldown {
			shouldThrottle = true
			p.lastThrottleAt = now
			p.events429 = nil
		}
	}
	cb := p.throttleCallback
	p.mu.Unlock()

	if shouldThrottle && cb != nil {
		log.Printf("[LLMProxy] Upstream 429 triggered %d times within %v; invoking auto-throttle callback", windowCount, window)
		cb(-1)
	}
}

func (p *LLMProxy) record502() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total502Count++
}

func (p *LLMProxy) reset429() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Reset only the strict consecutive counter; keep the sliding-window 429 history
	// so interleaved 200/502 responses across concurrent slots cannot suppress throttling.
	p.consecutive429 = 0
}

func (p *LLMProxy) computeBackoff(attempt int, header http.Header) time.Duration {
	p.mu.Lock()
	base := p.baseBackoff
	p.mu.Unlock()

	var backoff time.Duration

	// Check Retry-After header
	if header != nil {
		if h := header.Get("Retry-After"); h != "" {
			if sec, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && sec >= 0 {
				backoff = time.Duration(sec) * time.Second
			}
		}
		if backoff == 0 {
			if h := header.Get("x-ratelimit-reset-requests"); h != "" {
				if d, err := time.ParseDuration(strings.TrimSpace(h)); err == nil && d > 0 {
					backoff = d
				}
			}
		}
	}

	if backoff == 0 {
		backoff = base * (1 << attempt)
	}

	// Always cap backoff to prevent proxy requests from stalling for hours on huge Retry-After
	const maxBackoff = 60 * time.Second
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	jitter := time.Duration(rand.Intn(50)) * time.Millisecond
	return backoff + jitter
}

// idleTimeoutReader wraps an io.ReadCloser and triggers a timeout if no bytes are read
// within idleTimeout duration, aborting hung connections rather than waiting for OS TCP keepalive.
type idleTimeoutReader struct {
	r           io.ReadCloser
	idleTimeout time.Duration
	timer       *time.Timer
	onTimeout   func()
	closed      atomic.Bool
	timedOut    atomic.Bool
}

func newIdleTimeoutReader(r io.ReadCloser, timeout time.Duration, onTimeout func()) *idleTimeoutReader {
	tr := &idleTimeoutReader{
		r:           r,
		idleTimeout: timeout,
		onTimeout:   onTimeout,
	}
	if timeout > 0 {
		tr.timer = time.AfterFunc(timeout, func() {
			tr.timedOut.Store(true)
			if tr.onTimeout != nil {
				tr.onTimeout()
			}
			_ = tr.r.Close()
		})
	}
	return tr
}

func (tr *idleTimeoutReader) Read(p []byte) (n int, err error) {
	n, err = tr.r.Read(p)
	if tr.timer != nil && !tr.closed.Load() {
		if !tr.timer.Stop() {
			select {
			case <-tr.timer.C:
			default:
			}
		}
		if err == nil {
			tr.timer.Reset(tr.idleTimeout)
		}
	}
	if tr.timedOut.Load() && (err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
		return n, fmt.Errorf("idle timeout exceeded (%v elapsed without data from upstream)", tr.idleTimeout)
	}
	return n, err
}

func (tr *idleTimeoutReader) Close() error {
	tr.closed.Store(true)
	if tr.timer != nil {
		tr.timer.Stop()
	}
	return tr.r.Close()
}

func (p *LLMProxy) doAttemptWithHeaderTimeout(parent context.Context, req *http.Request, headerTimeout time.Duration) (*http.Response, context.CancelFunc, bool, error) {
	attemptCtx, cancelAttempt := context.WithCancel(parent)
	var timedOut atomic.Bool
	var timer *time.Timer
	if headerTimeout > 0 {
		timer = time.AfterFunc(headerTimeout, func() {
			timedOut.Store(true)
			cancelAttempt()
		})
	}
	resp, err := p.client.Do(req.WithContext(attemptCtx))
	if timer != nil {
		timer.Stop()
	}
	return resp, cancelAttempt, timedOut.Load(), err
}

func isStreamRequest(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Stream
	}
	return bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
}

func ensureStreamUsageOption(body []byte) []byte {
	if len(body) == 0 || !isStreamRequest(body) {
		return body
	}
	if bytes.Contains(body, []byte(`"include_usage"`)) {
		return body
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	opts, ok := raw["stream_options"].(map[string]interface{})
	if !ok {
		opts = make(map[string]interface{})
	}
	opts["include_usage"] = true
	raw["stream_options"] = opts
	if modified, err := json.Marshal(raw); err == nil {
		return modified
	}
	return body
}

// ServeHTTP handles incoming OpenAI-compatible proxy requests.
func (p *LLMProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqID := fmt.Sprintf("%06d", rand.Intn(1000000))

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
			log.Printf("[LLMProxy][%s][build %s] Error reading client request body: %v", reqID, buildID, err)
			http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
			return
		}
	}
	bodyBytes = ensureStreamUsageOption(bodyBytes)
	isStream := isStreamRequest(bodyBytes)

	// Token estimation
	estTokens := len(bodyBytes) / 4
	if len(bodyBytes) > 0 && estTokens == 0 {
		estTokens = 1
	}

	// Build target URL (and extract optional /build/<buildID>/ prefix from path)
	subPath := strings.TrimPrefix(r.URL.Path, "/internal/llm/v1")
	if strings.HasPrefix(subPath, "/build/") {
		rest := strings.TrimPrefix(subPath, "/build/")
		parts := strings.SplitN(rest, "/", 2)
		if parts[0] != "" && buildID == "" {
			buildID = parts[0]
		}
		if len(parts) > 1 {
			subPath = "/" + parts[1]
		} else {
			subPath = ""
		}
	}
	if !strings.HasPrefix(subPath, "/") && subPath != "" {
		subPath = "/" + subPath
	}
	targetURL := p.upstreamURL + subPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	log.Printf("[LLMProxy][%s][build %s] --> %s %s (body=%d bytes, est_tokens=%d, stream=%v)",
		reqID, buildID, r.Method, subPath, len(bodyBytes), estTokens, isStream)

	// Rate limiter acquire
	acqStart := time.Now()
	if err := p.limiter.Acquire(r.Context(), estTokens); err != nil {
		log.Printf("[LLMProxy][%s][build %s] Rate limit acquire rejected after %v: %v",
			reqID, buildID, time.Since(acqStart), err)
		http.Error(w, fmt.Sprintf("rate limit acquire error: %v", err), http.StatusTooManyRequests)
		return
	}
	if time.Since(acqStart) > 50*time.Millisecond {
		log.Printf("[LLMProxy][%s][build %s] Rate limit acquired after waiting %v",
			reqID, buildID, time.Since(acqStart))
	}

	p.mu.Lock()
	maxRetries := p.maxRetries
	maxTransportRetries := p.maxTransportRetries
	headerTimeout := p.responseHeaderTimeout
	p.mu.Unlock()
	if headerTimeout <= 0 {
		headerTimeout = DefaultLLMTimeout
	}

	var lastErr error
	var lastTimedOut bool
	transportRetries := 0

	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptStart := time.Now()
		req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
		if err != nil {
			log.Printf("[LLMProxy][%s][build %s] Failed to create upstream request for %s: %v", reqID, buildID, targetURL, err)
			http.Error(w, fmt.Sprintf("create upstream request: %v", err), http.StatusInternalServerError)
			return
		}

		// Copy headers, excluding hop-by-hop and compression headers that break streaming
		for k, vv := range r.Header {
			if strings.EqualFold(k, "Accept-Encoding") || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		if isStream {
			req.Header.Set("Accept", "text/event-stream")
		}

		// Inject real upstream API key if provided
		if p.realAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.realAPIKey)
		} else if strings.HasPrefix(authHeader, "Bearer ow-build-") {
			req.Header.Del("Authorization")
		}

		log.Printf("[LLMProxy][%s][build %s] Upstream attempt %d/%d --> %s (stream=%v, header_timeout=%v)",
			reqID, buildID, attempt+1, maxRetries+1, targetURL, isStream, headerTimeout)

		resp, cancelAttempt, timedOut, err := p.doAttemptWithHeaderTimeout(r.Context(), req, headerTimeout)
		if err != nil {
			cancelAttempt()
			ttfb := time.Since(attemptStart)
			if r.Context().Err() != nil {
				log.Printf("[LLMProxy][%s][build %s] Client context cancelled after %v on attempt %d/%d: %v",
					reqID, buildID, ttfb, attempt+1, maxRetries+1, r.Context().Err())
				return
			}
			p.record502()
			lastErr = err
			lastTimedOut = timedOut
			if attempt < maxRetries && transportRetries < maxTransportRetries {
				transportRetries++
				backoff := p.computeBackoff(attempt, nil)
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
				log.Printf("[LLMProxy][%s][build %s] Upstream transport error on attempt %d/%d after %v (timedOut=%v): %v; retrying in %v",
					reqID, buildID, attempt+1, maxRetries+1, ttfb, timedOut, err, backoff)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(backoff):
					continue
				}
			}
			log.Printf("[LLMProxy][%s][build %s] Upstream transport failed permanently on attempt %d/%d after %v (timedOut=%v): %v",
				reqID, buildID, attempt+1, maxRetries+1, ttfb, timedOut, err)
			break
		}

		ttfb := time.Since(attemptStart)
		contentType := resp.Header.Get("Content-Type")
		log.Printf("[LLMProxy][%s][build %s] <-- Upstream responded in %v: HTTP %d (contentType=%q, attempt %d/%d)",
			reqID, buildID, ttfb, resp.StatusCode, contentType, attempt+1, maxRetries+1)

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			p.record429(buildID)
			if attempt == maxRetries {
				log.Printf("[LLMProxy][%s][build %s] Reached max retries (%d) on HTTP %d; forwarding error to client",
					reqID, buildID, maxRetries, resp.StatusCode)
				defer cancelAttempt()
				p.forwardResponse(w, resp, buildID, estTokens, reqID)
				return
			}
			backoff := p.computeBackoff(attempt, resp.Header)
			log.Printf("[LLMProxy][%s][build %s] Upstream returned HTTP %d on attempt %d/%d; sleeping %v before retry (Retry-After=%q)",
				reqID, buildID, resp.StatusCode, attempt+1, maxRetries+1, backoff, resp.Header.Get("Retry-After"))
			resp.Body.Close()
			cancelAttempt()

			select {
			case <-r.Context().Done():
				return
			case <-time.After(backoff):
				continue
			}
		}

		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout {
			p.record502()
			if attempt < maxRetries && transportRetries < maxTransportRetries {
				transportRetries++
				backoff := p.computeBackoff(attempt, resp.Header)
				if backoff > 10*time.Second {
					backoff = 10 * time.Second
				}
				log.Printf("[LLMProxy][%s][build %s] Upstream returned HTTP %d on attempt %d/%d; retrying in %v",
					reqID, buildID, resp.StatusCode, attempt+1, maxRetries+1, backoff)
				resp.Body.Close()
				cancelAttempt()

				select {
				case <-r.Context().Done():
					return
				case <-time.After(backoff):
					continue
				}
			}
			log.Printf("[LLMProxy][%s][build %s] Reached max transport retries on HTTP %d; forwarding error to client",
				reqID, buildID, resp.StatusCode)
			defer cancelAttempt()
			p.forwardResponse(w, resp, buildID, estTokens, reqID)
			return
		}

		// Normal non-retryable response (200 OK, or client/server error 400/401/403/404/500)
		p.reset429()
		defer cancelAttempt()
		p.forwardResponse(w, resp, buildID, estTokens, reqID)
		return
	}

	if lastTimedOut || errors.Is(lastErr, context.DeadlineExceeded) {
		log.Printf("[LLMProxy][%s][build %s] Upstream gateway timeout after %v: %v", reqID, buildID, headerTimeout, lastErr)
		http.Error(w, fmt.Sprintf("upstream gateway timeout after %v: %v", headerTimeout, lastErr), http.StatusGatewayTimeout)
		return
	}
	log.Printf("[LLMProxy][%s][build %s] Upstream proxy error: %v", reqID, buildID, lastErr)
	http.Error(w, fmt.Sprintf("upstream proxy error: %v", lastErr), http.StatusBadGateway)
}

func (p *LLMProxy) forwardResponse(w http.ResponseWriter, resp *http.Response, buildID string, estTokens int, reqID string) {
	defer resp.Body.Close()

	start := time.Now()
	contentType := resp.Header.Get("Content-Type")

	// 1. Streaming response (SSE): 仅当 200 OK 且明确为 text/event-stream 时走流式通道
	if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		p.forwardSSE(w, resp, buildID, estTokens, reqID)
		return
	}

	// 2. Non-streaming JSON, error status (400/401/429/500), or other response:
	// 使用 idleTimeoutReader 防止私有环境网络挂起无休止等待 TCP keepalive (2小时)
	idleTimeout := p.GetStreamIdleTimeout()
	tr := newIdleTimeoutReader(resp.Body, idleTimeout, func() {
		log.Printf("[LLMProxy][%s][build %s] Upstream response body read stalled for %v; aborting",
			reqID, buildID, idleTimeout)
	})
	defer tr.Close()

	body, err := io.ReadAll(tr)
	if err != nil {
		log.Printf("[LLMProxy][%s][build %s] Error reading upstream response body: %v", reqID, buildID, err)
		http.Error(w, fmt.Sprintf("read upstream response: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract tokens if JSON and 200 OK
	var tokenData struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if strings.Contains(contentType, "application/json") && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &tokenData); err == nil && tokenData.Usage.TotalTokens > 0 {
			p.AddTokens(buildID, tokenData.Usage.PromptTokens, tokenData.Usage.CompletionTokens, tokenData.Usage.TotalTokens)
			p.limiter.RecordRealUsage(estTokens, tokenData.Usage.TotalTokens)
		}
	}

	if resp.StatusCode == http.StatusOK {
		log.Printf("[LLMProxy][%s][build %s] Non-streaming 200 OK completed in %v (body=%d bytes, tokens=%d)",
			reqID, buildID, time.Since(start), len(body), tokenData.Usage.TotalTokens)
	} else {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 300 {
			preview = preview[:300] + "...(truncated)"
		}
		log.Printf("[LLMProxy][%s][build %s] Upstream returned non-200 HTTP %d (%v): %s",
			reqID, buildID, resp.StatusCode, time.Since(start), preview)
	}

	// Copy headers
	for k, vv := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (p *LLMProxy) forwardSSE(w http.ResponseWriter, resp *http.Response, buildID string, estTokens int, reqID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	for k, vv := range resp.Header {
		if strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Content-Encoding") ||
			strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	// 使用 idleTimeoutReader 防止私有环境网络在流式生成中途静默中断挂死 2 小时
	idleTimeout := p.GetStreamIdleTimeout()
	tr := newIdleTimeoutReader(resp.Body, idleTimeout, func() {
		log.Printf("[LLMProxy][%s][build %s] Upstream SSE stream idle timeout (%v without data); aborting stream",
			reqID, buildID, idleTimeout)
	})
	defer tr.Close()

	scanner := bufio.NewScanner(tr)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var streamTotalTokens int
	var streamPromptTokens int
	var streamCompletionTokens int

	streamStart := time.Now()
	var firstTokenAt time.Time
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		if firstTokenAt.IsZero() && strings.TrimSpace(line) != "" {
			firstTokenAt = time.Now()
			log.Printf("[LLMProxy][%s][build %s] SSE first chunk received in %v (TTFT)", reqID, buildID, firstTokenAt.Sub(streamStart))
		}
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			// Performance optimization: skip JSON unmarshal if line doesn't contain usage keywords
			if !strings.Contains(payload, "usage") {
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

	streamDuration := time.Since(streamStart)
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		log.Printf("[LLMProxy][%s][build %s] SSE stream closed with error after %v (%d lines received): %v",
			reqID, buildID, streamDuration, lineCount, err)
	} else {
		log.Printf("[LLMProxy][%s][build %s] SSE stream completed in %v (%d lines): prompt=%d, completion=%d, total=%d",
			reqID, buildID, streamDuration, lineCount, streamPromptTokens, streamCompletionTokens, streamTotalTokens)
	}

	if streamTotalTokens > 0 {
		p.AddTokens(buildID, streamPromptTokens, streamCompletionTokens, streamTotalTokens)
		p.limiter.RecordRealUsage(estTokens, streamTotalTokens)
	}
}
