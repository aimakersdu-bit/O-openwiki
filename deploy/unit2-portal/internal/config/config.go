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
		Enabled              bool   `yaml:"enabled"`
		URL                  string `yaml:"url"`                    // e.g. ldap://ad.company.com:389 or ldaps://ad.company.com:636
		ServerURI            string `yaml:"server_uri"`             // Alias for url
		BaseDN               string `yaml:"base_dn"`                // e.g. DC=company,DC=com
		UserSearchBase       string `yaml:"user_search_base"`       // Alias for base_dn
		UserDNFormat         string `yaml:"user_dn_format"`         // e.g. %s@company.com or CN=%s,OU=Users,DC=company,DC=com
		BindDN               string `yaml:"bind_dn"`                // Admin / Service Account bind DN
		BindPassword         string `yaml:"bind_password"`          // Admin / Service Account bind password
		UserSearchFilter     string `yaml:"user_search_filter"`     // e.g. (&(objectCategory=person)(objectClass=user)(|(userPrincipalName={username})(sAMAccountName={username})))
		UsernameAttribute    string `yaml:"username_attribute"`     // e.g. cn
		EmailAttribute       string `yaml:"email_attribute"`        // e.g. mail
		DisplayNameAttribute string `yaml:"display_name_attribute"` // e.g. displayName
		UserStatusAttribute  string `yaml:"user_status_attribute"`  // e.g. userAccountControl
		InsecureSkip         bool   `yaml:"insecure_skip_verify"`
	} `yaml:"ldap"`

	// RBAC Auth Configuration
	Auth struct {
		AdminUsers []string `yaml:"admin_users"` // Usernames with admin privileges
		AdminGroup string   `yaml:"admin_group"` // LDAP group with admin privileges
	} `yaml:"auth"`

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
	cfg.LDAP.Enabled = true
	cfg.LDAP.URL = "ldap://127.0.0.1:389"
	cfg.LDAP.UserDNFormat = "%s@company.com"
	cfg.Auth.AdminUsers = []string{"admin", "bigc"}
	return cfg
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	defer overrideEnv(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			normalizeLDAPConfig(cfg)
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	normalizeLDAPConfig(cfg)
	return cfg, nil
}

func normalizeLDAPConfig(cfg *Config) {
	if cfg.LDAP.URL == "" && cfg.LDAP.ServerURI != "" {
		cfg.LDAP.URL = cfg.LDAP.ServerURI
	}
	if cfg.LDAP.BaseDN == "" && cfg.LDAP.UserSearchBase != "" {
		cfg.LDAP.BaseDN = cfg.LDAP.UserSearchBase
	}
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
	if envBindDN := os.Getenv("LDAP_BIND_DN"); envBindDN != "" {
		cfg.LDAP.BindDN = envBindDN
	}
	if envBindPass := os.Getenv("LDAP_BIND_PASSWORD"); envBindPass != "" {
		cfg.LDAP.BindPassword = envBindPass
	}
	normalizeLDAPConfig(cfg)
}
