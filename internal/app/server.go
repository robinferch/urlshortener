package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/gzhttp"
	"github.com/oschwald/geoip2-golang"
)

type Server struct {
	cfg     *Config
	db      *pgxpool.Pool
	cache   *cache
	visits  *visitRecorder
	oidc    *oidcAuth
	limiter *ipLimiter // logins and setup
	linkPw  *ipLimiter // protected-link passwords; separate so visitors' typos can't lock out admins
	geo     *geoip2.Reader
	app     http.Handler
	appHost string
	// importSem allows one Shlink import per process.
	importSem chan struct{}
	ctx       context.Context // lives until shutdown; for background jobs
}

// New wires up the server. Background workers stop when ctx is cancelled;
// call Close afterwards to flush pending visits.
func New(ctx context.Context, cfg *Config, db *pgxpool.Pool, ui fs.FS) (*Server, error) {
	s := &Server{
		cfg:       cfg,
		db:        db,
		cache:     newCache(),
		limiter:   newIPLimiter(ctx),
		linkPw:    newIPLimiter(ctx),
		appHost:   normalizeHost(cfg.AppURL.Host),
		importSem: make(chan struct{}, 1),
		ctx:       ctx,
	}
	if cfg.GeoIPDB != "" {
		g, err := geoip2.Open(cfg.GeoIPDB)
		if err != nil {
			return nil, err
		}
		s.geo = g
	}
	if cfg.OIDCEnabled() {
		s.oidc = &oidcAuth{cfg: cfg}
	}
	s.visits = newVisitRecorder(db)
	go s.visits.run()
	go s.cache.listen(ctx, db)
	go s.housekeeping(ctx)
	if err := s.bootstrapAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.failStaleImports(ctx); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", spaHandler(ui))
	s.app = gzhttp.GzipHandler(securityHeaders(csrfGuard(mux)))
	return s, nil
}

// Close flushes buffered visits. Call after the HTTP server has stopped.
func (s *Server) Close() {
	s.visits.close()
	if s.geo != nil {
		s.geo.Close()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if err := s.db.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
		return
	}
	// Only the proxy's own "ask" call qualifies: it comes from a private peer and, unlike proxied
	// visitor traffic, carries no X-Forwarded-For. Everything else falls through to normal routing.
	if r.URL.Path == tlsCheckPath && privatePeer(r) && r.Header.Get("X-Forwarded-For") == "" {
		s.handleTLSCheck(w, r)
		return
	}
	if normalizeHost(r.Host) == s.appHost {
		s.app.ServeHTTP(w, r)
		return
	}
	s.redirect(w, r)
}

const tlsCheckPath = "/.well-known/urlshortener/tls-check"

// handleTLSCheck answers a reverse proxy's on-demand TLS "ask" request (Caddy: on_demand_tls { ask ... }):
// 200 if ?domain= is the app host or a configured short domain, so certificates are only issued for those.
func (s *Server) handleTLSCheck(w http.ResponseWriter, r *http.Request) {
	host := normalizeHost(r.URL.Query().Get("domain"))
	if host != "" && host != s.appHost {
		d, err := s.lookupDomain(r.Context(), host)
		if err != nil {
			http.Error(w, "lookup failed", http.StatusServiceUnavailable)
			return
		}
		if !d.found {
			http.Error(w, "unknown domain", http.StatusNotFound)
			return
		}
	} else if host == "" {
		http.Error(w, "missing domain", http.StatusBadRequest)
		return
	}
	w.Write([]byte("ok"))
}

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/config", s.h(s.handleConfig))
	m.HandleFunc("POST /api/setup", s.h(s.handleSetup))
	m.HandleFunc("POST /api/auth/login", s.h(s.handleLogin))
	m.HandleFunc("POST /api/auth/logout", s.h(s.handleLogout))
	m.HandleFunc("GET /api/auth/oidc/login", s.h(s.handleOIDCLogin))
	m.HandleFunc("GET /api/auth/oidc/callback", s.handleOIDCCallback)

	m.HandleFunc("GET /api/me", s.authed(s.handleMe))
	m.HandleFunc("PATCH /api/me", s.authed(s.handleUpdateMe))
	m.HandleFunc("GET /api/me/api-keys", s.authed(s.handleListAPIKeys))
	m.HandleFunc("POST /api/me/api-keys", s.authed(s.handleCreateAPIKey))
	m.HandleFunc("DELETE /api/me/api-keys/{id}", s.authed(s.handleDeleteAPIKey))

	m.HandleFunc("GET /api/users", s.authed(s.handleListUsers))
	m.HandleFunc("POST /api/users", s.authed(s.handleCreateUser))
	m.HandleFunc("PATCH /api/users/{id}", s.authed(s.handleUpdateUser))
	m.HandleFunc("DELETE /api/users/{id}", s.authed(s.handleDeleteUser))

	m.HandleFunc("GET /api/domains", s.authed(s.handleListDomains))
	m.HandleFunc("POST /api/domains", s.authed(s.handleCreateDomain))
	m.HandleFunc("PATCH /api/domains/{id}", s.authed(s.handleUpdateDomain))
	m.HandleFunc("DELETE /api/domains/{id}", s.authed(s.handleDeleteDomain))
	m.HandleFunc("GET /api/domains/{id}/members", s.authed(s.handleListMembers))
	m.HandleFunc("PUT /api/domains/{id}/members", s.authed(s.handlePutMember))
	m.HandleFunc("DELETE /api/domains/{id}/members/{userId}", s.authed(s.handleDeleteMember))

	m.HandleFunc("GET /api/links", s.authed(s.handleListLinks))
	m.HandleFunc("POST /api/links", s.authed(s.handleCreateLink))
	m.HandleFunc("GET /api/links/{id}", s.authed(s.handleGetLink))
	m.HandleFunc("PUT /api/links/{id}", s.authed(s.handleUpdateLink))
	m.HandleFunc("DELETE /api/links/{id}", s.authed(s.handleDeleteLink))
	m.HandleFunc("GET /api/links/{id}/stats", s.authed(s.handleLinkStats))
	m.HandleFunc("GET /api/links/{id}/qr", s.authed(s.handleLinkQR))
	m.HandleFunc("GET /api/links/{id}/visits.csv", s.authed(s.handleVisitsCSV))
	m.HandleFunc("GET /api/tags", s.authed(s.handleListTags))
	m.HandleFunc("GET /api/stats", s.authed(s.handleOverviewStats))

	m.HandleFunc("POST /api/import/shlink", s.authed(s.handleShlinkImport))
	m.HandleFunc("GET /api/import/jobs", s.authed(s.handleListImportJobs))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
}

