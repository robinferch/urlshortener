package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "us_session"

type User struct {
	ID          int64            `json:"id"`
	Email       string           `json:"email"`
	Name        string           `json:"name"`
	IsAdmin     bool             `json:"isAdmin"`
	Disabled    bool             `json:"disabled"`
	HasPassword bool             `json:"hasPassword"`
	SSO         bool             `json:"sso"`
	CreatedAt   time.Time        `json:"createdAt"`
	LastLoginAt *time.Time       `json:"lastLoginAt"`
	Roles       map[int64]string `json:"-"` // domain id -> "admin" | "user"
}

const userCols = `id, email, name, is_admin, disabled, password_hash IS NOT NULL, oidc_subject IS NOT NULL, created_at, last_login_at`

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.Disabled, &u.HasPassword, &u.SSO, &u.CreatedAt, &u.LastLoginAt)
	return u, err
}

func (u *User) canUseDomain(id int64) bool    { return u.IsAdmin || u.Roles[id] != "" }
func (u *User) canManageDomain(id int64) bool { return u.IsAdmin || u.Roles[id] == "admin" }
func (u *User) canEditLink(domainID int64, createdBy *int64) bool {
	return u.canManageDomain(domainID) || (u.canUseDomain(domainID) && createdBy != nil && *createdBy == u.ID)
}

// domainsByRole returns the domains the user administers and the ones they are a plain member of.
func (u *User) domainsByRole() (admin, member []int64) {
	admin, member = []int64{}, []int64{}
	for id, role := range u.Roles {
		if role == "admin" {
			admin = append(admin, id)
		} else {
			member = append(member, id)
		}
	}
	return
}

// linkScope is a SQL predicate over alias l limiting links to those the user may see.
// Uses named args @is_admin, @admin_domains, @member_domains, @uid.
const linkScope = `(@is_admin::bool OR l.domain_id = ANY(@admin_domains::bigint[]) OR (l.domain_id = ANY(@member_domains::bigint[]) AND l.created_by = @uid::bigint))`

func (u *User) scopeArgs() pgx.NamedArgs {
	a, m := u.domainsByRole()
	return pgx.NamedArgs{"is_admin": u.IsAdmin, "admin_domains": a, "member_domains": m, "uid": u.ID}
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func hashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", badRequest("password must be at least 8 characters")
	}
	if len(pw) > 72 {
		return "", badRequest("password must be at most 72 bytes")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

func normalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if a, err := mail.ParseAddress(e); err != nil || a.Address != e {
		return "", badRequest("invalid email address")
	}
	return e, nil
}

