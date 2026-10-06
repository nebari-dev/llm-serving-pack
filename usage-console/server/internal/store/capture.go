package store

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// ContentWriter stores Warden's captured content off the request path (spec
// §9.3: no synchronous database call while deciding). Put never blocks; when
// the queue is full the content is dropped and counted, and the receipt
// still says its route captured.
type ContentWriter struct {
	st      *Store
	ch      chan CapturedContent
	Dropped atomic.Int64
}

// NewContentWriter writes queued content until ctx ends.
func (s *Store) NewContentWriter(ctx context.Context, queue int) *ContentWriter {
	w := &ContentWriter{st: s, ch: make(chan CapturedContent, queue)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-w.ch:
				if err := s.PutContent(ctx, c); err != nil {
					log.Printf("capture: receipt %s: %v", c.ReceiptID, err)
				}
			}
		}
	}()
	return w
}

func (w *ContentWriter) Put(c CapturedContent) {
	select {
	case w.ch <- c:
	default:
		// Said on the first drop and every 1,000th: a line per drop would
		// flood the log under the load that fills the queue.
		if n := w.Dropped.Add(1); n == 1 || n%1000 == 0 {
			log.Printf("capture: queue full, %d contents dropped so far (latest: receipt %s)", n, c.ReceiptID)
		}
	}
}

func (s *Store) PutContent(ctx context.Context, c CapturedContent) error {
	_, err := s.Receipts.Exec(ctx, `INSERT INTO receipt_content (tenant_id, receipt_id, ts, route, content) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING`, c.Tenant, c.ReceiptID, c.TS, c.Route, c.Content)
	return err
}

// contentJoin finds a receipt's content: Warden's, keyed by the receipt id
// with its own clock (within minutes of the receipt's start), else what the
// dev gateway stored on the receipt itself.
const contentJoin = `LEFT JOIN LATERAL (SELECT content FROM receipt_content c WHERE c.tenant_id = r.tenant_id AND c.receipt_id = r.id
		AND c.ts BETWEEN r.ts - interval '5 minutes' AND r.ts + interval '5 minutes' LIMIT 1) c ON true`

// SetRouteCapture turns content capture on or off for a route (spec §9.2),
// with its audit row. It changes nothing in the gateway, so it isn't a
// pending routing change: Warden picks it up at its next reload.
func (s *Store) SetRouteCapture(ctx context.Context, tenant, actor, name, ifMatch string, on bool) (model.Route, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Route{}, err
	}
	defer tx.Rollback(ctx)
	was, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeCols+` FROM routes WHERE tenant_id = $1 AND name = $2 FOR UPDATE`, tenant, name))
	if err != nil {
		return was, err
	}
	if ifMatch != was.ETag {
		return was, &StaleError{Current: was}
	}
	if was.CaptureContent == on {
		return was, nil
	}
	next := was
	next.CaptureContent = on
	next.ETag = RouteETag(next)
	if _, err := tx.Exec(ctx, `UPDATE routes SET capture_content = $3 WHERE tenant_id = $1 AND name = $2`, tenant, name, on); err != nil {
		return was, err
	}
	action := "Turned off content capture"
	if on {
		action = "Turned on content capture"
	}
	if err := audit(ctx, tx, tenant, actor, action, name, "Route", name, map[string]bool{"captureContent": was.CaptureContent}, map[string]bool{"captureContent": on}); err != nil {
		return was, err
	}
	return next, tx.Commit(ctx)
}

// RunningRoutes is the routes of the last apply that took, which the gateway
// runs; nil when no apply recorded them (before migration 060), and then the
// desired routes are the best guess.
func (s *Store) RunningRoutes(ctx context.Context, tenant string) ([]model.Route, error) {
	var b []byte
	err := s.Config.QueryRow(ctx, `SELECT routes FROM routing_applies WHERE tenant_id = $1 AND ok AND routes IS NOT NULL
		ORDER BY ts DESC, id DESC LIMIT 1`, tenant).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rs []model.Route
	return rs, json.Unmarshal(b, &rs)
}

// ReplayRow is a recorded request as replay reads it.
type ReplayRow struct {
	ID, KeyID, KeyName, Team, ProjectID, Model, Region string
	TS                                                 int64
	// Content is the captured content, nil when there's none.
	Content json.RawMessage
}

// ReplayRows calls fn for the newest receipts since since, at most limit,
// newest first: requests that reached policy evaluation (a key, settled, not
// refused before the rules by the key check, a budget or the model). It
// returns how many it read.
func (s *Store) ReplayRows(ctx context.Context, tenant string, since time.Time, limit int, fn func(ReplayRow)) (int, error) {
	rows, err := s.Receipts.Query(ctx, `SELECT r.id, r.ts, r.key_id, r.key_name, r.team, coalesce(r.project_id, ''), r.requested_model,
			coalesce(r.data_region, ''), coalesce(c.content, CASE WHEN r.content_captured THEN r.content END)
		FROM receipts r `+contentJoin+`
		WHERE r.tenant_id = $1 AND r.ts >= $2 AND r.key_id <> '' AND NOT r.in_flight
		  AND (jsonb_array_length(r.rules) > 0 OR r.verdict NOT IN ('blocked', 'throttled'))
		ORDER BY r.ts DESC LIMIT $3`, tenant, since, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var r ReplayRow
		var ts time.Time
		var content []byte
		if err := rows.Scan(&r.ID, &ts, &r.KeyID, &r.KeyName, &r.Team, &r.ProjectID, &r.Model, &r.Region, &content); err != nil {
			return n, err
		}
		r.TS = ts.UnixMilli()
		if len(content) > 0 && string(content) != "null" {
			r.Content = content
		}
		fn(r)
		n++
	}
	return n, rows.Err()
}
