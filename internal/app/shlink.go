package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type importJob struct {
	ID          int64      `json:"id"`
	Source      string     `json:"source"`
	Status      string     `json:"status"`
	Total       int        `json:"total"`
	Imported    int        `json:"imported"`
	Skipped     int        `json:"skipped"`
	Visits      int        `json:"visits"`
	Unsupported int        `json:"unsupported"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// failStaleImports marks jobs whose worker stopped reporting (process died) as failed.
// Live jobs on other replicas keep heartbeating, so a rolling restart doesn't touch them.
func (s *Server) failStaleImports(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE import_jobs SET status='failed', error='interrupted (server stopped)', finished_at=now()
		WHERE status='running' AND heartbeat_at < now() - interval '10 minutes'`)
	return err
}

func (s *Server) handleListImportJobs(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, _ := s.db.Query(r.Context(), `SELECT id, source, status, total, imported, skipped, visits, unsupported, error, created_at, finished_at
		FROM import_jobs WHERE user_id = $1 ORDER BY id DESC LIMIT 20`, u.ID)
	jobs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importJob])
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, jobs)
}

type shlinkImportInput struct {
	URL          string `json:"url"`
	APIKey       string `json:"apiKey"`
	DomainID     int64  `json:"domainId"`
	ImportVisits bool   `json:"importVisits"`
}

