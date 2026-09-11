package auth

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/openwiki/portal/internal/config"
)

type UserInfo struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"` // "admin" or "user"
}

// Authenticate validates user credentials against LDAP / AD server matching code-usage-service-go reference implementation.
func Authenticate(cfg *config.Config, username, password string) (*UserInfo, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}

	role := "user"
	for _, adminUser := range cfg.Auth.AdminUsers {
		if strings.EqualFold(strings.TrimSpace(adminUser), username) {
			role = "admin"
			break
		}
	}

	// Dev / Mock fallback if LDAP is disabled or URL is set to mock / none / disabled / empty
	cleanURL := strings.ToLower(strings.TrimSpace(cfg.LDAP.URL))
	if !cfg.LDAP.Enabled || cleanURL == "mock" || cleanURL == "none" || cleanURL == "disabled" || cleanURL == "" || strings.HasPrefix(cleanURL, "mock") {
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

	timeout := 5 * time.Second
	dialer := &net.Dialer{Timeout: timeout}

	if strings.HasPrefix(ldapURL, "ldaps://") {
		tlsConfig := &tls.Config{InsecureSkipVerify: cfg.LDAP.InsecureSkip}
		conn, err = ldap.DialURL(ldapURL, ldap.DialWithTLSConfig(tlsConfig), ldap.DialWithDialer(dialer))
	} else {
		conn, err = ldap.DialURL(ldapURL, ldap.DialWithDialer(dialer))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to LDAP server: %w", err)
	}
	defer conn.Close()

	// Mode 1: Admin Bind + User Search (Matching code-usage-service-go)
	if cfg.LDAP.BindDN != "" && cfg.LDAP.BindPassword != "" {
		if err := conn.Bind(cfg.LDAP.BindDN, cfg.LDAP.BindPassword); err != nil {
			return nil, fmt.Errorf("LDAP admin bind failed: %w", err)
		}

		baseDN := cfg.LDAP.BaseDN
		if baseDN == "" {
			baseDN = cfg.LDAP.UserSearchBase
		}

		entry, err := searchUserCandidate(conn, cfg, baseDN, username)
		if err != nil {
			return nil, fmt.Errorf("LDAP user search error: %w", err)
		}
		if entry == nil {
			return nil, fmt.Errorf("LDAP user not found: %s", username)
		}

		if !userEnabled(entry, cfg.LDAP.UserStatusAttribute) {
			return nil, fmt.Errorf("LDAP user disabled: %s", username)
		}

		if err := conn.Bind(entry.DN, password); err != nil {
			return nil, fmt.Errorf("LDAP user authentication failed: %w", err)
		}

		resolvedUsername := firstAttribute(entry, cfg.LDAP.UsernameAttribute, username)
		displayName := firstAttribute(entry, cfg.LDAP.DisplayNameAttribute, resolvedUsername)

		return &UserInfo{
			UserID:      resolvedUsername,
			DisplayName: displayName,
			Role:        role,
		}, nil
	}

	// Mode 2: Direct User DN Bind
	userDN := username
	if cfg.LDAP.UserDNFormat != "" {
		if strings.Contains(cfg.LDAP.UserDNFormat, "%s") {
			userDN = fmt.Sprintf(cfg.LDAP.UserDNFormat, username)
		} else {
			userDN = cfg.LDAP.UserDNFormat
		}
	}

	if err := conn.Bind(userDN, password); err != nil {
		return nil, fmt.Errorf("LDAP user authentication failed: %w", err)
	}

	return &UserInfo{
		UserID:      username,
		DisplayName: username,
		Role:        role,
	}, nil
}

func searchUserCandidate(conn *ldap.Conn, cfg *config.Config, baseDN string, username string) (*ldap.Entry, error) {
	for _, candidate := range getCandidates(cfg, username) {
		filter := buildFilter(cfg.LDAP.UserSearchFilter, candidate)
		attrDisplayName := cfg.LDAP.DisplayNameAttribute
		if attrDisplayName == "" {
			attrDisplayName = "displayName"
		}
		attrUsername := cfg.LDAP.UsernameAttribute
		if attrUsername == "" {
			attrUsername = "cn"
		}
		attrEmail := cfg.LDAP.EmailAttribute
		if attrEmail == "" {
			attrEmail = "mail"
		}
		attrStatus := cfg.LDAP.UserStatusAttribute
		if attrStatus == "" {
			attrStatus = "userAccountControl"
		}

		attributes := []string{attrUsername, attrEmail, attrDisplayName, attrStatus, "sAMAccountName", "userPrincipalName"}

		request := ldap.NewSearchRequest(
			baseDN,
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			2,
			5,
			false,
			filter,
			attributes,
			nil,
		)
		result, err := conn.Search(request)
		if err != nil {
			return nil, err
		}
		if len(result.Entries) >= 1 {
			return result.Entries[0], nil
		}
	}
	return nil, nil
}

func buildFilter(templateFilter string, candidate string) string {
	escaped := ldap.EscapeFilter(candidate)
	if templateFilter == "" {
		templateFilter = "(&(objectCategory=person)(objectClass=user)(|(userPrincipalName={username})(sAMAccountName={username})))"
	}
	filter := strings.ReplaceAll(templateFilter, "{username}", escaped)
	filter = strings.ReplaceAll(filter, "{{username}}", escaped)
	filter = strings.ReplaceAll(filter, "{email}", escaped)
	filter = strings.ReplaceAll(filter, "{{email}}", escaped)
	if strings.Contains(filter, "%s") {
		filter = fmt.Sprintf(filter, escaped)
	}
	return filter
}

func getCandidates(cfg *config.Config, username string) []string {
	if strings.Contains(username, "@") || !strings.Contains(cfg.LDAP.BindDN, "@") {
		return []string{username}
	}
	domain := strings.TrimSpace(cfg.LDAP.BindDN[strings.LastIndex(cfg.LDAP.BindDN, "@")+1:])
	if domain == "" {
		return []string{username}
	}
	return []string{username, username + "@" + domain}
}

func userEnabled(entry *ldap.Entry, statusAttr string) bool {
	if strings.TrimSpace(statusAttr) == "" {
		statusAttr = "userAccountControl"
	}
	value := entry.GetAttributeValue(statusAttr)
	if value == "" {
		return true
	}
	number, err := strconv.Atoi(value)
	return err != nil || number&2 == 0 // check bit 2 (ACCOUNTDISABLE)
}

func firstAttribute(entry *ldap.Entry, attrName string, fallback string) string {
	if strings.TrimSpace(attrName) == "" {
		return fallback
	}
	if value := entry.GetAttributeValue(attrName); value != "" {
		return value
	}
	return fallback
}
