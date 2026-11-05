package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests need a throwaway Postgres: TEST_DATABASE_URL=postgres://... go test ./...
func testServer(t *testing.T) (*Server, *httptest.Server, *pgxpool.Pool) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	db, err := OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close) // cleanups run LIFO: stop server, then workers, then close the pool
	t.Cleanup(cancel)
	if _, err := db.Exec(ctx, "TRUNCATE users, domains, links, visits, sessions, api_keys, import_jobs RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	appURL, _ := url.Parse(ts.URL)
	cfg := &Config{AppURL: appURL, LocalAuth: true, SessionTTL: time.Hour, Version: "test"}
	srv, err := New(ctx, cfg, db, fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return srv, ts, db
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
	host string
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("X-Requested-With", "fetch")
	req.Header.Set("Content-Type", "application/json")
	if c.host != "" {
		req.Host = c.host
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func expect(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got status %d, want %d", what, got, want)
	}
}

func TestEndToEnd(t *testing.T) {
	srv, ts, db := testServer(t)
	admin := newClient(t, ts.URL)

	var cfg map[string]any
	admin.do("GET", "/api/config", nil, &cfg)
	if cfg["setupRequired"] != true {
		t.Fatalf("expected setup required, got %v", cfg)
	}
	expect(t, admin.do("POST", "/api/setup", map[string]string{"email": "Admin@Example.com", "password": "supersecret"}, nil), 201, "setup")
	expect(t, newClient(t, ts.URL).do("POST", "/api/setup", map[string]string{"email": "evil@example.com", "password": "supersecret"}, nil), 409, "second setup")

	// CSRF guard: cookie-authenticated writes without X-Requested-With are refused.
	req, _ := http.NewRequest("POST", ts.URL+"/api/domains", strings.NewReader(`{"host":"x.test"}`))
	res, _ := admin.http.Do(req)
	expect(t, res.StatusCode, 403, "csrf")
	// Basic credentials (e.g. from an auth proxy) are resent by browsers, so they must not bypass the guard
	// nor be mistaken for an API key.
	req, _ = http.NewRequest("POST", ts.URL+"/api/auth/login", strings.NewReader(`{"email":"x@example.com","password":"supersecret"}`))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	res, _ = http.DefaultClient.Do(req)
	expect(t, res.StatusCode, 403, "csrf with basic auth")
	req, _ = http.NewRequest("GET", ts.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	for _, c := range admin.http.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	res, _ = http.DefaultClient.Do(req)
	expect(t, res.StatusCode, 200, "session works behind basic-auth proxy")

	var d1, d2 Domain
	expect(t, admin.do("POST", "/api/domains", map[string]string{"host": "go.test", "scheme": "http", "fallbackUrl": "https://example.com/"}, &d1), 201, "domain1")
	expect(t, admin.do("POST", "/api/domains", map[string]string{"host": "other.test"}, &d2), 201, "domain2")
	expect(t, admin.do("PUT", fmt.Sprintf("/api/domains/%d/members", d1.ID), map[string]string{"email": "bob@example.com", "role": "user", "password": "bobpassword"}, nil), 200, "add member")
	expect(t, admin.do("PUT", fmt.Sprintf("/api/domains/%d/members", d1.ID), map[string]string{"email": "dana@example.com", "role": "admin", "password": "danapassword"}, nil), 200, "add domain admin")

	dana0 := newClient(t, ts.URL)
	dana0.do("POST", "/api/auth/login", map[string]string{"email": "dana@example.com", "password": "danapassword"}, nil)
	expect(t, dana0.do("PUT", fmt.Sprintf("/api/domains/%d/members", d1.ID), map[string]string{"email": "cfo@example.com", "role": "user", "password": "chosenbydana"}, nil), 403, "domain admin sets password")
	expect(t, dana0.do("PUT", fmt.Sprintf("/api/domains/%d/members", d1.ID), map[string]string{"email": "cfo@example.com", "role": "user"}, nil), 400, "passwordless account without SSO")
	expect(t, admin.do("PUT", fmt.Sprintf("/api/domains/%d/members", d1.ID), map[string]string{"email": "bob@example.com", "role": "user", "password": "overwrite1"}, nil), 409, "password for existing user")
	expect(t, dana0.do("POST", "/api/import/shlink", map[string]any{"url": "http://127.0.0.1:1", "apiKey": "k", "domainId": d1.ID}, nil), 403, "domain admin import")

	bob := newClient(t, ts.URL)
	expect(t, bob.do("POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "wrong-password"}, nil), 401, "bad login")
	expect(t, bob.do("POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "bobpassword"}, nil), 200, "login")
	dana := newClient(t, ts.URL)
	expect(t, dana.do("POST", "/api/auth/login", map[string]string{"email": "dana@example.com", "password": "danapassword"}, nil), 200, "login dana")

	// Role checks.
	expect(t, bob.do("POST", "/api/links", map[string]any{"domainId": d2.ID, "targetUrl": "https://example.org"}, nil), 403, "bob on foreign domain")
	expect(t, bob.do("POST", "/api/domains", map[string]string{"host": "bob.test"}, nil), 403, "bob creates domain")
	expect(t, bob.do("GET", "/api/users", nil, nil), 403, "bob lists users")
	expect(t, bob.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "targetUrl": "javascript:alert(1)"}, nil), 400, "bad scheme")

	var bl Link
	expect(t, bob.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "docs", "targetUrl": "https://example.org/docs?a=1",
		"iosUrl": "https://apps.apple.com/x", "tags": []string{"Docs", "docs", " team "}}, &bl), 201, "bob link")
	if bl.ShortURL != "http://go.test/docs" || len(bl.Tags) != 2 {
		t.Fatalf("unexpected link %+v", bl)
	}
	expect(t, bob.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "docs", "targetUrl": "https://example.org"}, nil), 409, "dup slug")
	var al Link
	expect(t, admin.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "targetUrl": "https://example.net"}, &al), 201, "admin random slug")
	if len(al.Slug) != 6 {
		t.Fatalf("random slug %q", al.Slug)
	}
	expect(t, bob.do("PUT", fmt.Sprintf("/api/links/%d", al.ID), map[string]any{"targetUrl": "https://evil.test"}, nil), 404, "bob edits admin link")
	// Dana administers d1 but is only a plain member of d2: she may not move Bob's link there.
	expect(t, admin.do("PUT", fmt.Sprintf("/api/domains/%d/members", d2.ID), map[string]string{"email": "dana@example.com", "role": "user"}, nil), 200, "dana joins d2")
	expect(t, dana.do("PUT", fmt.Sprintf("/api/links/%d", bl.ID), map[string]any{"domainId": d2.ID, "targetUrl": "https://attacker.example"}, nil), 403, "move foreign link to domain without admin rights")
	expect(t, dana.do("PUT", fmt.Sprintf("/api/links/%d", bl.ID), map[string]any{"targetUrl": "https://example.org/docs?a=1", "iosUrl": "https://apps.apple.com/x", "title": "Docs"}, nil), 200, "domain admin edits member link")
	var list struct {
		Items []Link `json:"items"`
		Total int    `json:"total"`
	}
	bob.do("GET", "/api/links", nil, &list)
	if list.Total != 1 {
		t.Fatalf("bob should see only own links, got %d", list.Total)
	}
	dana.do("GET", "/api/links?q=example", nil, &list)
	if list.Total != 2 {
		t.Fatalf("domain admin should see all domain links, got %d", list.Total)
	}

	// Redirects on the short domain.
	short := newClient(t, ts.URL)
	short.host = "go.test"
	get := func(path, ua string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Host = "go.test"
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", "https://www.news.example/article/1")
		res, err := short.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	r1 := get("/docs?utm_source=x&a=2", chrome)
	expect(t, r1.StatusCode, 302, "redirect")
	if loc := r1.Header.Get("Location"); loc != "https://example.org/docs?a=1&utm_source=x" {
		t.Fatalf("query forwarding: %s", loc)
	}
	if loc := get("/docs", iphone).Header.Get("Location"); loc != "https://apps.apple.com/x" {
		t.Fatalf("ios redirect: %s", loc)
	}
	get("/docs", "curl/8.0")
	if loc := get("/missing", chrome).Header.Get("Location"); loc != "https://example.com/" {
		t.Fatalf("fallback: %s", loc)
	}

	// Disable takes effect immediately (cache invalidation).
	expect(t, bob.do("PUT", fmt.Sprintf("/api/links/%d", bl.ID), map[string]any{"targetUrl": "https://example.org/docs?a=1", "iosUrl": "https://apps.apple.com/x", "enabled": false}, nil), 200, "disable")
	expect(t, get("/docs", chrome).StatusCode, 410, "disabled link")
	expect(t, bob.do("PUT", fmt.Sprintf("/api/links/%d", bl.ID), map[string]any{"targetUrl": "https://example.org/docs?a=1", "iosUrl": "https://apps.apple.com/x", "enabled": true}, nil), 200, "enable")

	// Password-protected link.
	var pl Link
	expect(t, admin.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "secret", "targetUrl": "https://example.com/secret", "password": "letmein1"}, &pl), 201, "pw link")
	expect(t, get("/secret", chrome).StatusCode, 200, "password page")
	form := func(pw string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/secret", strings.NewReader(url.Values{"password": {pw}}.Encode()))
		req.Host = "go.test"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, _ := short.http.Do(req)
		res.Body.Close()
		return res
	}
	expect(t, form("nope").StatusCode, 401, "wrong link password")
	// Correct passwords don't use up the per-IP budget (many visitors behind one NAT address).
	for i := range 12 { // above the burst of 10
		expect(t, form("letmein1").StatusCode, 302, fmt.Sprintf("right link password #%d", i))
	}

	// Max visits.
	var ml Link
	admin.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "once", "targetUrl": "https://example.com", "maxVisits": 1}, &ml)
	// A limited link never reveals its target without using up a visit.
	if res := get("/once+", chrome); res.StatusCode != 200 || res.Header.Get("Location") != "" {
		t.Fatalf("preview of limited link: %d", res.StatusCode)
	}
	headReq, _ := http.NewRequest("HEAD", ts.URL+"/once", nil)
	headReq.Host = "go.test"
	if res, _ := short.http.Do(headReq); res.StatusCode != 200 || res.Header.Get("Location") != "" {
		t.Fatalf("HEAD of limited link revealed target: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var botCapped Link
	admin.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "botcap", "targetUrl": "https://example.com", "maxVisits": 1}, &botCapped)
	expect(t, get("/botcap", "Slackbot-LinkExpanding 1.0").StatusCode, 302, "bot uses up capped link")
	expect(t, get("/botcap", chrome).StatusCode, 410, "capped link used up by bot")
	var botStats Link
	admin.do("GET", fmt.Sprintf("/api/links/%d", botCapped.ID), nil, &botStats)
	if botStats.Visits != 0 || botStats.LimitUsed != 1 {
		t.Fatalf("bot hit: visits=%d (want 0), limitUsed=%d (want 1)", botStats.Visits, botStats.LimitUsed)
	}
	expect(t, get("/once", chrome).StatusCode, 302, "first visit")
	expect(t, get("/once", chrome).StatusCode, 410, "over max visits")

	// Max visits holds under concurrency (the check-then-count race). Plain GETs: password
	// attempts would hit the per-IP rate limiter shared by this whole test.
	admin.do("POST", "/api/links", map[string]any{"domainId": d1.ID, "slug": "three", "targetUrl": "https://example.com", "maxVisits": 3}, nil)
	results := make(chan int, 20)
	for range 20 {
		go func() {
			req, _ := http.NewRequest("GET", ts.URL+"/three", nil)
			req.Host = "go.test"
			req.Header.Set("User-Agent", chrome)
			res, err := short.http.Do(req)
			if err != nil {
				results <- 0
				return
			}
			res.Body.Close()
			results <- res.StatusCode
		}()
	}
	ok := 0
	for range 20 {
		if <-results == 302 {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("maxVisits=3 allowed %d concurrent redirects", ok)
	}

	// Analytics (wait for the batch writer).
	time.Sleep(1500 * time.Millisecond)
	var st Stats
	expect(t, bob.do("GET", fmt.Sprintf("/api/links/%d/stats?days=7", bl.ID), nil, &st), 200, "stats")
	if st.Visits != 2 || st.Bots != 1 || len(st.Series) != 7 || st.Referrers[0].Key != "news.example" {
		t.Fatalf("unexpected stats: %+v", st)
	}
	var ov Stats
	expect(t, admin.do("GET", "/api/stats?days=30", nil, &ov), 200, "overview")
	if ov.Visits != 6 || ov.AllTime != 6 || ov.Bots != 14 || len(ov.TopLinks) != 3 { // bots: curl + 12 UA-less password POSTs + Slackbot; AllTime proves reserved visits aren't double-counted
		t.Fatalf("unexpected overview: %+v", ov)
	}

	// API keys authenticate without cookies or CSRF header.
	var key struct {
		Key string `json:"key"`
	}
	expect(t, bob.do("POST", "/api/me/api-keys", map[string]string{"name": "ci"}, &key), 201, "api key")
	req, _ = http.NewRequest("GET", ts.URL+"/api/links", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	res, _ = http.DefaultClient.Do(req)
	expect(t, res.StatusCode, 200, "api key auth")

	// Reverse-proxy on-demand TLS check.
	tlsCheck := func(domain string) int {
		res, err := http.Get(ts.URL + "/.well-known/urlshortener/tls-check?domain=" + url.QueryEscape(domain))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	expect(t, tlsCheck("GO.test"), 200, "tls check known domain")
	expect(t, tlsCheck(strings.TrimPrefix(ts.URL, "http://")), 200, "tls check app host")
	expect(t, tlsCheck("evil.test"), 404, "tls check unknown domain")
	// Proxied visitor traffic (has X-Forwarded-For) must not reach the check.
	req, _ = http.NewRequest("GET", ts.URL+"/.well-known/urlshortener/tls-check?domain=go.test", nil)
	req.Host = "go.test"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	res, _ = short.http.Do(req)
	expect(t, res.StatusCode, 302, "tls check hidden from proxied visitors (falls through to the fallback redirect)")

	// Deleting the domain removes it from the redirect path.
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/domains/%d", d1.ID), nil, nil), 204, "delete domain")
	expect(t, get("/docs", chrome).StatusCode, 404, "deleted domain")
	_ = srv
	_ = db
}

// Shlink v2 only reports visitsCount (bots included); an exhausted capped link must stay closed.
func TestShlinkV2CappedImport(t *testing.T) {
	_, ts, db := testServer(t)
	shlink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/v2/short-urls":
			io.WriteString(w, `{"shortUrls":{"data":[{"shortCode":"cap","longUrl":"https://a.example",
				"dateCreated":"2021-01-02T03:04:05+00:00","tags":[],"meta":{"maxVisits":2},"domain":null,"visitsCount":2}],
				"pagination":{"currentPage":1,"pagesCount":1,"totalItems":1}}}`)
		case r.URL.Path == "/rest/v2/short-urls/cap/visits":
			io.WriteString(w, `{"visits":{"data":[
				{"referer":"","date":"2021-02-01T10:00:00+00:00","userAgent":"Mozilla/5.0 (Windows NT 10.0) Chrome/120.0","visitLocation":null,"potentialBot":false},
				{"referer":"","date":"2021-02-01T11:00:00+00:00","userAgent":"curl/8.0","visitLocation":null,"potentialBot":true}
			],"pagination":{"currentPage":1,"pagesCount":1,"totalItems":2}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer shlink.Close()
	admin := newClient(t, ts.URL)
	admin.do("POST", "/api/setup", map[string]string{"email": "admin@example.com", "password": "supersecret"}, nil)
	var d Domain
	admin.do("POST", "/api/domains", map[string]string{"host": "v2.test", "scheme": "http"}, &d)
	var job importJob
	expect(t, admin.do("POST", "/api/import/shlink", map[string]any{"url": shlink.URL, "apiKey": "k", "domainId": d.ID, "importVisits": true}, &job), 202, "v2 import")
	for i := 0; i < 50 && job.Status == "running"; i++ {
		time.Sleep(100 * time.Millisecond)
		var jobs []importJob
		admin.do("GET", "/api/import/jobs", nil, &jobs)
		job = jobs[0]
	}
	var humans, bots int64
	db.QueryRow(context.Background(), "SELECT visit_count, capped_bot_hits FROM links WHERE slug='cap'").Scan(&humans, &bots)
	if job.Status != "done" || humans != 1 || bots != 1 {
		t.Fatalf("v2 capped import: job=%+v visit_count=%d capped_bot_hits=%d (want 1/1)", job, humans, bots)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/cap", nil)
	req.Host = "v2.test"
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	expect(t, res.StatusCode, 410, "exhausted v2 link stays closed")
}

func TestConcurrentSetup(t *testing.T) {
	_, ts, db := testServer(t)
	codes := make(chan int, 8)
	for i := range 8 {
		go func() {
			codes <- newClient(t, ts.URL).do("POST", "/api/setup", map[string]string{"email": fmt.Sprintf("a%d@example.com", i), "password": "supersecret"}, nil)
		}()
	}
	for range 8 {
		<-codes
	}
	var admins int
	db.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&admins)
	if admins != 1 {
		t.Fatalf("concurrent setup created %d admins", admins)
	}
}

func TestShlinkImport(t *testing.T) {
	_, ts, db := testServer(t)
	var failVisits atomic.Bool
	failVisits.Store(true)
	shlink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/rest/v3/short-urls" && r.URL.Query().Get("orderBy") != "dateCreated-ASC" && r.URL.Query().Get("itemsPerPage") != "1":
			w.WriteHeader(400) // page links oldest first so new links can't shift page boundaries
		case r.URL.Path == "/rest/v3/short-urls":
			io.WriteString(w, `{"shortUrls":{"data":[
				{"shortCode":"abc","longUrl":"https://a.example","dateCreated":"2023-01-02T03:04:05+00:00","tags":["x"],
				 "meta":{"validSince":null,"validUntil":null,"maxVisits":50},"domain":null,"title":"A","forwardQuery":true,"hasRedirectRules":false,
				 "visitsSummary":{"total":3,"nonBots":2,"bots":1}},
				{"shortCode":"ghi","longUrl":"https://c.example","dateCreated":"2023-01-02T03:04:05+00:00","tags":[],
				 "meta":{},"domain":null,"title":null,"deviceLongUrls":{"android":null,"ios":null,"desktop":"https://c.example/desktop"}},
				{"shortCode":"def","longUrl":"https://b.example","dateCreated":"2023-01-02T03:04:05+00:00","tags":[],
				 "meta":{},"domain":"s.test","title":null,"hasRedirectRules":true},
				{"shortCode":"bad","longUrl":"ftp://nope","dateCreated":"2023-01-02T03:04:05+00:00","meta":{}}
			],"pagination":{"currentPage":1,"pagesCount":1,"totalItems":3}}}`)
		case r.URL.Path == "/rest/v3/short-urls/abc/redirect-rules":
			w.WriteHeader(500) // must not be requested: abc reports hasRedirectRules=false
		case r.URL.Path == "/rest/v3/short-urls/def/redirect-rules":
			io.WriteString(w, `{"defaultLongUrl":"https://b.example","redirectRules":[
				{"longUrl":"https://play.example/app","priority":1,"conditions":[{"type":"device","matchValue":"android","matchKey":null}]},
				{"longUrl":"https://b.example/de","priority":2,"conditions":[{"type":"language","matchValue":"de","matchKey":null}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/visits") && r.URL.Query().Get("endDate") == "":
			w.WriteHeader(400) // the importer must pin visit paging to a snapshot
		case strings.HasSuffix(r.URL.Path, "/visits") && failVisits.Load():
			w.WriteHeader(500)
		case strings.HasSuffix(r.URL.Path, "/visits"):
			io.WriteString(w, `{"visits":{"data":[
				{"referer":"https://t.co/x","date":"2023-02-01T10:00:00+00:00","userAgent":"Mozilla/5.0 (Windows NT 10.0) Chrome/120.0","visitLocation":{"countryCode":"de"},"potentialBot":false},
				{"referer":"","date":"2023-02-01T11:00:00+00:00","userAgent":"Mozilla/5.0 (Windows NT 10.0) Chrome/120.0","visitLocation":null,"potentialBot":true}
			],"pagination":{"currentPage":1,"pagesCount":1,"totalItems":2}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer shlink.Close()

	admin := newClient(t, ts.URL)
	admin.do("POST", "/api/setup", map[string]string{"email": "admin@example.com", "password": "supersecret"}, nil)
	var main, s Domain
	admin.do("POST", "/api/domains", map[string]string{"host": "main.test"}, &main)
	admin.do("POST", "/api/domains", map[string]string{"host": "s.test"}, &s)

	expect(t, admin.do("POST", "/api/import/shlink", map[string]any{"url": shlink.URL, "apiKey": "wrong", "domainId": main.ID}, nil), 400, "bad key")
	runImport := func() importJob {
		var job importJob
		expect(t, admin.do("POST", "/api/import/shlink", map[string]any{"url": shlink.URL, "apiKey": "k", "domainId": main.ID, "importVisits": true}, &job), 202, "import")
		for i := 0; i < 50 && job.Status == "running"; i++ {
			time.Sleep(100 * time.Millisecond)
			var jobs []importJob
			admin.do("GET", "/api/import/jobs", nil, &jobs)
			job = jobs[0]
		}
		return job
	}
	// A failed visit fetch rolls back the link too, so a re-run imports it completely.
	if job := runImport(); job.Status != "failed" || job.Imported != 0 {
		t.Fatalf("expected failed job with nothing imported, got %+v", job)
	}
	failVisits.Store(false)
	job := runImport()
	if job.Status != "done" || job.Imported != 3 || job.Skipped != 1 || job.Visits != 6 || job.Unsupported != 2 { // unsupported: def language rule, ghi desktop URL
		t.Fatalf("unexpected job %+v", job)
	}
	var domainOfDef int64
	var count int64
	db.QueryRow(context.Background(), "SELECT domain_id FROM links WHERE slug='def'").Scan(&domainOfDef)
	db.QueryRow(context.Background(), "SELECT visit_count FROM links WHERE slug='abc'").Scan(&count)
	var cappedBots int64
	db.QueryRow(context.Background(), "SELECT capped_bot_hits FROM links WHERE slug='abc'").Scan(&cappedBots)
	if cappedBots != 1 { // visitsSummary total 3 - nonBots 2
		t.Fatalf("capped bot hits not imported: %d", cappedBots)
	}
	var android string
	db.QueryRow(context.Background(), "SELECT android_url FROM links WHERE slug='def'").Scan(&android)
	if android != "https://play.example/app" {
		t.Fatalf("Shlink 4 device rule not mapped: %q", android)
	}
	if domainOfDef != s.ID || count != 2 { // history has 1 human visit, Shlink summary says 2: counter follows the summary
		t.Fatalf("def should map to s.test (%d), abc visit_count 2, got %d / %d", s.ID, domainOfDef, count)
	}
	// Re-running skips existing slugs.
	expect(t, admin.do("POST", "/api/import/shlink", map[string]any{"url": shlink.URL, "apiKey": "k", "domainId": main.ID}, nil), 202, "re-import")
	time.Sleep(300 * time.Millisecond)
	var jobs []importJob
	admin.do("GET", "/api/import/jobs", nil, &jobs)
	if jobs[0].Imported != 0 || jobs[0].Skipped != 4 {
		t.Fatalf("re-import should skip all: %+v", jobs[0])
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{cfg: &Config{TrustProxy: true, ProxyHops: 1}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	if ip := s.clientIP(r); ip != "1.2.3.4" {
		t.Fatalf("spoofable XFF entry used: %s", ip)
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6") // client-sent line, proxy adds its own (HAProxy style)
	r.Header.Add("X-Forwarded-For", "5.5.5.5")
	if ip := s.clientIP(r); ip != "5.5.5.5" {
		t.Fatalf("multi-line XFF: %s", ip)
	}
	r.RemoteAddr = "203.0.113.9:1234" // public peer hitting the app port directly
	if ip := s.clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("XFF trusted from a public peer: %s", ip)
	}
	r.RemoteAddr = "10.0.0.1:1234"
	s.cfg.TrustProxy = false
	if ip := s.clientIP(r); ip != "10.0.0.1" {
		t.Fatalf("untrusted proxy: %s", ip)
	}
}

func TestLimiterRefund(t *testing.T) {
	l := newIPLimiter(context.Background())
	for i := range 25 {
		refund := l.take("1.2.3.4")
		if refund == nil {
			t.Fatalf("successful attempt %d was throttled", i)
		}
		time.Sleep(time.Millisecond) // the refund must work after time has passed (bcrypt)
		refund()
	}
	// Interleaved: 10 in flight, an 11th rejected, then all 10 succeed and refund.
	var inflight []func()
	for range 10 {
		inflight = append(inflight, l.take("9.9.9.9"))
	}
	if l.take("9.9.9.9") != nil {
		t.Fatal("11th concurrent attempt allowed")
	}
	for _, refund := range inflight {
		refund()
	}
	if l.take("9.9.9.9") == nil {
		t.Fatal("refunds lost after a rejected attempt")
	}
	for range 10 {
		l.take("5.6.7.8") // failures are not refunded
	}
	if l.take("5.6.7.8") != nil {
		t.Fatal("11th failed attempt allowed")
	}
}

func TestCSVSafe(t *testing.T) {
	if csvSafe("=1+2") != "'=1+2" || csvSafe("example.com") != "example.com" {
		t.Fatal("csvSafe")
	}
}

func TestValidateSlug(t *testing.T) {
	for slug, ok := range map[string]bool{"abc": true, "a/b": true, "ünï": true, "": false, "a b": false, "x+": false,
		"/x": false, "a?b": false, "robots.txt": false, `a"b`: false, "a//b": false, ".well-known/x": false, "a/../b": false, "./a": false, "a/.": false, "a.b/..c": true} {
		if (validateSlug(slug) == nil) != ok {
			t.Errorf("validateSlug(%q) ok=%v", slug, !ok)
		}
	}
}

func TestMergeQuery(t *testing.T) {
	for target, want := range map[string]string{
		"https://x.test/p?z=1&a=1&flag#frag": "https://x.test/p?z=1&a=1&flag&b=2&c#frag",
		"https://x.test/p":                   "https://x.test/p?a=9&b=2&c",
	} {
		if got := mergeQuery(target, "a=9&b=2&c"); got != want {
			t.Errorf("mergeQuery(%q) = %q, want %q", target, got, want)
		}
	}
}
