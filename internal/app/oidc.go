package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const oidcFlowCookie = "us_oidc"

type oidcAuth struct {
	cfg      *Config
	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// init discovers the provider lazily so the app still boots while the IdP is down.
func (o *oidcAuth) init(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.oauth != nil {
		return nil
	}
	p, err := oidc.NewProvider(ctx, o.cfg.OIDCIssuer)
	if err != nil {
		return err
	}
	o.verifier = p.Verifier(&oidc.Config{ClientID: o.cfg.OIDCClientID})
	o.oauth = &oauth2.Config{
		ClientID:     o.cfg.OIDCClientID,
		ClientSecret: o.cfg.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  o.cfg.AppURL.String() + "/api/auth/oidc/callback",
		Scopes:       o.cfg.OIDCScopes,
	}
	return nil
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) error {
	if s.oidc == nil {
		return errNotFound
	}
	if err := s.oidc.init(r.Context()); err != nil {
		slog.Error("oidc discovery failed", "err", err)
		return errStatus(http.StatusBadGateway, "identity provider unavailable")
	}
	state, nonce, verifier := randomToken(16), randomToken(16), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{
		Name: oidcFlowCookie, Value: state + "." + nonce + "." + verifier, Path: "/api/auth/oidc",
		MaxAge: 600, HttpOnly: true, Secure: s.cfg.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
	return nil
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string, err error) {
		if err != nil {
			slog.Warn("oidc login failed", "reason", msg, "err", err)
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
	}
	if s.oidc == nil {
		fail("SSO is not configured", nil)
		return
	}
	ctx := r.Context()
	if err := s.oidc.init(ctx); err != nil {
		fail("identity provider unavailable", err)
		return
	}
	c, err := r.Cookie(oidcFlowCookie)
	http.SetCookie(w, &http.Cookie{Name: oidcFlowCookie, Path: "/api/auth/oidc", MaxAge: -1})
	parts := []string{}
	if err == nil {
		parts = strings.Split(c.Value, ".")
	}
	if len(parts) != 3 || r.URL.Query().Get("state") != parts[0] {
		fail("login session expired, please try again", nil)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("identity provider error: "+e, nil)
		return
	}
	tok, err := s.oidc.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(parts[2]))
	if err != nil {
		fail("could not complete login", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := s.oidc.verifier.Verify(ctx, raw)
	if err != nil || idt.Nonce != parts[1] {
		fail("invalid ID token", err)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail("invalid ID token", err)
		return
	}
	userID, err := s.oidcUser(ctx, idt.Issuer+"|"+idt.Subject, claims)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			fail(ae.msg, nil)
		} else {
			fail("login failed", err)
		}
		return
	}
	if err := s.startSession(ctx, w, userID); err != nil {
		fail("login failed", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func claimString(c map[string]any, k string) string {
	s, _ := c[k].(string)
	return s
}

func claimGroups(c map[string]any, k string) []string {
	switch v := c[k].(type) {
	case []any:
		out := []string{}
		for _, g := range v {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(strings.ReplaceAll(v, ",", " "))
	}
	return nil
}

// oidcUser finds the user for an OIDC identity, linking by verified email or creating one.
func (s *Server) oidcUser(ctx context.Context, subject string, claims map[string]any) (int64, error) {
	email := strings.ToLower(claimString(claims, "email"))
	verified, _ := claims["email_verified"].(bool)
	name := claimString(claims, "name")
	if name == "" {
		name = claimString(claims, "preferred_username")
	}
	var isAdmin *bool
	if s.cfg.OIDCAdminGroup != "" {
		a := slices.Contains(claimGroups(claims, s.cfg.OIDCGroupsClaim), s.cfg.OIDCAdminGroup)
		isAdmin = &a
	}

	var id int64
	var disabled bool
	err := s.db.QueryRow(ctx, "SELECT id, disabled FROM users WHERE oidc_subject = $1", subject).Scan(&id, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		if email == "" {
			return 0, errStatus(http.StatusForbidden, "your identity provider did not share an email address")
		}
		if !verified && !s.cfg.OIDCSkipEmailVerified {
			return 0, errStatus(http.StatusForbidden, "your email address is not verified by the identity provider")
		}
		err = s.db.QueryRow(ctx, "UPDATE users SET oidc_subject = $2 WHERE email = $1 AND oidc_subject IS NULL RETURNING id, disabled",
			email, subject).Scan(&id, &disabled)
		if errors.Is(err, pgx.ErrNoRows) {
			if !s.cfg.OIDCAutoCreate {
				return 0, errStatus(http.StatusForbidden, "no account exists for "+email)
			}
			err = s.db.QueryRow(ctx, "INSERT INTO users (email, name, oidc_subject, is_admin) VALUES ($1, $2, $3, $4) RETURNING id, disabled",
				email, name, subject, isAdmin != nil && *isAdmin).Scan(&id, &disabled)
			if isUniqueViolation(err) {
				return 0, errStatus(http.StatusForbidden, email+" is already linked to another SSO identity")
			}
		}
	}
	if err != nil {
		return 0, err
	}
	if disabled {
		return 0, errStatus(http.StatusForbidden, "account disabled")
	}
	if isAdmin != nil {
		if _, err := s.db.Exec(ctx, "UPDATE users SET is_admin = $2 WHERE id = $1", id, *isAdmin); err != nil {
			return 0, err
		}
	}
	return id, nil
}
