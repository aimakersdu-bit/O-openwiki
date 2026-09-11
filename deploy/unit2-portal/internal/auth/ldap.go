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
	Role        string `json:"role"` // "admin" or "user"
}

// Authenticate validates user credentials against LDAP / AD server.
func Authenticate(cfg *config.Config, username, password string) (*UserInfo, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	role := "user"
	for _, adminUser := range cfg.Auth.AdminUsers {
		if strings.EqualFold(strings.TrimSpace(adminUser), strings.TrimSpace(username)) {
			role = "admin"
			break
		}
	}

	// Dev / Mock fallback if LDAP URL is set to mock
	if cfg.LDAP.URL == "mock" || strings.HasPrefix(cfg.LDAP.URL, "mock://") {
		return &UserInfo{
			UserID:      username,
			DisplayName: "Dev User (" + username + ")",
			Role:        role,
		}, nil
	}

	ldapURL := cfg.LDAP.URL
	if ldapURL == "" && cfg.LDAP.ServerURI != "" {
		ldapURL = cfg.LDAP.ServerURI
	}

	var conn *ldap.Conn
	var err error

	if strings.HasPrefix(ldapURL, "ldaps://") {
		tlsConfig := &tls.Config{InsecureSkipVerify: cfg.LDAP.InsecureSkip}
		conn, err = ldap.DialURL(ldapURL, ldap.DialWithTLSConfig(tlsConfig))
	} else {
		conn, err = ldap.DialURL(ldapURL)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to LDAP server: %w", err)
	}
	defer conn.Close()

	userDN := ""
	displayName := username

	// Mode 1: Admin Bind + User Search (Enterprise Mode)
	if cfg.LDAP.BindDN != "" && cfg.LDAP.BindPassword != "" {
		if err := conn.Bind(cfg.LDAP.BindDN, cfg.LDAP.BindPassword); err != nil {
			return nil, fmt.Errorf("LDAP admin bind failed: %w", err)
		}

		baseDN := cfg.LDAP.BaseDN
		if baseDN == "" {
			baseDN = cfg.LDAP.UserSearchBase
		}

		filter := cfg.LDAP.UserSearchFilter
		if filter == "" {
			filter = fmt.Sprintf("(sAMAccountName=%s)", ldap.EscapeFilter(username))
		} else {
			// Replace {username} or %s in user_search_filter
			filter = strings.ReplaceAll(filter, "{username}", ldap.EscapeFilter(username))
			if strings.Contains(filter, "%s") {
				filter = fmt.Sprintf(filter, ldap.EscapeFilter(username))
			}
		}

		attrDisplayName := cfg.LDAP.DisplayNameAttribute
		if attrDisplayName == "" {
			attrDisplayName = "displayName"
		}
		attrUsername := cfg.LDAP.UsernameAttribute
		if attrUsername == "" {
			attrUsername = "cn"
		}

		attributes := []string{attrDisplayName, attrUsername, "sAMAccountName", "userPrincipalName", "mail"}

		searchRequest := ldap.NewSearchRequest(
			baseDN,
			ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
			filter,
			attributes,
			nil,
		)

		sr, err := conn.Search(searchRequest)
		if err != nil {
			return nil, fmt.Errorf("LDAP user search failed: %w", err)
		}
		if len(sr.Entries) == 0 {
			return nil, fmt.Errorf("LDAP user not found: %s", username)
		}

		entry := sr.Entries[0]
		userDN = entry.DN

		if dnVal := entry.GetAttributeValue(attrDisplayName); dnVal != "" {
			displayName = dnVal
		} else if cnVal := entry.GetAttributeValue("cn"); cnVal != "" {
			displayName = cnVal
		} else if samVal := entry.GetAttributeValue("sAMAccountName"); samVal != "" {
			displayName = samVal
		}
	} else {
		// Mode 2: Direct User DN Bind (Fallback)
		if cfg.LDAP.UserDNFormat != "" {
			userDN = fmt.Sprintf(cfg.LDAP.UserDNFormat, username)
		} else {
			userDN = username
		}
	}

	// Attempt User Bind to verify user password
	if err := conn.Bind(userDN, password); err != nil {
		return nil, fmt.Errorf("LDAP user authentication failed: %w", err)
	}

	// If display name is still default username and BaseDN is available in Mode 2, attempt search
	if displayName == username && cfg.LDAP.BaseDN != "" && cfg.LDAP.BindDN == "" {
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
		Role:        role,
	}, nil
}
