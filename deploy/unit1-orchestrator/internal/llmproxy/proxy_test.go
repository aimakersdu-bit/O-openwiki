package llmproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDynamicTPMBucket(t *testing.T) {
	limiter := NewTPMRateLimiter(0, 0)

	// Step 1: max_tpm = 0 (unconstrained) should acquire instantly
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := limiter.Acquire(ctx, 5000)
	if err != nil {
		t.Fatalf("expected instant acquire with quota=0, got %v", err)
	}

	curTPM, curRPM, _, _ := limiter.GetStats()
	if curTPM != 5000 || curRPM != 1 {
		t.Fatalf("expected 5000 TPM and 1 RPM, got TPM=%d RPM=%d", curTPM, curRPM)
	}

	// Step 2: Set dynamic quota max_tpm = 1000
	limiter.SetQuota(1000, 0)
	maxTPM, maxRPM := limiter.GetQuota()
	if maxTPM != 1000 || maxRPM != 0 {
		t.Fatalf("expected quota 1000/0, got %d/%d", maxTPM, maxRPM)
	}

	// Since 5000 is already in bucket, an acquire for 100 tokens must block until timeout
	blockedCtx, blockedCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer blockedCancel()

	err = limiter.Acquire(blockedCtx, 100)
	if err == nil {
		t.Fatalf("expected acquire to block and return timeout, but it succeeded")
	}

	// Step 3: Advance time artificially to clear the window
	fixedNow := time.Now().Add(65 * time.Second)
	limiter.SetNowFunc(func() time.Time {
		return fixedNow
	})

	fastCtx, fastCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer fastCancel()

	err = limiter.Acquire(fastCtx, 800)
	if err != nil {
		t.Fatalf("expected acquire to succeed after window cleared, got %v", err)
	}
}

func TestUpstream429RetryAndAutoThrottle(t *testing.T) {
	var callCount int32
	mockRT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		cnt := atomic.AddInt32(&callCount, 1)
		if cnt < 3 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":"rate limit exceeded"}`)),
			}, nil
		}
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"id":"chat-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)),
		}, nil
	})

	var throttledDelta int32
	proxy := NewLLMProxy("http://mock-upstream/v1", "real-secret-key", nil, func(delta int) {
		atomic.StoreInt32(&throttledDelta, int32(delta))
	})
	proxy.SetTransport(mockRT)
	proxy.SetBaseBackoff(2 * time.Millisecond)

	req := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer ow-build-test-build-1")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	usage, ok := proxy.PopBuildUsage("test-build-1")
	if !ok {
		t.Fatalf("expected usage recorded for test-build-1")
	}
	if usage.RateLimitedRetries != 2 {
		t.Fatalf("expected 2 retries on 429, got %d", usage.RateLimitedRetries)
	}
	if usage.TotalTokens != 30 {
		t.Fatalf("expected 30 total tokens, got %d", usage.TotalTokens)
	}

	// Now test 3 consecutive 429s triggering throttle callback
	atomic.StoreInt32(&callCount, 0)
	always429RT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"always 429"}`)),
		}, nil
	})

	proxyFail := NewLLMProxy("http://mock-upstream/v1", "", nil, func(delta int) {
		atomic.StoreInt32(&throttledDelta, int32(delta))
	})
	proxyFail.SetTransport(always429RT)
	proxyFail.SetBaseBackoff(1 * time.Millisecond)
	proxyFail.SetMaxRetries(3) // 4 attempts total (>= 3 consecutive 429s)

	reqFail := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{}`))
	wFail := httptest.NewRecorder()
	proxyFail.ServeHTTP(wFail, reqFail)

	if atomic.LoadInt32(&throttledDelta) != -1 {
		t.Fatalf("expected throttle callback called with -1, got %d", atomic.LoadInt32(&throttledDelta))
	}
}

func TestStreamAndNonStreamUsageAccounting(t *testing.T) {
	mockRT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "stream") {
			header := make(http.Header)
			header.Set("Content-Type", "text/event-stream")
			streamData := "data: {\"choices\":[{\"delta\":{\"content\":\"Chunk1\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140}}\n\n" +
				"data: [DONE]\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(streamData)),
			}, nil
		}

		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":25,"total_tokens":75}}`)),
		}, nil
	})

	proxy := NewLLMProxy("http://mock-upstream/v1", "sk-test", nil, nil)
	proxy.SetTransport(mockRT)

	// 1. Test Non-Stream
	reqNonStream := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{}`))
	reqNonStream.Header.Set("Authorization", "Bearer ow-build-non-stream-build")
	wNonStream := httptest.NewRecorder()
	proxy.ServeHTTP(wNonStream, reqNonStream)

	if wNonStream.Code != http.StatusOK {
		t.Fatalf("non-stream expected 200, got %d", wNonStream.Code)
	}

	uNonStream, ok := proxy.PopBuildUsage("non-stream-build")
	if !ok || uNonStream.TotalTokens != 75 || uNonStream.PromptTokens != 50 || uNonStream.CompletionTokens != 25 {
		t.Fatalf("expected non-stream usage 50/25/75, got %+v", uNonStream)
	}

	// 2. Test Stream SSE
	reqStream := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions/stream", strings.NewReader(`{}`))
	reqStream.Header.Set("Authorization", "Bearer ow-build-stream-build")
	wStream := httptest.NewRecorder()
	proxy.ServeHTTP(wStream, reqStream)

	if wStream.Code != http.StatusOK {
		t.Fatalf("stream expected 200, got %d", wStream.Code)
	}

	uStream, ok := proxy.PopBuildUsage("stream-build")
	if !ok || uStream.TotalTokens != 140 || uStream.PromptTokens != 100 || uStream.CompletionTokens != 40 {
		t.Fatalf("expected stream usage 100/40/140, got %+v", uStream)
	}
}