// handleShlinkImport starts a background import from a Shlink instance's REST API.
// Links whose Shlink domain matches a domain here (and the caller may use) keep it; the rest go to DomainID.
func (s *Server) handleShlinkImport(w http.ResponseWriter, r *http.Request, u *User) error {
	var in shlinkImportInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	// The importer fetches a user-supplied URL from the server's network, so it is limited to
	// instance admins, who already control the server.
	if !u.IsAdmin {
		return errStatus(http.StatusForbidden, "importing requires instance admin rights")
	}
	var domainExists bool
	if err := s.db.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM domains WHERE id = $1)", in.DomainID).Scan(&domainExists); err != nil {
		return err
	}
	if !domainExists {
		return badRequest("target domain does not exist")
	}
	base, err := validateURL(in.URL, "Shlink URL", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return badRequest("Shlink API key is required")
	}
	c := &shlinkClient{base: strings.TrimSuffix(base, "/"), key: strings.TrimSpace(in.APIKey), http: &http.Client{
		Timeout:       time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	// Fail fast on a wrong URL or key instead of creating a job that dies immediately.
	if err := c.detectVersion(r.Context()); err != nil {
		return badRequest(err.Error())
	}
	// One import per process: each holds a DB connection for its transaction plus one for heartbeats.
	select {
	case s.importSem <- struct{}{}:
	default:
		return errStatus(http.StatusConflict, "an import is already running, wait for it to finish")
	}
	var job importJob
	if err := s.db.QueryRow(r.Context(), `INSERT INTO import_jobs (user_id, source) VALUES ($1, $2)
		RETURNING id, source, status, total, imported, skipped, visits, unsupported, error, created_at, finished_at`, u.ID, "shlink: "+c.base).
		Scan(&job.ID, &job.Source, &job.Status, &job.Total, &job.Imported, &job.Skipped, &job.Visits, &job.Unsupported, &job.Error, &job.CreatedAt, &job.FinishedAt); err != nil {
		<-s.importSem
		return err
	}
	go s.runShlinkImport(job.ID, c, u, in) // releases importSem
	return writeJSON(w, http.StatusAccepted, job)
}

type shlinkClient struct {
	base, key, version string
	http               *http.Client
	// cutoff pins visit paging to a fixed snapshot: Shlink lists visits newest first, so visits
	// arriving mid-import would otherwise shift page boundaries (duplicating and skipping rows).
	cutoff time.Time
}

func (c *shlinkClient) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/rest/"+c.version+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		slog.Warn("shlink request failed", "err", err)
		return errors.New("could not reach Shlink at this URL")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Report only the status; echoing the body would turn this into an SSRF read primitive.
		return &shlinkHTTPError{res.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

type shlinkHTTPError struct{ status int }

func (e *shlinkHTTPError) Error() string {
	if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
		return "Shlink rejected the API key"
	}
	return "Shlink returned HTTP " + strconv.Itoa(e.status)
}

func (c *shlinkClient) detectVersion(ctx context.Context) error {
	var err error
	for _, v := range []string{"v3", "v2"} {
		c.version = v
		var page shlinkPage
		err = c.get(ctx, "/short-urls", url.Values{"page": {"1"}, "itemsPerPage": {"1"}}, &page)
		var he *shlinkHTTPError
		if !errors.As(err, &he) || he.status != http.StatusNotFound {
			return err
		}
	}
	return errors.New("no Shlink REST API found at this URL")
}

type shlinkPagination struct {
	CurrentPage int `json:"currentPage"`
	PagesCount  int `json:"pagesCount"`
	TotalItems  int `json:"totalItems"`
}

type shlinkPage struct {
	ShortURLs struct {
		Data       []shlinkShortURL `json:"data"`
		Pagination shlinkPagination `json:"pagination"`
	} `json:"shortUrls"`
}

type shlinkShortURL struct {
	ShortCode   string    `json:"shortCode"`
	LongURL     string    `json:"longUrl"`
	DateCreated time.Time `json:"dateCreated"`
	Tags        []string  `json:"tags"`
	Meta        struct {
		ValidSince *time.Time `json:"validSince"`
		ValidUntil *time.Time `json:"validUntil"`
		MaxVisits  *int32     `json:"maxVisits"`
	} `json:"meta"`
	Domain           *string            `json:"domain"`
	Title            *string            `json:"title"`
	ForwardQuery     *bool              `json:"forwardQuery"`
	DeviceLongURLs   map[string]*string `json:"deviceLongUrls"`
	HasRedirectRules *bool              `json:"hasRedirectRules"` // Shlink 4; lets us skip the rules request
	VisitsSummary    *struct {
		Total   int64 `json:"total"`
		NonBots int64 `json:"nonBots"`
	} `json:"visitsSummary"`
	VisitsCount int64 `json:"visitsCount"` // Shlink v2
}

type shlinkVisitsPage struct {
	Visits struct {
		Data []struct {
			Referer       string    `json:"referer"`
			Date          time.Time `json:"date"`
			UserAgent     string    `json:"userAgent"`
			PotentialBot  bool      `json:"potentialBot"`
			VisitLocation *struct {
				CountryCode string `json:"countryCode"`
			} `json:"visitLocation"`
		} `json:"data"`
		Pagination shlinkPagination `json:"pagination"`
	} `json:"visits"`
}

func (s *Server) runShlinkImport(jobID int64, c *shlinkClient, u *User, in shlinkImportInput) {
	c.cutoff = time.Now().Add(-time.Second).Truncate(time.Second) // strictly before the first visits request
	ctx, cancel := context.WithTimeout(s.ctx, 12*time.Hour)       // cancelled on shutdown; the transaction rolls back
	defer cancel()
	var imported, skipped, visits, total, unsupported int
	heartbeat := func() error {
		_, err := s.db.Exec(ctx, "UPDATE import_jobs SET total=$2, imported=$3, skipped=$4, visits=$5, unsupported=$6, heartbeat_at=now() WHERE id=$1",
			jobID, total, imported, skipped, visits, unsupported)
		return err
	}
	err := func() error {
		hosts := map[string]int64{}
		rows, _ := s.db.Query(ctx, "SELECT host, id FROM domains")
		var host string
		var id int64
		if _, err := pgx.ForEachRow(rows, []any{&host, &id}, func() error {
			if u.canUseDomain(id) {
				hosts[host] = id
			}
			return nil
		}); err != nil {
			return err
		}
		for page := 1; ; page++ {
			var p shlinkPage
			// Oldest first, so links created during the import append to the end instead of shifting pages.
			if err := c.get(ctx, "/short-urls", url.Values{"page": {strconv.Itoa(page)}, "itemsPerPage": {"100"}, "orderBy": {"dateCreated-ASC"}}, &p); err != nil {
				return err
			}
			total = p.ShortURLs.Pagination.TotalItems
			for _, su := range p.ShortURLs.Data {
				n, partial, err := s.importShlinkLink(ctx, c, su, hosts, in, u.ID, heartbeat)
				if err != nil {
					return err
				}
				if n < 0 {
					skipped++
				} else {
					imported++
					visits += n
					if partial {
						unsupported++
					}
				}
				if err := heartbeat(); err != nil {
					return err
				}
			}
			if err := heartbeat(); err != nil {
				return err
			}
			if page >= p.ShortURLs.Pagination.PagesCount || len(p.ShortURLs.Data) == 0 {
				return nil
			}
		}
	}()
	status, msg := "done", ""
	if err != nil {
		status, msg = "failed", err.Error()
		slog.Warn("shlink import failed", "job", jobID, "err", err)
	}
	<-s.importSem // before publishing the final status, so a client that sees "done" can start the next import
	s.cache.invalidate(context.Background(), s.db, "*")
	if _, err := s.db.Exec(context.Background(), `UPDATE import_jobs SET status=$2, error=$3, total=$4, imported=$5,
		skipped=$6, visits=$7, unsupported=$8, finished_at=now() WHERE id=$1`, jobID, status, msg, total, imported, skipped, visits, unsupported); err != nil {
		slog.Error("updating import job", "job", jobID, "err", err)
	}
}

// importShlinkLink imports one short URL and (optionally) its visit history in a single transaction,
// so a failed import leaves nothing behind and a re-run retries it. Returns -1 visits if skipped, and
// partial=true if some Shlink redirect rules couldn't be carried over.
func (s *Server) importShlinkLink(ctx context.Context, c *shlinkClient, su shlinkShortURL, hosts map[string]int64,
	in shlinkImportInput, userID int64, heartbeat func() error) (visits int, partial bool, err error) {
	// Shlink 4 replaced deviceLongUrls with redirect rules; map its device rules to our iOS/Android targets.
	if su.DeviceLongURLs == nil && c.version == "v3" && (su.HasRedirectRules == nil || *su.HasRedirectRules) {
		if partial, err = c.deviceRules(ctx, &su); err != nil {
			return 0, false, err
		}
	}
	// Shlink 3 also has a desktop-specific destination, which has no equivalent here.
	if d := su.DeviceLongURLs["desktop"]; d != nil && *d != "" {
		partial = true
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	linkID, err := importShlinkURL(ctx, tx, su, hosts, in.DomainID, userID)
	if err != nil || linkID == 0 {
		return -1, false, err
	}
	if in.ImportVisits {
		if visits, err = importShlinkVisits(ctx, tx, c, su, linkID, heartbeat); err != nil {
			return 0, false, err
		}
	}
	// Counters follow Shlink's own totals (history stops at the snapshot cutoff, or isn't imported):
	// human visits into visit_count, and for capped links the rest into capped_bot_hits, because
	// Shlink counts bots towards maxVisits. Otherwise a link exhausted at the source would reopen.
	humans, total := int64(0), su.VisitsCount // Shlink v2 only reports a total
	if su.VisitsSummary != nil {
		humans, total = su.VisitsSummary.NonBots, su.VisitsSummary.Total
	} else if !in.ImportVisits {
		humans = su.VisitsCount // no way to tell bots apart; best estimate for the analytics counter
	}
	if _, err := tx.Exec(ctx, `UPDATE links SET visit_count = GREATEST(visit_count, $2),
		capped_bot_hits = CASE WHEN max_visits IS NULL THEN 0 ELSE GREATEST(0, $3 - GREATEST(visit_count, $2)) END
		WHERE id = $1`, linkID, humans, total); err != nil {
		return 0, false, err
	}
	return visits, partial, tx.Commit(ctx)
}

type shlinkRedirectRules struct {
	RedirectRules []struct {
		LongURL    string `json:"longUrl"`
		Conditions []struct {
			Type       string `json:"type"`
			MatchValue string `json:"matchValue"`
		} `json:"conditions"`
	} `json:"redirectRules"`
}

// deviceRules fills su.DeviceLongURLs from Shlink 4 redirect rules. Rules other than a single
// ios/android device condition can't be represented here; it reports those as unsupported.
func (c *shlinkClient) deviceRules(ctx context.Context, su *shlinkShortURL) (unsupported bool, err error) {
	q := url.Values{}
	if su.Domain != nil {
		q.Set("domain", *su.Domain)
	}
	var rr shlinkRedirectRules
	err = c.get(ctx, "/short-urls/"+url.PathEscape(su.ShortCode)+"/redirect-rules", q, &rr)
	var he *shlinkHTTPError
	if errors.As(err, &he) && he.status == http.StatusNotFound {
		return false, nil // Shlink 3: no redirect rules endpoint
	}
	if err != nil {
		return false, err
	}
	su.DeviceLongURLs = map[string]*string{}
	for _, rule := range rr.RedirectRules { // ordered by priority; the first match wins in Shlink
		if len(rule.Conditions) == 1 && rule.Conditions[0].Type == "device" &&
			(rule.Conditions[0].MatchValue == "ios" || rule.Conditions[0].MatchValue == "android") {
			if su.DeviceLongURLs[rule.Conditions[0].MatchValue] == nil {
				u := rule.LongURL
				su.DeviceLongURLs[rule.Conditions[0].MatchValue] = &u
			}
			continue
		}
		unsupported = true
	}
	return unsupported, nil
}

// importShlinkURL inserts one short URL; returns 0 if it was skipped (invalid or slug already taken).
func importShlinkURL(ctx context.Context, tx pgx.Tx, su shlinkShortURL, hosts map[string]int64, fallback, userID int64) (int64, error) {
	domainID := fallback
	if su.Domain != nil {
		if id, ok := hosts[normalizeHost(*su.Domain)]; ok {
			domainID = id
		}
	}
	in := linkInput{DomainID: domainID, Slug: su.ShortCode, TargetURL: su.LongURL, Tags: su.Tags,
		ValidSince: su.Meta.ValidSince, ValidUntil: su.Meta.ValidUntil, MaxVisits: su.Meta.MaxVisits, ForwardQuery: su.ForwardQuery}
	if su.Title != nil {
		in.Title = *su.Title
	}
	if p := su.DeviceLongURLs["ios"]; p != nil {
		in.IOSURL = *p
	}
	if p := su.DeviceLongURLs["android"]; p != nil {
		in.AndroidURL = *p
	}
	if in.Slug == "" || in.validate() != nil {
		return 0, nil
	}
	created := su.DateCreated
	if created.IsZero() {
		created = time.Now()
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO links (domain_id, slug, target_url, ios_url, android_url, title, tags,
		valid_since, valid_until, max_visits, forward_query, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (domain_id, slug) DO NOTHING RETURNING id`,
		domainID, in.Slug, in.TargetURL, in.IOSURL, in.AndroidURL, in.Title, in.Tags, in.ValidSince, in.ValidUntil,
		in.MaxVisits, in.ForwardQuery == nil || *in.ForwardQuery, userID, created).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func importShlinkVisits(ctx context.Context, tx pgx.Tx, c *shlinkClient, su shlinkShortURL, linkID int64, heartbeat func() error) (int, error) {
	n := 0
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "itemsPerPage": {"5000"}, "endDate": {c.cutoff.Format(time.RFC3339)}}
		if su.Domain != nil {
			q.Set("domain", *su.Domain)
		}
		var p shlinkVisitsPage
		if err := c.get(ctx, "/short-urls/"+url.PathEscape(su.ShortCode)+"/visits", q, &p); err != nil {
			return n, err
		}
		buf := make([]visit, 0, len(p.Visits.Data))
		for _, d := range p.Visits.Data {
			v := visit{linkID: linkID, ts: d.Date, referrer: refHost(d.Referer)}
			v.browser, v.os, v.device, v.bot = classify(d.UserAgent)
			v.bot = v.bot || d.PotentialBot
			if v.bot {
				v.device = "bot"
			}
			if d.VisitLocation != nil && len(d.VisitLocation.CountryCode) == 2 {
				v.country = strings.ToUpper(d.VisitLocation.CountryCode)
			}
			buf = append(buf, v)
		}
		if err := insertVisits(ctx, tx, buf, true); err != nil {
			return n, err
		}
		n += len(buf)
		if err := heartbeat(); err != nil { // one link's history can take many pages
			return n, err
		}
		if page >= p.Visits.Pagination.PagesCount || len(p.Visits.Data) == 0 {
			return n, nil
		}
	}
}
