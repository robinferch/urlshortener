package app

import (
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type bucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type point struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type topLink struct {
	ID       int64  `json:"id"`
	ShortURL string `json:"shortUrl"`
	Title    string `json:"title"`
	Target   string `json:"targetUrl"`
	Count    int64  `json:"count"`
}

type Stats struct {
	Days      int       `json:"days"`
	Visits    int64     `json:"visits"`
	Bots      int64     `json:"bots"`
	Links     int64     `json:"links"`
	AllTime   int64     `json:"allTime"`
	Series    []point   `json:"series"`
	Hours     []int64   `json:"hours"`
	Referrers []bucket  `json:"referrers"`
	Countries []bucket  `json:"countries"`
	Browsers  []bucket  `json:"browsers"`
	OS        []bucket  `json:"os"`
	Devices   []bucket  `json:"devices"`
	TopLinks  []topLink `json:"topLinks,omitempty"`
}

func parseDays(r *http.Request) int {
	d, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if d < 1 || d > 3650 {
		return 30
	}
	return d
}

// computeStats aggregates non-bot visits since `from` for links matching scope (a predicate over alias l).
func (s *Server) computeStats(ctx context.Context, scope string, args pgx.NamedArgs, days int, withTop bool) (*Stats, error) {
	from := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	args["from"] = from
	st := &Stats{Days: days, Hours: make([]int64, 24)}
	base := ` FROM visits v JOIN links l ON l.id = v.link_id WHERE ` + scope + ` AND v.ts >= @from`

	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT v.bot), count(*) FILTER (WHERE v.bot)`+base, args).
		Scan(&st.Visits, &st.Bots); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*), COALESCE(sum(visit_count), 0) FROM links l WHERE `+scope, args).
		Scan(&st.Links, &st.AllTime); err != nil {
		return nil, err
	}
	rows, _ := s.db.Query(ctx, `SELECT to_char(d, 'YYYY-MM-DD'), COALESCE(c.n, 0)
		FROM generate_series(@from::timestamptz, date_trunc('day', now()), interval '1 day') d
		LEFT JOIN (SELECT date_trunc('day', v.ts) AS day, count(*) AS n`+base+` AND NOT v.bot GROUP BY 1) c ON c.day = d
		ORDER BY d`, args)
	var err error
	if st.Series, err = pgx.CollectRows(rows, pgx.RowToStructByPos[point]); err != nil {
		return nil, err
	}
	rows, _ = s.db.Query(ctx, `SELECT extract(hour FROM v.ts)::int, count(*)`+base+` AND NOT v.bot GROUP BY 1`, args)
	var h int
	var n int64
	if _, err := pgx.ForEachRow(rows, []any{&h, &n}, func() error { st.Hours[h] = n; return nil }); err != nil {
		return nil, err
	}
	for col, dst := range map[string]*[]bucket{
		"referrer": &st.Referrers, "country": &st.Countries, "browser": &st.Browsers, "os": &st.OS, "device": &st.Devices,
	} {
		rows, _ := s.db.Query(ctx, `SELECT v.`+col+`, count(*)`+base+
			` AND NOT v.bot GROUP BY 1 ORDER BY 2 DESC LIMIT 10`, args)
		if *dst, err = pgx.CollectRows(rows, pgx.RowToStructByPos[bucket]); err != nil {
			return nil, err
		}
	}
	if withTop {
		rows, _ := s.db.Query(ctx, `SELECT l.id, d.scheme || '://' || d.host || '/' || l.slug, l.title, l.target_url, count(*)`+
			strings.Replace(base, "JOIN links l ON l.id = v.link_id", "JOIN links l ON l.id = v.link_id JOIN domains d ON d.id = l.domain_id", 1)+
			` AND NOT v.bot GROUP BY l.id, d.scheme, d.host ORDER BY 5 DESC LIMIT 10`, args)
		if st.TopLinks, err = pgx.CollectRows(rows, pgx.RowToStructByPos[topLink]); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (s *Server) handleLinkStats(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	st, err := s.computeStats(r.Context(), "l.id = @link", pgx.NamedArgs{"link": l.ID}, parseDays(r), false)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleOverviewStats(w http.ResponseWriter, r *http.Request, u *User) error {
	args := u.scopeArgs()
	scope := linkScope
	if d, _ := strconv.ParseInt(r.URL.Query().Get("domainId"), 10, 64); d != 0 {
		scope += " AND l.domain_id = @domain"
		args["domain"] = d
	}
	st, err := s.computeStats(r.Context(), scope, args, parseDays(r), true)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

// csvSafe neutralises spreadsheet formulas in visitor-controlled cells (CSV injection).
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) handleVisitsCSV(w http.ResponseWriter, r *http.Request, u *User) error {
	l, err := s.linkForUser(r, u)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(r.Context(), `SELECT ts, referrer, country, browser, os, device, bot FROM visits
		WHERE link_id = $1 ORDER BY ts DESC LIMIT 1000000`, l.ID)
	if err != nil {
		return err
	}
	// Large exports outlive the server's default write timeout.
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="visits-`+itoa(l.ID)+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"timestamp", "referrer", "country", "browser", "os", "device", "bot"})
	var ts time.Time
	var ref, country, browser, os, device string
	var bot bool
	_, err = pgx.ForEachRow(rows, []any{&ts, &ref, &country, &browser, &os, &device, &bot}, func() error {
		return cw.Write([]string{ts.Format(time.RFC3339), csvSafe(ref), country, csvSafe(browser), csvSafe(os), device, strconv.FormatBool(bot)})
	})
	cw.Flush()
	return err
}
