package app

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/skip2/go-qrcode"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

type Link struct {
	ID           int64      `json:"id"`
	DomainID     int64      `json:"domainId"`
	Domain       string     `json:"domain"`
	Slug         string     `json:"slug"`
	ShortURL     string     `json:"shortUrl"`
	TargetURL    string     `json:"targetUrl"`
	IOSURL       string     `json:"iosUrl"`
	AndroidURL   string     `json:"androidUrl"`
	Title        string     `json:"title"`
	Tags         []string   `json:"tags"`
	HasPassword  bool       `json:"hasPassword"`
	ValidSince   *time.Time `json:"validSince"`
	ValidUntil   *time.Time `json:"validUntil"`
	MaxVisits    *int32     `json:"maxVisits"`
	ForwardQuery bool       `json:"forwardQuery"`
	Enabled      bool       `json:"enabled"`
	Visits       int64      `json:"visits"`
	LimitUsed    int64      `json:"limitUsed"` // human visits + bot hits counted against maxVisits
	CreatedBy    *int64     `json:"createdBy"`
	CreatorEmail *string    `json:"creatorEmail"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

const linkSelect = `SELECT l.id, l.domain_id, d.host, d.scheme, l.slug, l.target_url, l.ios_url, l.android_url, l.title, l.tags,
	l.password_hash IS NOT NULL, l.valid_since, l.valid_until, l.max_visits, l.forward_query, l.enabled, l.visit_count,
	l.visit_count + l.capped_bot_hits,
	l.created_by, u.email, l.created_at, l.updated_at
	FROM links l JOIN domains d ON d.id = l.domain_id LEFT JOIN users u ON u.id = l.created_by`

func scanLink(row pgx.Row) (*Link, error) {
	l := &Link{}
	var scheme string
	err := row.Scan(&l.ID, &l.DomainID, &l.Domain, &scheme, &l.Slug, &l.TargetURL, &l.IOSURL, &l.AndroidURL, &l.Title, &l.Tags,
		&l.HasPassword, &l.ValidSince, &l.ValidUntil, &l.MaxVisits, &l.ForwardQuery, &l.Enabled, &l.Visits, &l.LimitUsed,
		&l.CreatedBy, &l.CreatorEmail, &l.CreatedAt, &l.UpdatedAt)
	l.ShortURL = scheme + "://" + l.Domain + "/" + l.Slug
	return l, err
}

type linkInput struct {
	DomainID     int64      `json:"domainId"`
	Slug         string     `json:"slug"`
	TargetURL    string     `json:"targetUrl"`
	IOSURL       string     `json:"iosUrl"`
	AndroidURL   string     `json:"androidUrl"`
	Title        string     `json:"title"`
	Tags         []string   `json:"tags"`
	Password     *string    `json:"password"` // nil keeps the current password, "" removes it
	ValidSince   *time.Time `json:"validSince"`
	ValidUntil   *time.Time `json:"validUntil"`
	MaxVisits    *int32     `json:"maxVisits"`
	ForwardQuery *bool      `json:"forwardQuery"`
	Enabled      *bool      `json:"enabled"`
}

var reservedSlugs = []string{"robots.txt", "favicon.ico", "healthz"}

func validateSlug(slug string) error {
	if slug == "" || len(slug) > 200 || strings.HasPrefix(slug, "/") || strings.HasSuffix(slug, "/") ||
		strings.HasSuffix(slug, "+") || slices.Contains(reservedSlugs, strings.ToLower(slug)) || strings.HasPrefix(slug, ".well-known/") ||
		strings.ContainsAny(slug, "?#%\\\"<>") || strings.IndexFunc(slug, unicode.IsSpace) >= 0 || strings.Contains(slug, "//") {
		return badRequest("invalid slug: no spaces or ?#%\\\"<>, may not start/end with / or end with +")
	}
	for _, seg := range strings.Split(slug, "/") {
		if seg == "." || seg == ".." { // browsers resolve these before requesting, so the link could never be reached
			return badRequest("invalid slug: no . or .. path segments")
		}
	}
	return nil
}

func (in *linkInput) validate() error {
	var err error
	if in.TargetURL, err = validateURL(in.TargetURL, "target URL", false); err != nil {
		return err
	}
	if in.IOSURL, err = validateURL(in.IOSURL, "iOS URL", true); err != nil {
		return err
	}
	if in.AndroidURL, err = validateURL(in.AndroidURL, "Android URL", true); err != nil {
		return err
	}
	in.Slug = strings.TrimSpace(in.Slug)
	if in.Slug != "" {
		if err := validateSlug(in.Slug); err != nil {
			return err
		}
	}
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) > 500 {
		return badRequest("title is too long")
	}
	tags := []string{}
	for _, t := range in.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && len(t) <= 50 && !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	if len(tags) > 20 {
		return badRequest("at most 20 tags")
	}
	in.Tags = tags
	if in.MaxVisits != nil && *in.MaxVisits < 1 {
		return badRequest("max visits must be positive")
	}
	if in.ValidSince != nil && in.ValidUntil != nil && !in.ValidUntil.After(*in.ValidSince) {
		return badRequest("valid until must be after valid since")
	}
	return nil
}

const slugAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no look-alikes (0/O, 1/l/I)

func randomSlug(n int) string {
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(slugAlphabet))))
		b[i] = slugAlphabet[x.Int64()]
	}
	return string(b)
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (s *Server) getLink(ctx context.Context, id int64) (*Link, error) {
	return scanLink(s.db.QueryRow(ctx, linkSelect+" WHERE l.id = $1", id))
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request, u *User) error {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("perPage"))
	page = max(page, 1)
	if perPage < 1 || perPage > 100 {
		perPage = 25
	}
	domainID, _ := strconv.ParseInt(q.Get("domainId"), 10, 64)
	order := map[string]string{
		"created": "l.created_at DESC", "visits": "l.visit_count DESC, l.id DESC", "slug": "l.slug ASC",
	}[q.Get("sort")]
	if order == "" {
		order = "l.created_at DESC"
	}
	args := u.scopeArgs()
	args["q"] = strings.TrimSpace(q.Get("q"))
	args["domain"] = domainID
	args["tag"] = strings.ToLower(q.Get("tag"))
	args["limit"] = perPage
	args["offset"] = (page - 1) * perPage
	// shortcut: ILIKE '%q%' is a seq scan; add a pg_trgm index once tables reach millions of rows.
	where := ` WHERE ` + linkScope + `
		AND (@q = '' OR l.slug ILIKE '%' || @q || '%' OR l.target_url ILIKE '%' || @q || '%' OR l.title ILIKE '%' || @q || '%')
		AND (@domain::bigint = 0 OR l.domain_id = @domain)
		AND (@tag = '' OR @tag = ANY(l.tags))`
	var total int64
	if err := s.db.QueryRow(r.Context(), "SELECT count(*) FROM links l"+where, args).Scan(&total); err != nil {
		return err
	}
	rows, _ := s.db.Query(r.Context(), linkSelect+where+" ORDER BY "+order+" LIMIT @limit OFFSET @offset", args)
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Link, error) { return scanLink(row) })
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": links, "total": total, "page": page, "perPage": perPage})
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, _ := s.db.Query(r.Context(), `SELECT t, count(*) FROM links l, unnest(l.tags) t WHERE `+linkScope+`
		GROUP BY t ORDER BY 2 DESC, 1 LIMIT 200`, u.scopeArgs())
	type tag struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
	}
	tags, err := pgx.CollectRows(rows, pgx.RowToStructByPos[tag])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, tags)
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request, u *User) error {
	var in linkInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if !u.canUseDomain(in.DomainID) {
		return errStatus(http.StatusForbidden, "you are not a member of this domain")
	}
	if err := in.validate(); err != nil {
		return err
	}
	var pwHash *string
	if in.Password != nil && *in.Password != "" {
		h, err := hashPassword(*in.Password)
		if err != nil {
			return err
		}
		pwHash = &h
	}
	custom := in.Slug != ""
	var id int64
	for attempt := 0; ; attempt++ {
		slug := in.Slug
		if !custom {
			slug = randomSlug(6 + attempt/3) // lengthen if the keyspace gets crowded
		}
		err := s.db.QueryRow(r.Context(), `INSERT INTO links (domain_id, slug, target_url, ios_url, android_url, title, tags,
			password_hash, valid_since, valid_until, max_visits, forward_query, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
			in.DomainID, slug, in.TargetURL, in.IOSURL, in.AndroidURL, in.Title, in.Tags, pwHash,
			in.ValidSince, in.ValidUntil, in.MaxVisits, in.ForwardQuery == nil || *in.ForwardQuery,
			in.Enabled == nil || *in.Enabled, u.ID).Scan(&id)
		if err == nil {
			s.cache.invalidate(r.Context(), s.db, linkKey(in.DomainID, slug))
			break
		}
		if !isUniqueViolation(err) || custom || attempt >= 8 {
			if isUniqueViolation(err) {
				return errStatus(http.StatusConflict, "slug is already taken on this domain")
			}
			return err
		}
	}
	l, err := s.getLink(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, l)
}

