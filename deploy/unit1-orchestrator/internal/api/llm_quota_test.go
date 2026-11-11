package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLLMQuotaEndpoints(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	// 1. GET initial settings
	reqGet := httptest.NewRequest("GET", "/api/settings/llm-quota", nil)
	wGet := httptest.NewRecorder()
	server.ServeHTTP(wGet, reqGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wGet.Code, wGet.Body.String())
	}

	var initResp map[string]interface{}
	if err := json.NewDecoder(wGet.Body).Decode(&initResp); err != nil {
		t.Fatalf("decode get response failed: %v", err)
	}
	if initResp["max_tpm"].(float64) != 0 || initResp["day_max_concurrency"].(float64) != 2 ||
		initResp["llm_timeout_sec"].(float64) != 600 || initResp["llm_proxy_max_retries"].(float64) != 6 {
		t.Fatalf("unexpected initial settings: %+v", initResp)
	}

	// 2. PUT updated settings including timeout & max retries
	payload := `{"max_tpm": 50000, "max_rpm": 60, "day_max_concurrency": 3, "night_max_concurrency": 6, "llm_timeout_sec": 900, "llm_proxy_max_retries": 8}`
	reqPut := httptest.NewRequest("PUT", "/api/settings/llm-quota", bytes.NewBufferString(payload))
	reqPut.Header.Set("Content-Type", "application/json")
	wPut := httptest.NewRecorder()
	server.ServeHTTP(wPut, reqPut)

	if wPut.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", wPut.Code, wPut.Body.String())
	}

	// Verify limiter and LLMProxy instance updated in real time
	if server.GetLLMProxy() != nil {
		tpm, rpm := server.GetLLMProxy().GetLimiter().GetQuota()
		if tpm != 50000 || rpm != 60 {
			t.Fatalf("expected proxy limiter updated to 50000/60, got %d/%d", tpm, rpm)
		}
		if server.GetLLMProxy().GetTimeout() != 900*time.Second {
			t.Fatalf("expected proxy timeout updated to 900s, got %v", server.GetLLMProxy().GetTimeout())
		}
		if server.GetLLMProxy().GetMaxRetries() != 8 {
			t.Fatalf("expected proxy max retries updated to 8, got %d", server.GetLLMProxy().GetMaxRetries())
		}
	}

	// 3. GET after update
	reqGet2 := httptest.NewRequest("GET", "/api/settings/llm-quota", nil)
	wGet2 := httptest.NewRecorder()
	server.ServeHTTP(wGet2, reqGet2)

	var updatedResp map[string]interface{}
	if err := json.NewDecoder(wGet2.Body).Decode(&updatedResp); err != nil {
		t.Fatalf("decode get response failed: %v", err)
	}
	if updatedResp["max_tpm"].(float64) != 50000 || updatedResp["max_rpm"].(float64) != 60 ||
		updatedResp["day_max_concurrency"].(float64) != 3 || updatedResp["llm_timeout_sec"].(float64) != 900 ||
		updatedResp["llm_proxy_max_retries"].(float64) != 8 {
		t.Fatalf("unexpected updated settings: %+v", updatedResp)
	}

	// 4. Reject invalid timeout (< 10s)
	badReq := httptest.NewRequest("PUT", "/api/settings/llm-quota", bytes.NewBufferString(`{"llm_timeout_sec": 5}`))
	badReq.Header.Set("Content-Type", "application/json")
	wBad := httptest.NewRecorder()
	server.ServeHTTP(wBad, badReq)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid llm_timeout_sec, got %d", wBad.Code)
	}
}