func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE expires_at < now()"); err != nil {
				slog.Warn("session cleanup", "err", err)
			}
			if err := s.failStaleImports(ctx); err != nil {
				slog.Warn("import cleanup", "err", err)
			}
		}
	}
}

// ---- HTTP helpers ----

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func errStatus(status int, msg string) error { return &apiError{status, msg} }
func badRequest(msg string) error            { return &apiError{http.StatusBadRequest, msg} }

var (
	errUnauthorized = &apiError{http.StatusUnauthorized, "authentication required"}
	errForbidden    = &apiError{http.StatusForbidden, "forbidden"}
	errNotFound     = &apiError{http.StatusNotFound, "not found"}
)

func (s *Server) h(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var ae *apiError
		var pe *pgconn.PgError
		switch {
		case errors.As(err, &ae):
			writeError(w, ae.status, ae.msg)
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "not found")
		case errors.As(err, &pe) && pe.Code == "23505":
			writeError(w, http.StatusConflict, "already exists")
		case errors.As(err, &pe) && pe.Code == "23503":
			writeError(w, http.StatusBadRequest, "referenced record does not exist")
		default:
			slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, errNotFound
	}
	return id, nil
}

func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	// Drop default ports so "example.com:443" and "example.com" match.
	if host, port, err := net.SplitHostPort(h); err == nil && (port == "80" || port == "443") {
		h = host
	}
	return h
}

// fromTrustedProxy reports whether forwarding headers may be believed: TRUST_PROXY is on and the
// direct peer is on loopback or a private network, so clients reaching an exposed app port directly can't spoof them.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	return s.cfg.TrustProxy && privatePeer(r)
}

func privatePeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func (s *Server) clientIP(r *http.Request) string {
	if s.fromTrustedProxy(r) {
		// Proxies append to X-Forwarded-For (some as an extra header line), so only the last
		// PROXY_HOPS entries across all lines are trustworthy; anything further left is client-controlled.
		if xff := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ","); len(xff) >= s.cfg.ProxyHops {
			if ip := net.ParseIP(strings.TrimSpace(xff[len(xff)-s.cfg.ProxyHops])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard rejects browser-originated API writes that lack X-Requested-With. Cross-site forms
// can't set that header, and cross-origin fetch can't without a CORS preflight we never allow.
// API-key requests are exempt: browsers never attach X-Api-Key or Bearer tokens automatically
// (unlike Basic credentials, which they resend on their own).
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") &&
			r.Header.Get("X-Requested-With") == "" && r.Header.Get("X-Api-Key") == "" && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeError(w, http.StatusForbidden, "missing X-Requested-With header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves the built frontend, falling back to index.html for client routes.
func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" {
			if st, err := fs.Stat(ui, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}
