package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/georgebuilds/carlos/internal/agent"
)

// dedupeSchemaSQL is the broker-owned idempotency table. Lives in the
// same SQLite database as the event log (we share the *sql.DB handle
// via SQLiteEventLog.DB) so the broker stays a self-contained migration
// without forking the canonical schema in eventlog_sqlite.go.
//
// One row per (source, gateway_event_id) seen. A retry from the
// platform (Telegram resending an update because we crashed before
// acking, ntfy resending an action click) bumps into the primary key
// and the broker drops the duplicate before any downstream work.
const dedupeSchemaSQL = `
CREATE TABLE IF NOT EXISTS gateway_inbound_idempotency (
  source           TEXT    NOT NULL,
  gateway_event_id TEXT    NOT NULL,
  envelope_id      TEXT    NOT NULL,
  ingested_at      INTEGER NOT NULL,
  PRIMARY KEY (source, gateway_event_id)
);
`

// migrateDedupe applies the idempotency-table migration. Idempotent;
// safe to call on every broker start. Returns nil on a fresh DB AND on
// a DB that already has the table.
func migrateDedupe(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("dedupe: nil db")
	}
	if _, err := db.ExecContext(ctx, dedupeSchemaSQL); err != nil {
		return fmt.Errorf("dedupe: migrate: %w", err)
	}
	return nil
}

// claimIngest reserves (source, gatewayEventID) before the broker
// writes the EvtGatewayInbound row. Returns (true, nil) on first sight,
// (false, nil) on duplicate, (false, err) on a real DB problem.
//
// Why this pattern: persisting the claim in SQL is the only way to
// survive a daemon crash mid-ingest - an in-memory set is wiped on
// restart and the next Telegram poll re-delivers the unacked update.
// The row-write happens BEFORE the rest of the ingest pipeline so a
// crash after claim but before event-append is recoverable as "ingest
// happened, event lost" - the inbound was logically processed, the
// audit row is just missing. Acceptable for an idempotency guarantee.
func claimIngest(ctx context.Context, db *sql.DB, source Source, gatewayEventID, envelopeID string, ingestedAtMillis int64) (bool, error) {
	if db == nil {
		return false, errors.New("dedupe: nil db")
	}
	if !source.Valid() {
		return false, fmt.Errorf("dedupe: unknown source %q", source)
	}
	if gatewayEventID == "" {
		return false, errors.New("dedupe: gateway_event_id required")
	}
	if envelopeID == "" {
		return false, errors.New("dedupe: envelope_id required")
	}
	// INSERT OR IGNORE returns rows-affected=0 on collision.
	res, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO gateway_inbound_idempotency(source, gateway_event_id, envelope_id, ingested_at)
		 VALUES (?, ?, ?, ?)`,
		string(source), gatewayEventID, envelopeID, ingestedAtMillis,
	)
	if err != nil {
		return false, fmt.Errorf("dedupe: insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("dedupe: rows affected: %w", err)
	}
	return n == 1, nil
}

// dedupeLookup is exposed for tests + manage view "have we seen this?".
// Returns the recorded envelope_id and true if (source, gatewayEventID)
// has been claimed; ("", false, nil) otherwise.
func dedupeLookup(ctx context.Context, db *sql.DB, source Source, gatewayEventID string) (string, bool, error) {
	row := db.QueryRowContext(ctx,
		`SELECT envelope_id FROM gateway_inbound_idempotency WHERE source = ? AND gateway_event_id = ?`,
		string(source), gatewayEventID,
	)
	var envID string
	err := row.Scan(&envID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("dedupe: lookup: %w", err)
	}
	return envID, true, nil
}

// brokerDB returns the underlying *sql.DB the broker uses for its own
// tables. Kept as a thin helper so the broker constructor can do the
// migration without reaching into agent package internals beyond the
// public DB() accessor.
func brokerDB(log *agent.SQLiteEventLog) *sql.DB {
	if log == nil {
		return nil
	}
	return log.DB()
}
