package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	if initResp["max_tpm"].(float64) != 0 || initResp["day_max_concurrency"].(float64) != 2 {
		t.Fatalf("unexpected initial settings: %+v", initResp)
	}

	// 2. PUT updated settings
	payload := `{"max_tpm": 50000, "max_rpm": 60, "day_max_concurrency": 3, "night_max_concurrency": 6}`
	reqPut := httptest.NewRequest("PUT", "/api/settings/llm-quota", bytes.NewBufferString(payload))
	reqPut.Header.Set("Content-Type", "application/json")
	wPut := httptest.NewRecorder()
	server.ServeHTTP(wPut, reqPut)

	if wPut.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", wPut.Code, wPut.Body.String())
	}

	// Verify limiter instance updated
	if server.GetLLMProxy() != nil {
		tpm, rpm := server.GetLLMProxy().GetLimiter().GetQuota()
		if tpm != 50000 || rpm != 60 {
			t.Fatalf("expected proxy limiter updated to 50000/60, got %d/%d", tpm, rpm)
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
	if updatedResp["max_tpm"].(float64) != 50000 || updatedResp["max_rpm"].(float64) != 60 || updatedResp["day_max_concurrency"].(float64) != 3 {
		t.Fatalf("unexpected updated settings: %+v", updatedResp)
	}
}
