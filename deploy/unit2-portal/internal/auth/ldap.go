package auth

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"
	"github.com/openwiki/portal/internal/config"
)

type UserInfo struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// Authenticate validates user credentials against LDAP / AD server.
func Authenticate(cfg *config.Config, username, password string) (*UserInfo, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	// Dev / Mock fallback if LDAP URL is set to mock
	if cfg.LDAP.URL == "mock" || strings.HasPrefix(cfg.LDAP.URL, "mock://") {
		return &UserInfo{
			UserID:      username,
			DisplayName: "Dev User (" + username + ")",
		}, nil
	}

	var conn *ldap.Conn
	var err error

	if strings.HasPrefix(cfg.LDAP.URL, "ldaps://") {
		tlsConfig := &tls.Config{InsecureSkipVerify: true}
		conn, err = ldap.DialURL(cfg.LDAP.URL, ldap.DialWithTLSConfig(tlsConfig))
	} else {
		conn, err = ldap.DialURL(cfg.LDAP.URL)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to LDAP server: %w", err)
	}
	defer conn.Close()

	// Form user principal name / bind DN
	bindDN := fmt.Sprintf(cfg.LDAP.UserDNFormat, username)

	// Attempt User Bind
	if err := conn.Bind(bindDN, password); err != nil {
		return nil, fmt.Errorf("LDAP authentication failed: %w", err)
	}

	displayName := username
	// Search for display name if BaseDN is configured
	if cfg.LDAP.BaseDN != "" {
		searchRequest := ldap.NewSearchRequest(
			cfg.LDAP.BaseDN,
			ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
			fmt.Sprintf("(sAMAccountName=%s)", ldap.EscapeFilter(username)),
			[]string{"displayName", "sAMAccountName", "cn"},
			nil,
		)

		sr, err := conn.Search(searchRequest)
		if err == nil && len(sr.Entries) > 0 {
			if dn := sr.Entries[0].GetAttributeValue("displayName"); dn != "" {
				displayName = dn
			} else if cn := sr.Entries[0].GetAttributeValue("cn"); cn != "" {
				displayName = cn
			}
		}
	}

	return &UserInfo{
		UserID:      username,
		DisplayName: displayName,
	}, nil
}
