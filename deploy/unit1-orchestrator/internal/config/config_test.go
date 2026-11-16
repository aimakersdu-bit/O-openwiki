package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDotEnvFallback(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	envContent := `
# OpenWiki env file
OPENAI_COMPATIBLE_BASE_URL="http://mock-ai:8000/v1"
OPENAI_COMPATIBLE_API_KEY="sk-mock-key-123"
OPENWIKI_PROVIDER="openai-compatible"
OPENWIKI_MODEL_ID="deepseek-v3"
`
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENWIKI_HOME", tmpDir)

	cfg := &Config{}
	readDotEnvFallback(cfg)

	if cfg.OpenAIUpstreamURL != "http://mock-ai:8000/v1" {
		t.Fatalf("expected upstream URL http://mock-ai:8000/v1, got %q", cfg.OpenAIUpstreamURL)
	}
	if cfg.OpenwikiAPIKey != "sk-mock-key-123" {
		t.Fatalf("expected api key sk-mock-key-123, got %q", cfg.OpenwikiAPIKey)
	}
	if cfg.OpenwikiProvider != "openai-compatible" {
		t.Fatalf("expected provider openai-compatible, got %q", cfg.OpenwikiProvider)
	}
	if cfg.OpenwikiModel != "deepseek-v3" {
		t.Fatalf("expected model deepseek-v3, got %q", cfg.OpenwikiModel)
	}
}