// currentUser resolves the caller from an API key header or session cookie.
func (s *Server) currentUser(r *http.Request) (u *User, err error) {
	ctx := r.Context()
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		// Only Bearer counts; Basic credentials from a proxy in front of the app are not API keys.
		if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			key = k
		}
	}
	if key != "" {
		u, err = scanUser(s.db.QueryRow(ctx, `SELECT `+prefixCols("u.", userCols)+` FROM api_keys k JOIN users u ON u.id = k.user_id
			WHERE k.key_hash = $1`, hashToken(key)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errUnauthorized
		}
		if err == nil {
			// Throttled so busy keys don't serialise every request on one row lock.
			_, err = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE key_hash = $1
				AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, hashToken(key))
		}
	} else {
		c, cerr := r.Cookie(sessionCookie)
		if cerr != nil {
			return nil, nil
		}
		u, err = scanUser(s.db.QueryRow(ctx, `SELECT `+prefixCols("u.", userCols)+` FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(c.Value)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, errStatus(http.StatusForbidden, "account disabled")
	}
	u.Roles = map[int64]string{}
	rows, _ := s.db.Query(ctx, "SELECT domain_id, role FROM domain_members WHERE user_id = $1", u.ID)
	var id int64
	var role string
	_, err = pgx.ForEachRow(rows, []any{&id, &role}, func() error { u.Roles[id] = role; return nil })
	return u, err
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = prefix + p
	}
	return strings.Join(parts, ", ")
}

// authed requires a logged-in user.
func (s *Server) authed(fn func(http.ResponseWriter, *http.Request, *User) error) http.HandlerFunc {
	return s.h(func(w http.ResponseWriter, r *http.Request) error {
		u, err := s.currentUser(r)
		if err != nil {
			return err
		}
		if u == nil {
			return errUnauthorized
		}
		return fn(w, r, u)
	})
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, userID int64) error {
	token := randomToken(32)
	expires := time.Now().Add(s.cfg.SessionTTL)
	if _, err := s.db.Exec(ctx, "INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)", hashToken(token), userID, expires); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, "UPDATE users SET last_login_at = now() WHERE id = $1", userID); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) error {
	var users int
	if err := s.db.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&users); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.cfg.Version,
		"localAuth":     s.cfg.LocalAuth,
		"oidc":          s.cfg.OIDCEnabled(),
		"oidcName":      s.cfg.OIDCName,
		"setupRequired": users == 0 && s.cfg.LocalAuth,
	})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// handleSetup creates the first instance admin on a fresh install.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.LocalAuth {
		return errForbidden
	}
	if !s.limiter.allow(s.clientIP(r)) {
		return errStatus(http.StatusTooManyRequests, "too many attempts, try again later")
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		return err
	}
	email, err := normalizeEmail(c.Email)
	if err != nil {
		return err
	}
	hash, err := hashPassword(c.Password)
	if err != nil {
		return err
	}
	// The advisory lock serialises concurrent setup calls; NOT EXISTS alone races under READ COMMITTED.
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726202)"); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errStatus(http.StatusConflict, "setup already completed")
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO users (email, name, password_hash, is_admin) VALUES ($1, $2, $3, true) RETURNING id`,
		email, strings.TrimSpace(c.Name), hash).Scan(&id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := s.startSession(r.Context(), w, id); err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) bootstrapAdmin(ctx context.Context) error {
	if s.cfg.AdminEmail == "" || s.cfg.AdminPassword == "" {
		return nil
	}
	email, err := normalizeEmail(s.cfg.AdminEmail)
	if err != nil {
		return errors.New("ADMIN_EMAIL is invalid")
	}
	hash, err := hashPassword(s.cfg.AdminPassword)
	if err != nil {
		return errors.New("ADMIN_PASSWORD: " + err.Error())
	}
	_, err = s.db.Exec(ctx, `INSERT INTO users (email, name, password_hash, is_admin) VALUES ($1, 'Admin', $2, true)
		ON CONFLICT (email) DO NOTHING`, email, hash)
	return err
}

// dummyHash keeps login timing equal for unknown and known emails.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equaliser"), 12)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.LocalAuth {
		return errStatus(http.StatusForbidden, "local login is disabled")
	}
	attempt := s.limiter.take(s.clientIP(r))
	if attempt == nil {
		return errStatus(http.StatusTooManyRequests, "too many attempts, try again later")
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		return err
	}
	var id int64
	var hash *string
	var disabled bool
	err := s.db.QueryRow(r.Context(), "SELECT id, password_hash, disabled FROM users WHERE email = $1",
		strings.ToLower(strings.TrimSpace(c.Email))).Scan(&id, &hash, &disabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if hash == nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password))
		return errStatus(http.StatusUnauthorized, "invalid email or password")
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(c.Password)) != nil {
		return errStatus(http.StatusUnauthorized, "invalid email or password")
	}
	if disabled {
		return errStatus(http.StatusForbidden, "account disabled")
	}
	attempt() // correct password: refund the attempt
	if err := s.startSession(r.Context(), w, id); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.db.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash = $1", hashToken(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.secureCookies(), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type domainRole struct {
	ID     int64  `json:"id"`
	Host   string `json:"host"`
	Scheme string `json:"scheme"`
	Role   string `json:"role"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, _ := s.db.Query(r.Context(), `SELECT d.id, d.host, d.scheme, CASE WHEN $2 THEN 'admin' ELSE m.role END FROM domains d
		LEFT JOIN domain_members m ON m.domain_id = d.id AND m.user_id = $1
		WHERE $2 OR m.user_id IS NOT NULL ORDER BY d.host`, u.ID, u.IsAdmin)
	domains, err := pgx.CollectRows(rows, pgx.RowToStructByPos[domainRole])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"user": u, "domains": domains})
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request, u *User) error {
	var in struct {
		Name            *string `json:"name"`
		CurrentPassword string  `json:"currentPassword"`
		NewPassword     string  `json:"newPassword"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if in.Name != nil {
		if _, err := s.db.Exec(ctx, "UPDATE users SET name = $1 WHERE id = $2", strings.TrimSpace(*in.Name), u.ID); err != nil {
			return err
		}
	}
	if in.NewPassword != "" {
		if !s.cfg.LocalAuth {
			return errStatus(http.StatusForbidden, "local login is disabled")
		}
		// An SSO account gaining a local password would bypass the IdP (MFA, offboarding).
		if u.SSO && !u.HasPassword {
			return errStatus(http.StatusForbidden, "SSO accounts cannot set a local password")
		}
		var hash *string
		if err := s.db.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1", u.ID).Scan(&hash); err != nil {
			return err
		}
		if hash != nil && bcrypt.CompareHashAndPassword([]byte(*hash), []byte(in.CurrentPassword)) != nil {
			return badRequest("current password is incorrect")
		}
		nh, err := hashPassword(in.NewPassword)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", nh, u.ID); err != nil {
			return err
		}
		// Log out other sessions after a password change.
		token := ""
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		if _, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2", u.ID, hashToken(token)); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, _ := s.db.Query(r.Context(), "SELECT id, name, prefix, created_at, last_used_at FROM api_keys WHERE user_id = $1 ORDER BY id", u.ID)
	keys, err := pgx.CollectRows(rows, pgx.RowToStructByPos[apiKey])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, u *User) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return badRequest("name is required (max 100 chars)")
	}
	key := "usk_" + randomToken(24)
	var k apiKey
	err := s.db.QueryRow(r.Context(), `INSERT INTO api_keys (user_id, name, key_hash, prefix) VALUES ($1, $2, $3, $4)
		RETURNING id, name, prefix, created_at, last_used_at`, u.ID, in.Name, hashToken(key), key[:10]).
		Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, map[string]any{"key": key, "apiKey": k})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM api_keys WHERE id = $1 AND user_id = $2", id, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ipLimiter throttles brute-force-prone endpoints per client IP.
