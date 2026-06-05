package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

func newDedupeLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if err := migrateDedupe(context.Background(), brokerDB(log)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return log
}

func TestDedupe_FirstClaimSucceeds_DuplicateRejected(t *testing.T) {
	log := newDedupeLog(t)
	db := brokerDB(log)
	ctx := context.Background()
	ok, err := claimIngest(ctx, db, SourceTelegram, "update-123", "env-1", 1_000_000)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !ok {
		t.Fatal("first claim should win")
	}
	ok, err = claimIngest(ctx, db, SourceTelegram, "update-123", "env-2", 2_000_000)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if ok {
		t.Error("second claim should lose")
	}
}

func TestDedupe_SeparatePerSource(t *testing.T) {
	log := newDedupeLog(t)
	db := brokerDB(log)
	ctx := context.Background()
	if ok, _ := claimIngest(ctx, db, SourceTelegram, "id-1", "env-tg", 0); !ok {
		t.Fatal("telegram claim should win")
	}
	if ok, _ := claimIngest(ctx, db, SourceNtfy, "id-1", "env-ntfy", 0); !ok {
		t.Error("ntfy with same id should be independent")
	}
}

func TestDedupe_Lookup(t *testing.T) {
	log := newDedupeLog(t)
	db := brokerDB(log)
	ctx := context.Background()
	if _, err := claimIngest(ctx, db, SourceNtfy, "click-1", "env-x", 42); err != nil {
		t.Fatal(err)
	}
	envID, found, err := dedupeLookup(ctx, db, SourceNtfy, "click-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
	if envID != "env-x" {
		t.Errorf("envelope id: want env-x got %q", envID)
	}

	if _, found, err := dedupeLookup(ctx, db, SourceNtfy, "missing"); err != nil || found {
		t.Errorf("missing lookup: found=%v err=%v", found, err)
	}
}

func TestDedupe_Validates(t *testing.T) {
	log := newDedupeLog(t)
	db := brokerDB(log)
	ctx := context.Background()
	if _, err := claimIngest(ctx, db, Source(""), "id", "env", 0); err == nil {
		t.Error("empty source: expected error")
	}
	if _, err := claimIngest(ctx, db, SourceTelegram, "", "env", 0); err == nil {
		t.Error("empty gateway_event_id: expected error")
	}
	if _, err := claimIngest(ctx, db, SourceTelegram, "id", "", 0); err == nil {
		t.Error("empty envelope id: expected error")
	}
}

func TestMigrateDedupe_Idempotent(t *testing.T) {
	log := newDedupeLog(t)
	if err := migrateDedupe(context.Background(), brokerDB(log)); err != nil {
		t.Errorf("re-run migrate: %v", err)
	}
}
