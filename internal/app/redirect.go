package app

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mileusna/useragent"
	"golang.org/x/crypto/bcrypt"
)

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Title}}</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:ui-sans-serif,system-ui,sans-serif;background:#f8fafc;color:#0f172a}
@media (prefers-color-scheme:dark){body{background:#020617;color:#e2e8f0}.card{background:#0f172a!important;border-color:#1e293b!important}input{background:#020617;color:#e2e8f0;border-color:#334155!important}}
.card{background:#fff;border:1px solid #e2e8f0;border-radius:16px;padding:32px;max-width:420px;width:calc(100% - 48px);box-shadow:0 10px 30px -12px rgba(15,23,42,.15)}
h1{font-size:20px;margin:0 0 8px}p{color:#64748b;margin:0 0 20px;line-height:1.5;word-break:break-all}
input{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #cbd5e1;border-radius:8px;font-size:15px;margin-bottom:12px}
button,a.btn{display:inline-block;background:#0891b2;color:#fff;border:0;border-radius:8px;padding:10px 16px;font-size:15px;font-weight:600;cursor:pointer;text-decoration:none}
.err{color:#dc2626;font-size:14px;margin:-4px 0 12px}
</style></head><body><main class="card">
<h1>{{.Title}}</h1>
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .Password}}<form method="post"><label for="pw" style="display:block;font-size:14px;margin-bottom:6px">Password</label>
<input id="pw" name="password" type="password" autofocus required autocomplete="current-password">
{{if .Error}}<div class="err" role="alert">{{.Error}}</div>{{end}}<button type="submit">Continue</button></form>{{end}}
{{if .Target}}<a class="btn" href="{{.Target}}" rel="noopener noreferrer">Continue to destination</a>{{end}}
</main></body></html>`))

type pageData struct {
	Title, Message, Error, Target string
	Password                      bool
}

func renderPage(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	pageTmpl.Execute(w, d)
}

func (s *Server) lookupDomain(ctx context.Context, host string) (*cachedDomain, error) {
	if d, ok := s.cache.domains.Get(host); ok {
		return d, nil
	}
	gen := s.cache.gen.Load()
	d := &cachedDomain{found: true}
	err := s.db.QueryRow(ctx, "SELECT id, fallback_url FROM domains WHERE host = $1", host).Scan(&d.id, &d.fallback)
	if errors.Is(err, pgx.ErrNoRows) {
		d = &cachedDomain{}
	} else if err != nil {
		return nil, err
	}
	store(s.cache, s.cache.domains, gen, host, d)
	return d, nil
}

func (s *Server) lookupLink(ctx context.Context, domainID int64, slug string) (*cachedLink, error) {
	key := linkKey(domainID, slug)
	if l, ok := s.cache.links.Get(key); ok {
		return l, nil
	}
	gen := s.cache.gen.Load()
	l := &cachedLink{found: true}
	var pw *string
	var visits int64
	err := s.db.QueryRow(ctx, `SELECT id, target_url, ios_url, android_url, password_hash, valid_since, valid_until,
		max_visits, visit_count + capped_bot_hits, forward_query, enabled FROM links WHERE domain_id = $1 AND slug = $2`, domainID, slug).
		Scan(&l.id, &l.target, &l.ios, &l.android, &pw, &l.validSince, &l.validUntil, &l.maxVisits, &visits, &l.forwardQuery, &l.enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		l = &cachedLink{}
	} else if err != nil {
		return nil, err
	} else {
		l.exhausted.Store(l.maxVisits != nil && visits >= int64(*l.maxVisits))
		if pw != nil {
			l.passwordHash = *pw
		}
	}
	store(s.cache, s.cache.links, gen, key, l)
	return l, nil
}

// redirect serves every request that isn't for the app host.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	if r.URL.Path == "/robots.txt" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	}
	d, err := s.lookupDomain(ctx, normalizeHost(r.Host))
	if err != nil {
		renderPage(w, http.StatusServiceUnavailable, pageData{Title: "Temporarily unavailable", Message: "Please try again in a moment."})
		return
	}
	if !d.found {
		renderPage(w, http.StatusNotFound, pageData{Title: "Unknown domain", Message: "This domain is not configured."})
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/")
	slug = strings.TrimSuffix(slug, "/")
	preview := strings.HasSuffix(slug, "+")
	slug = strings.TrimSuffix(slug, "+")
	notFound := func() {
		if d.fallback != "" {
			http.Redirect(w, r, d.fallback, http.StatusFound)
			return
		}
		renderPage(w, http.StatusNotFound, pageData{Title: "Link not found", Message: "This short link does not exist."})
	}
	if slug == "" {
		notFound()
		return
	}
	l, err := s.lookupLink(ctx, d.id, slug)
	if err != nil {
		renderPage(w, http.StatusServiceUnavailable, pageData{Title: "Temporarily unavailable", Message: "Please try again in a moment."})
		return
	}
	if !l.found {
		notFound()
		return
	}
	now := time.Now()
	if !l.enabled || (l.validSince != nil && now.Before(*l.validSince)) || (l.validUntil != nil && now.After(*l.validUntil)) ||
		l.exhausted.Load() {
		renderGone(w)
		return
	}

	ua := useragent.Parse(r.UserAgent())
	target := l.target
	if ua.IsIOS() && l.ios != "" {
		target = l.ios
	} else if ua.IsAndroid() && l.android != "" {
		target = l.android
	}
	if l.forwardQuery && r.URL.RawQuery != "" {
		target = mergeQuery(target, r.URL.RawQuery)
	}

	if l.passwordHash != "" {
		if r.Method != http.MethodPost {
			renderPage(w, http.StatusOK, pageData{Title: "Protected link", Message: "Enter the password to continue.", Password: true})
			return
		}
		attempt := s.linkPw.take(s.clientIP(r))
		if attempt == nil {
			renderPage(w, http.StatusTooManyRequests, pageData{Title: "Protected link", Password: true, Error: "Too many attempts. Wait a minute and try again."})
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(l.passwordHash), []byte(r.FormValue("password"))) != nil {
			renderPage(w, http.StatusUnauthorized, pageData{Title: "Protected link", Password: true, Error: "Wrong password."})
			return
		}
		attempt() // only wrong passwords count against the limit
	} else if r.Method == http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limited := l.maxVisits != nil
	if limited && (preview || r.Method == http.MethodHead) {
		// Revealing the target without using up a visit would defeat the limit.
		renderPage(w, http.StatusOK, pageData{Title: "Limited link", Message: "This link can only be opened a limited number of times, so no preview is shown."})
		return
	}
	if preview {
		renderPage(w, http.StatusOK, pageData{Title: "Link preview", Message: target, Target: target})
		return
	}

	if r.Method != http.MethodHead {
		v := s.newVisit(r, ua, l.id)
		if limited { // bots too: an unfurler that sees the target has used the link
			// Reserve the visit atomically in the DB: exact under concurrency and across replicas.
			// Only links with a limit pay this write; everything else stays on the batched path.
			// Bots use up the quota via capped_bot_hits so visit_count stays human-only for analytics.
			tag, err := s.db.Exec(ctx, `UPDATE links SET visit_count = visit_count + (NOT $2)::int, capped_bot_hits = capped_bot_hits + $2::int
				WHERE id = $1 AND (max_visits IS NULL OR visit_count + capped_bot_hits < max_visits)`, l.id, v.bot)
			if err != nil {
				renderPage(w, http.StatusServiceUnavailable, pageData{Title: "Temporarily unavailable", Message: "Please try again in a moment."})
				return
			}
			if tag.RowsAffected() == 0 {
				l.exhausted.Store(true)
				renderGone(w)
				return
			}
			v.counted = true
		}
		s.visits.record(v)
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, target, http.StatusFound)
}

func renderGone(w http.ResponseWriter) {
	renderPage(w, http.StatusGone, pageData{Title: "Link unavailable", Message: "This link has expired or is not active."})
}

// mergeQuery appends incoming query params the target doesn't already set. The target's own
// query is kept byte-for-byte (order, encoding, valueless flags), so signed URLs keep working.
func mergeQuery(target, incomingRaw string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	existing := u.Query()
	var extra []string
	for _, pair := range strings.Split(incomingRaw, "&") {
		k, _, _ := strings.Cut(pair, "=")
		if key, err := url.QueryUnescape(k); err == nil && key != "" {
			if _, ok := existing[key]; !ok {
				extra = append(extra, pair)
			}
		}
	}
	if len(extra) == 0 {
		return target
	}
	if u.RawQuery != "" {
		u.RawQuery += "&"
	}
	u.RawQuery += strings.Join(extra, "&")
	return u.String()
}
