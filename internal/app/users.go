package app

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	rows, _ := s.db.Query(r.Context(), `SELECT `+userCols+` FROM users
		WHERE email ILIKE $1 OR name ILIKE $1 ORDER BY email LIMIT 500`, q)
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*User, error) { return scanUser(row) })
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, users)
}

type userInput struct {
	Email    string  `json:"email"`
	Name     *string `json:"name"`
	Password *string `json:"password"`
	IsAdmin  *bool   `json:"isAdmin"`
	Disabled *bool   `json:"disabled"`
}

// createUser inserts a user. A nil password creates an SSO-only account that
// gets linked on first OIDC login with the same verified email.
func (s *Server) createUser(r *http.Request, in userInput) (*User, error) {
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	var hash *string
	if in.Password != nil && *in.Password != "" {
		h, err := hashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		hash = &h
	} else if !s.cfg.OIDCEnabled() {
		return nil, badRequest("a password is required because SSO is not configured")
	}
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	return scanUser(s.db.QueryRow(r.Context(), `INSERT INTO users (email, name, password_hash, is_admin)
		VALUES ($1, $2, $3, $4) RETURNING `+userCols, email, name, hash, in.IsAdmin != nil && *in.IsAdmin))
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	var in userInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	nu, err := s.createUser(r, in)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, nu)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in userInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if id == u.ID && ((in.IsAdmin != nil && !*in.IsAdmin) || (in.Disabled != nil && *in.Disabled)) {
		return badRequest("you cannot demote or disable yourself")
	}
	var hash *string
	if in.Password != nil && *in.Password != "" {
		h, err := hashPassword(*in.Password)
		if err != nil {
			return err
		}
		hash = &h
	}
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		in.Name = &trimmed
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	nu, err := scanUser(tx.QueryRow(r.Context(), `UPDATE users SET
		name = COALESCE($2, name), password_hash = COALESCE($3, password_hash),
		is_admin = COALESCE($4, is_admin), disabled = COALESCE($5, disabled)
		WHERE id = $1 RETURNING `+userCols, id, in.Name, hash, in.IsAdmin, in.Disabled))
	if err != nil {
		return err
	}
	if nu.Disabled || hash != nil {
		// An admin password reset usually means compromise: revoke API keys as well as sessions.
		if _, err := tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id = $1", id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), "DELETE FROM api_keys WHERE user_id = $1", id); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, nu)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, u *User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if id == u.ID {
		return badRequest("you cannot delete yourself")
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM users WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