type ipLimiter struct {
	mu sync.Mutex
	m  map[string]*limiterEntry
}

// limiterEntry is a token bucket: up to limiterBurst attempts, refilling one per limiterRefill.
type limiterEntry struct {
	tokens float64
	last   time.Time // tokens were last brought up to date
	seen   time.Time
}

const (
	limiterBurst  = 10
	limiterRefill = 6 * time.Second
)

func newIPLimiter(ctx context.Context) *ipLimiter {
	l := &ipLimiter{m: map[string]*limiterEntry{}}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.mu.Lock()
				for k, e := range l.m {
					if time.Since(e.seen) > 10*time.Minute {
						delete(l.m, k)
					}
				}
				l.mu.Unlock()
			}
		}
	}()
	return l
}

// allow permits a burst of 10 attempts, refilling one every 6 seconds.
// shortcut: per-process state, so N replicas allow N× the rate; move to Redis/Postgres if that matters.
func (l *ipLimiter) allow(ip string) bool { return l.take(ip) != nil }

// take spends one attempt for ip, or returns nil if the budget is used up. Callers call the
// returned refund after a successful attempt, so only failures count: legitimate users behind one
// NAT address don't lock each other out, while parallel guessing still can't exceed the budget.
// A plain bucket under our mutex makes refunds exact regardless of how attempts interleave.
func (l *ipLimiter) take(ip string) (refund func()) {
	// IPv6 clients usually control a whole /64, so limit per /64 rather than per address.
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		ip = p.Mask(net.CIDRMask(64, 128)).String()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.m[ip]
	if !ok {
		e = &limiterEntry{tokens: limiterBurst, last: now}
		l.m[ip] = e
	}
	e.seen = now
	e.tokens = min(limiterBurst, e.tokens+float64(now.Sub(e.last))/float64(limiterRefill))
	e.last = now
	if e.tokens < 1 {
		return nil
	}
	e.tokens--
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		e.tokens = min(limiterBurst, e.tokens+1)
	}
}
