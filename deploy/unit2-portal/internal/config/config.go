package config

import (
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port            int    `yaml:"port"`
	DBPath          string `yaml:"db_path"`
	OrchestratorURL string `yaml:"orchestrator_url"` // e.g. http://127.0.0.1:3000
	StaticOutputDir string `yaml:"static_output_dir"` // e.g. ~/.openwiki/static

	// LDAP Configuration
	LDAP struct {
		URL          string `yaml:"url"`           // e.g. ldap://ad.company.com:389 or ldaps://ad.company.com:636
		BaseDN       string `yaml:"base_dn"`       // e.g. DC=company,DC=com
		UserDNFormat string `yaml:"user_dn_format"`// e.g. %s@company.com or CN=%s,OU=Users,DC=company,DC=com
		BindDN       string `yaml:"bind_dn"`       // Optional admin bind for search
		BindPassword string `yaml:"bind_password"` // Optional admin password
		InsecureSkip string `yaml:"insecure_skip_verify"`
	} `yaml:"ldap"`

	SessionTTLHours int `yaml:"session_ttl_hours"`
}

func DefaultConfig() *Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/tmp"
	}
	cfg := &Config{
		Port:            8080,
		DBPath:          "portal.db",
		OrchestratorURL: "http://127.0.0.1:3000",
		StaticOutputDir: filepath.Join(home, ".openwiki", "static"),
		SessionTTLHours: 24,
	}
	cfg.LDAP.URL = "ldap://127.0.0.1:389"
	cfg.LDAP.UserDNFormat = "%s@company.com"
	return cfg
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	defer overrideEnv(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func overrideEnv(cfg *Config) {
	if envPort := os.Getenv("PORTAL_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			cfg.Port = p
		}
	}
	if envDB := os.Getenv("PORTAL_DB_PATH"); envDB != "" {
		cfg.DBPath = envDB
	}
	if envOrch := os.Getenv("ORCHESTRATOR_URL"); envOrch != "" {
		cfg.OrchestratorURL = envOrch
	}
	if envStatic := os.Getenv("STATIC_OUTPUT_DIR"); envStatic != "" {
		cfg.StaticOutputDir = envStatic
	}
	if envLDAP := os.Getenv("LDAP_URL"); envLDAP != "" {
		cfg.LDAP.URL = envLDAP
	}
}