// linkForUser loads a link and checks the caller may edit it.
func (s *Server) linkForUser(r *http.Request, u *User) (*Link, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	l, err := s.getLink(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !u.canEditLink(l.DomainID, l.CreatedBy) {
		return nil, errNotFound // don't reveal links outside the caller's scope
	}
	return l, nil
}

func (s *Server) handleGetLink(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleUpdateLink(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	var in linkInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.DomainID == 0 {
		in.DomainID = l.DomainID
	}
	if in.Slug == "" {
		in.Slug = l.Slug
	}
	// Moving needs the same right on the target domain as editing there: own link + member, or domain admin.
	if in.DomainID != l.DomainID && !u.canEditLink(in.DomainID, l.CreatedBy) {
		return errStatus(http.StatusForbidden, "you may not move this link to the target domain")
	}
	if err := in.validate(); err != nil {
		return err
	}
	pw := "keep"
	var pwHash *string
	if in.Password != nil {
		pw = "set"
		if *in.Password != "" {
			h, err := hashPassword(*in.Password)
			if err != nil {
				return err
			}
			pwHash = &h
		}
	}
	// The WHERE re-checks the fields authorization was based on, so a concurrent move to another
	// domain (or owner change) between the check and this write makes the update a no-op.
	tag, err := s.db.Exec(r.Context(), `UPDATE links SET domain_id = $2, slug = $3, target_url = $4, ios_url = $5, android_url = $6,
		title = $7, tags = $8, password_hash = CASE WHEN $9 = 'set' THEN $10 ELSE password_hash END,
		valid_since = $11, valid_until = $12, max_visits = $13, forward_query = COALESCE($14, forward_query),
		enabled = COALESCE($15, enabled), updated_at = now()
		WHERE id = $1 AND domain_id = $16 AND slug = $17 AND created_by IS NOT DISTINCT FROM $18`,
		l.ID, in.DomainID, in.Slug, in.TargetURL, in.IOSURL, in.AndroidURL, in.Title, in.Tags, pw, pwHash,
		in.ValidSince, in.ValidUntil, in.MaxVisits, in.ForwardQuery, in.Enabled, l.DomainID, l.Slug, l.CreatedBy)
	if isUniqueViolation(err) {
		return errStatus(http.StatusConflict, "slug is already taken on this domain")
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errStatus(http.StatusConflict, "the link was changed or moved meanwhile, reload and try again")
	}
	s.cache.invalidate(r.Context(), s.db, linkKey(l.DomainID, l.Slug), linkKey(in.DomainID, in.Slug))
	nl, err := s.getLink(r.Context(), l.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, nl)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM links WHERE id = $1 AND domain_id = $2 AND slug = $3
		AND created_by IS NOT DISTINCT FROM $4`, l.ID, l.DomainID, l.Slug, l.CreatedBy) // see handleUpdateLink
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errStatus(http.StatusConflict, "the link was changed or moved meanwhile, reload and try again")
	}
	s.cache.invalidate(r.Context(), s.db, linkKey(l.DomainID, l.Slug))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleLinkQR(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size < 64 || size > 2048 {
		size = 512
	}
	png, err := qrcode.Encode(l.ShortURL, qrcode.Medium, size)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", `attachment; filename="qr-`+strings.ReplaceAll(l.Slug, "/", "_")+`.png"`)
	}
	_, err = w.Write(png)
	return err
}
