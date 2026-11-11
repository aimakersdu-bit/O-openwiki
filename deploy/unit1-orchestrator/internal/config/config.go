package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all orchestrator configuration.
type Config struct {
	// Server settings
	ListenAddr string `json:"listen_addr"` // e.g. ":3000"

	// Paths
	OpenwikiCLI     string `json:"openwiki_cli"`      // path to openwiki binary/script
	OpenwikiDistDir string `json:"openwiki_dist_dir"`  // path to openwiki dist/ directory
	StaticOutputDir string `json:"static_output_dir"`  // e.g. "/var/www/openwiki-static"
	VendorAssetsDir string `json:"vendor_assets_dir"`  // path to pre-downloaded vendor/ assets
	ReposBaseDir    string `json:"repos_base_dir"`     // base directory for cloned repositories
	ProjectsBaseDir string `json:"projects_base_dir"`  // base directory for virtual project workspaces
	LogBaseDir      string `json:"log_base_dir"`       // base directory for logs (e.g. /data/disk_log)
	DBPath          string `json:"db_path"`             // path to SQLite database file

	// OpenWiki provider settings
	OpenwikiProvider   string `json:"openwiki_provider"`     // e.g. "openai", "gemini"
	OpenwikiModel      string `json:"openwiki_model"`        // e.g. "gpt-4o"
	OpenwikiAPIKey     string `json:"openwiki_api_key"`      // LLM API key
	OpenAIUpstreamURL  string `json:"openai_upstream_url"`   // Upstream OpenAI-compatible API base URL (e.g. "https://api.openai.com/v1")
	LLMTimeoutSec      int    `json:"llm_timeout_sec"`       // Upstream first-header & CLI LLM request timeout in seconds (default 600 = 10 min)
	LLMProxyMaxRetries int    `json:"llm_proxy_max_retries"` // Max retry attempts in LLMProxy on 429/502/503/504/timeout (default 6)

	// QA settings
	MaxConcurrentQA  int    `json:"max_concurrent_qa"`   // max concurrent chat processes
	QATimeoutSec     int    `json:"qa_timeout_sec"`      // timeout per chat request in seconds
	QADaemonScript   string `json:"qa_daemon_script"`    // path to qa-daemon.js
	QASocketDir      string `json:"qa_socket_dir"`       // directory for Unix Domain Sockets
	QAIdleTimeoutSec int    `json:"qa_idle_timeout_sec"` // idle timeout before worker auto-exits

	// Language settings
	DefaultLanguage string `json:"default_language"` // default language tag for init/update/chat (e.g. "zh-CN")

	// Git Global HTTP Credentials
	GitHTTPUsername string `json:"git_http_username"`
	GitHTTPPassword string `json:"git_http_password"`
	GitHTTPToken    string `json:"git_http_token"`

	// Inter-service HMAC Authentication
	HMACSecret string `json:"hmac_secret"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/tmp"
	}
	return &Config{
		ListenAddr:       ":3000",
		OpenwikiCLI:      "openwiki",
		OpenwikiDistDir:  "",
		StaticOutputDir:  filepath.Join(home, ".openwiki", "static"),
		VendorAssetsDir:  "assets/vendor",
		ReposBaseDir:     filepath.Join(home, ".openwiki", "repos"),
		ProjectsBaseDir:  filepath.Join(home, ".openwiki", "projects"),
		LogBaseDir:       filepath.Join(home, ".openwiki", "disk_log"),
		DBPath:             "orchestrator.db",
		LLMTimeoutSec:      600,
		LLMProxyMaxRetries: 6,
		MaxConcurrentQA:    5,
		QATimeoutSec:     600,
		QADaemonScript:   "scripts/qa-daemon.js",
		QASocketDir:      os.TempDir(),
		QAIdleTimeoutSec: 7200,
		DefaultLanguage:  "zh-CN",
		HMACSecret:       "openwiki-hmac-shared-secret",
	}
}

// Load reads configuration from a JSON file, falling back to defaults.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	defer overrideEnv(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Resolve relative paths against the config file's directory.
	dir := filepath.Dir(path)
	if cfg.DBPath != "" && !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(dir, cfg.DBPath)
	}
	if cfg.VendorAssetsDir != "" && !filepath.IsAbs(cfg.VendorAssetsDir) {
		cfg.VendorAssetsDir = filepath.Join(dir, cfg.VendorAssetsDir)
	}

	return cfg, nil
}

func overrideEnv(cfg *Config) {
	if envAddr := os.Getenv("LISTEN_ADDR"); envAddr != "" {
		cfg.ListenAddr = envAddr
	}
	if envDB := os.Getenv("DB_PATH"); envDB != "" {
		cfg.DBPath = envDB
	}
	if envStatic := os.Getenv("STATIC_OUTPUT_DIR"); envStatic != "" {
		cfg.StaticOutputDir = envStatic
	}
	if envVendor := os.Getenv("VENDOR_ASSETS_DIR"); envVendor != "" {
		cfg.VendorAssetsDir = envVendor
	}
	if envRepos := os.Getenv("REPOS_BASE_DIR"); envRepos != "" {
		cfg.ReposBaseDir = envRepos
	}
	if envLog := os.Getenv("LOG_BASE_DIR"); envLog != "" {
		cfg.LogBaseDir = envLog
	}

	// Git HTTP Credentials Overrides
	if envUser := os.Getenv("GIT_HTTP_USERNAME"); envUser != "" {
		cfg.GitHTTPUsername = envUser
	} else if envUser := os.Getenv("GIT_USERNAME"); envUser != "" {
		cfg.GitHTTPUsername = envUser
	}

	if envPass := os.Getenv("GIT_HTTP_PASSWORD"); envPass != "" {
		cfg.GitHTTPPassword = envPass
	} else if envPass := os.Getenv("GIT_PASSWORD"); envPass != "" {
		cfg.GitHTTPPassword = envPass
	}

	if envToken := os.Getenv("GIT_HTTP_TOKEN"); envToken != "" {
		cfg.GitHTTPToken = envToken
	} else if envToken := os.Getenv("GIT_TOKEN"); envToken != "" {
		cfg.GitHTTPToken = envToken
	}

	if envUpstream := os.Getenv("OPENAI_UPSTREAM_URL"); envUpstream != "" {
		cfg.OpenAIUpstreamURL = envUpstream
	} else if envBase := os.Getenv("OPENAI_BASE_URL"); envBase != "" {
		cfg.OpenAIUpstreamURL = envBase
	} else if envCompatBase := os.Getenv("OPENAI_COMPATIBLE_BASE_URL"); envCompatBase != "" {
		cfg.OpenAIUpstreamURL = envCompatBase
	}
	if cfg.OpenwikiAPIKey == "" {
		if envKey := os.Getenv("OPENAI_API_KEY"); envKey != "" {
			cfg.OpenwikiAPIKey = envKey
		} else if envCompatKey := os.Getenv("OPENAI_COMPATIBLE_API_KEY"); envCompatKey != "" {
			cfg.OpenwikiAPIKey = envCompatKey
		} else if envOwKey := os.Getenv("OPENWIKI_API_KEY"); envOwKey != "" {
			cfg.OpenwikiAPIKey = envOwKey
		}
	}
	if envSecret := os.Getenv("OPENWIKI_HMAC_SECRET"); envSecret != "" {
		cfg.HMACSecret = envSecret
	}
	if envTimeout := os.Getenv("LLM_TIMEOUT_SEC"); envTimeout != "" {
		if sec, err := strconv.Atoi(envTimeout); err == nil && sec > 0 {
			cfg.LLMTimeoutSec = sec
		}
	} else if envOwTimeout := os.Getenv("OPENWIKI_LLM_TIMEOUT_SEC"); envOwTimeout != "" {
		if sec, err := strconv.Atoi(envOwTimeout); err == nil && sec > 0 {
			cfg.LLMTimeoutSec = sec
		}
	}
	if envRetries := os.Getenv("LLM_PROXY_MAX_RETRIES"); envRetries != "" {
		if n, err := strconv.Atoi(envRetries); err == nil && n >= 0 {
			cfg.LLMProxyMaxRetries = n
		}
	}
}
