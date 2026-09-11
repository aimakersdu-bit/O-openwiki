package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	DBPath          string `json:"db_path"`             // path to SQLite database file

	// OpenWiki provider settings
	OpenwikiProvider string `json:"openwiki_provider"` // e.g. "openai", "gemini"
	OpenwikiModel    string `json:"openwiki_model"`    // e.g. "gpt-4o"
	OpenwikiAPIKey   string `json:"openwiki_api_key"`  // LLM API key

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
		DBPath:           "orchestrator.db",
		MaxConcurrentQA:  5,
		QATimeoutSec:     120,
		QADaemonScript:   "scripts/qa-daemon.js",
		QASocketDir:      os.TempDir(),
		QAIdleTimeoutSec: 7200,
		DefaultLanguage:  "zh-CN",
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
}
