package app

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mileusna/useragent"
)

type visit struct {
	linkID   int64
	ts       time.Time
	referrer string
	country  string
	browser  string
	os       string
	device   string
	bot      bool
	counted  bool // already added to links.visit_count by a max-visits reservation
}

var botMarkers = []string{"bot", "crawl", "spider", "slurp", "curl", "wget", "python", "go-http-client", "java/", "okhttp",
	"facebookexternalhit", "preview", "headless", "whatsapp", "embedly", "httpclient", "axios", "node-fetch", "monitor"}

func isBot(ua useragent.UserAgent, raw string) bool {
	if ua.Bot || raw == "" {
		return true
	}
	l := strings.ToLower(raw)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func classify(raw string) (browser, os, device string, bot bool) {
	ua := useragent.Parse(raw)
	return classifyParsed(ua, raw)
}

func classifyParsed(ua useragent.UserAgent, raw string) (browser, os, device string, bot bool) {
	bot = isBot(ua, raw)
	switch {
	case bot:
		device = "bot"
	case ua.Tablet:
		device = "tablet"
	case ua.Mobile:
		device = "mobile"
	case ua.Desktop:
		device = "desktop"
	default:
		device = "other"
	}
	return ua.Name, ua.OS, device, bot
}

// refHost keeps only the referrer's host; full referrer URLs can leak private paths.
func refHost(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// newVisit builds a privacy-friendly visit record: no IP or full user agent is stored.
func (s *Server) newVisit(r *http.Request, ua useragent.UserAgent, linkID int64) visit {
	v := visit{linkID: linkID, ts: time.Now(), referrer: refHost(r.Referer())}
	v.browser, v.os, v.device, v.bot = classifyParsed(ua, r.UserAgent())
	if s.fromTrustedProxy(r) {
		if c := r.Header.Get("CF-IPCountry"); len(c) == 2 && c != "XX" {
			v.country = strings.ToUpper(c)
		}
	}
	if v.country == "" && s.geo != nil {
		if ip := net.ParseIP(s.clientIP(r)); ip != nil {
			if rec, err := s.geo.Country(ip); err == nil {
				v.country = rec.Country.IsoCode
			}
		}
	}
	return v
}

// visitRecorder batches visits in memory and writes them with COPY, so a redirect never waits on the DB.
type visitRecorder struct {
	db      *pgxpool.Pool
	ch      chan visit
	dropped atomic.Int64
	done    chan struct{}
	once    sync.Once
}

func newVisitRecorder(db *pgxpool.Pool) *visitRecorder {
	return &visitRecorder{db: db, ch: make(chan visit, 50_000), done: make(chan struct{})}
}

func (v *visitRecorder) record(vis visit) {
	select {
	case v.ch <- vis:
	default:
		// The DB can't keep up; shed analytics rather than slow down redirects.
		if n := v.dropped.Add(1); n%1000 == 1 {
			slog.Warn("visit buffer full, dropping visits", "dropped_total", n)
		}
	}
}

func (v *visitRecorder) run() {
	defer close(v.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	buf := make([]visit, 0, 2000)
	for {
		select {
		case vis, ok := <-v.ch:
			if !ok {
				v.flush(buf)
				return
			}
			buf = append(buf, vis)
			if len(buf) >= 2000 {
				v.flush(buf)
				buf = buf[:0]
			}
		case <-t.C:
			if len(buf) > 0 {
				v.flush(buf)
				buf = buf[:0]
			}
		}
	}
}

// close stops accepting visits and waits for the final flush.
func (v *visitRecorder) close() {
	v.once.Do(func() { close(v.ch) })
	<-v.done
}

func (v *visitRecorder) flush(buf []visit) {
	if len(buf) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := insertVisits(ctx, v.db, buf, true)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		// A link was deleted while its visits were buffered; drop those and retry.
		ids := make([]int64, len(buf))
		for i, b := range buf {
			ids[i] = b.linkID
		}
		rows, _ := v.db.Query(ctx, "SELECT id FROM links WHERE id = ANY($1)", ids)
		alive, qerr := pgx.CollectRows(rows, pgx.RowTo[int64])
		if qerr != nil {
			err = qerr
		} else {
			kept := slices.DeleteFunc(slices.Clone(buf), func(b visit) bool { return !slices.Contains(alive, b.linkID) })
			err = insertVisits(ctx, v.db, kept, true)
		}
	}
	if err != nil {
		slog.Error("writing visits failed", "count", len(buf), "err", err)
	}
}

// insertVisits copies visits into the table; if bumpCounts, it also adds non-bot visits to links.visit_count.
// db may be a pool or a transaction (Begin then opens a savepoint).
func insertVisits(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}, buf []visit, bumpCounts bool) error {
	if len(buf) == 0 {
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"visits"},
		[]string{"link_id", "ts", "referrer", "country", "browser", "os", "device", "bot"},
		pgx.CopyFromSlice(len(buf), func(i int) ([]any, error) {
			b := buf[i]
			return []any{b.linkID, b.ts, b.referrer, b.country, b.browser, b.os, b.device, b.bot}, nil
		}))
	if err != nil {
		return err
	}
	if bumpCounts {
		counts := map[int64]int64{}
		for _, b := range buf {
			if !b.counted && !b.bot {
				counts[b.linkID]++
			}
		}
		// Sorted ids give a consistent lock order across concurrent flushes (no deadlocks between replicas).
		ids := slices.Sorted(maps.Keys(counts))
		ns := make([]int64, len(ids))
		for i, id := range ids {
			ns[i] = counts[id]
		}
		if _, err := tx.Exec(ctx, `UPDATE links SET visit_count = visit_count + c.n
			FROM unnest($1::bigint[], $2::bigint[]) AS c(id, n) WHERE links.id = c.id`, ids, ns); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
