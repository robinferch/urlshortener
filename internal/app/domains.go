package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Domain struct {
	ID          int64     `json:"id"`
	Host        string    `json:"host"`
	Scheme      string    `json:"scheme"`
	FallbackURL string    `json:"fallbackUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	Links       int64     `json:"links"`
	Role        string    `json:"role"`
}

func validateHost(h string) (string, error) {
	h = normalizeHost(h)
	u, err := url.Parse("http://" + h)
	if h == "" || err != nil || u.Host != h || strings.ContainsAny(h, "/?#@ ") {
		return "", badRequest("invalid host, use e.g. go.example.com")
	}
	for _, c := range h {
		if c > 127 { // browsers send IDNs as punycode in the Host header
			return "", badRequest("enter internationalised domains in punycode form (xn--…)")
		}
	}
	return h, nil
}

// validateURL accepts absolute http(s) URLs only; other schemes (javascript:, data:) are refused.
func validateURL(raw string, field string, optional bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && optional {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 8192 {
		return "", badRequest(field + " must be an absolute http(s) URL")
	}
	return raw, nil
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, _ := s.db.Query(r.Context(), `SELECT d.id, d.host, d.scheme, d.fallback_url, d.created_at,
			(SELECT count(*) FROM links WHERE domain_id = d.id),
			CASE WHEN $2 THEN 'admin' ELSE m.role END
		FROM domains d LEFT JOIN domain_members m ON m.domain_id = d.id AND m.user_id = $1
		WHERE $2 OR m.user_id IS NOT NULL ORDER BY d.host`, u.ID, u.IsAdmin)
	ds, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Domain])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, ds)
}

type domainInput struct {
	Host        string `json:"host"`
	Scheme      string `json:"scheme"`
	FallbackURL string `json:"fallbackUrl"`
}

func (in *domainInput) validate() error {
	if in.Scheme == "" {
		in.Scheme = "https"
	}
	if in.Scheme != "http" && in.Scheme != "https" {
		return badRequest("scheme must be http or https")
	}
	var err error
	in.FallbackURL, err = validateURL(in.FallbackURL, "fallback URL", true)
	return err
}

func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	var in domainInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	host, err := validateHost(in.Host)
	if err != nil {
		return err
	}
	if host == s.appHost {
		return badRequest("the app host cannot also be a short domain")
	}
	if err := in.validate(); err != nil {
		return err
	}
	var d Domain
	err = s.db.QueryRow(r.Context(), `INSERT INTO domains (host, scheme, fallback_url) VALUES ($1, $2, $3)
		RETURNING id, host, scheme, fallback_url, created_at`, host, in.Scheme, in.FallbackURL).
		Scan(&d.ID, &d.Host, &d.Scheme, &d.FallbackURL, &d.CreatedAt)
	if err != nil {
		return err
	}
	d.Role = "admin"
	s.cache.invalidate(r.Context(), s.db, "d:"+host)
	return writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateDomain(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if !u.canManageDomain(id) {
		return errForbidden
	}
	var in domainInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	var d Domain
	err = s.db.QueryRow(r.Context(), `UPDATE domains SET scheme = $2, fallback_url = $3 WHERE id = $1
		RETURNING id, host, scheme, fallback_url, created_at`, id, in.Scheme, in.FallbackURL).
		Scan(&d.ID, &d.Host, &d.Scheme, &d.FallbackURL, &d.CreatedAt)
	if err != nil {
		return err
	}
	s.cache.invalidate(r.Context(), s.db, "d:"+d.Host)
	return writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var host string
	if err := s.db.QueryRow(r.Context(), "DELETE FROM domains WHERE id = $1 RETURNING host", id).Scan(&host); err != nil {
		return err
	}
	s.cache.invalidate(r.Context(), s.db, "d:"+host, "*")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type member struct {
	UserID int64  `json:"userId"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if !u.canManageDomain(id) {
		return errForbidden
	}
	rows, _ := s.db.Query(r.Context(), `SELECT u.id, u.email, u.name, m.role FROM domain_members m
		JOIN users u ON u.id = m.user_id WHERE m.domain_id = $1 ORDER BY u.email`, id)
	ms, err := pgx.CollectRows(rows, pgx.RowToStructByPos[member])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, ms)
}

// handlePutMember adds a user to a domain (creating the account if needed) or changes their role.
func (s *Server) handlePutMember(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if !u.canManageDomain(id) {
		return errForbidden
	}
	var in struct {
		Email    string  `json:"email"`
		Role     string  `json:"role"`
		Name     *string `json:"name"`
		Password *string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Role != "admin" && in.Role != "user" {
		return badRequest("role must be admin or user")
	}
	// A password chosen by a domain admin would survive later SSO linking of that email,
	// letting them sign in as the real user. Only instance admins may set one.
	if in.Password != nil && *in.Password != "" && !u.IsAdmin {
		return errStatus(http.StatusForbidden, "only instance admins can set passwords; the user can sign in via SSO")
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return err
	}
	var userID int64
	created := false
	err = s.db.QueryRow(r.Context(), "SELECT id FROM users WHERE email = $1", email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		nu, err := s.createUser(r, userInput{Email: email, Name: in.Name, Password: in.Password})
		if err != nil {
			return err
		}
		userID, created = nu.ID, true
	} else if err != nil {
		return err
	} else if in.Password != nil && *in.Password != "" {
		return errStatus(http.StatusConflict, "this user already exists; change their password on the Users page")
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO domain_members (domain_id, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (domain_id, user_id) DO UPDATE SET role = EXCLUDED.role`, id, userID, in.Role); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"userId": userID, "created": created})
}

func (s *Server) handleDeleteMember(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	userID, err := pathID(r, "userId")
	if err != nil {
		return err
	}
	if !u.canManageDomain(id) {
		return errForbidden
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM domain_members WHERE domain_id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
