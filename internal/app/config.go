package app

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL   string
	ListenAddr    string
	AppURL        *url.URL
	TrustProxy    bool
	ProxyHops     int
	GeoIPDB       string
	LocalAuth     bool
	AdminEmail    string
	AdminPassword string
	SessionTTL    time.Duration
	Version       string

	OIDCIssuer            string
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCName              string
	OIDCScopes            []string
	OIDCGroupsClaim       string
	OIDCAdminGroup        string
	OIDCAutoCreate        bool
	OIDCSkipEmailVerified bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// LoadConfig reads configuration from the environment.
func LoadConfig() (*Config, error) {
	c := &Config{
		DatabaseURL:           env("DATABASE_URL", ""),
		ListenAddr:            env("LISTEN_ADDR", ":8080"),
		TrustProxy:            envBool("TRUST_PROXY", false),
		GeoIPDB:               env("GEOIP_DB", ""),
		LocalAuth:             envBool("LOCAL_AUTH", true),
		AdminEmail:            env("ADMIN_EMAIL", ""),
		AdminPassword:         env("ADMIN_PASSWORD", ""),
		OIDCIssuer:            env("OIDC_ISSUER", ""),
		OIDCClientID:          env("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:      env("OIDC_CLIENT_SECRET", ""),
		OIDCName:              env("OIDC_NAME", "SSO"),
		OIDCScopes:            strings.Fields(env("OIDC_SCOPES", "openid email profile")),
		OIDCGroupsClaim:       env("OIDC_GROUPS_CLAIM", "groups"),
		OIDCAdminGroup:        env("OIDC_ADMIN_GROUP", ""),
		OIDCAutoCreate:        envBool("OIDC_AUTO_CREATE", true),
		OIDCSkipEmailVerified: envBool("OIDC_SKIP_EMAIL_VERIFIED", false),
	}
	c.ProxyHops, _ = strconv.Atoi(env("PROXY_HOPS", "1"))
	if c.ProxyHops < 1 {
		return nil, errors.New("PROXY_HOPS must be >= 1")
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	u, err := url.Parse(strings.TrimSuffix(env("APP_URL", "http://localhost:8080"), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("APP_URL must be an absolute http(s) URL")
	}
	c.AppURL = u
	ttl, err := time.ParseDuration(env("SESSION_TTL", "720h"))
	if err != nil {
		return nil, errors.New("SESSION_TTL: " + err.Error())
	}
	c.SessionTTL = ttl
	if !c.LocalAuth && !c.OIDCEnabled() {
		return nil, errors.New("LOCAL_AUTH=false requires OIDC to be configured")
	}
	return c, nil
}

func (c *Config) OIDCEnabled() bool { return c.OIDCIssuer != "" && c.OIDCClientID != "" }

func (c *Config) secureCookies() bool { return c.AppURL.Scheme == "https" }
