package memory

import (
	"errors"
	"strings"
	"testing"
)

// fakeRows is a hand-rolled rows interface for scanSummaries. Drives
// the Scan-error and rows.Err()-after-iteration branches without
// standing up a mock SQL driver.
type fakeRows struct {
	nextCalls int
	maxIter   int
	scanErr   error
	finalErr  error
}

func (r *fakeRows) Next() bool {
	if r.nextCalls >= r.maxIter {
		return false
	}
	r.nextCalls++
	return true
}

func (r *fakeRows) Scan(dest ...any) error { return r.scanErr }
func (r *fakeRows) Err() error              { return r.finalErr }

// TestScanSummaries_ScanError covers the Scan-error branch: a row
// surfaces, Scan fails, the wrapped error reaches the caller.
func TestScanSummaries_ScanError(t *testing.T) {
	rows := &fakeRows{maxIter: 1, scanErr: errors.New("scan boom")}
	_, err := scanSummaries(rows)
	if err == nil {
		t.Fatal("expected scan error to surface")
	}
	if !strings.Contains(err.Error(), "scan boom") {
		t.Errorf("error %q should wrap underlying scan error", err)
	}
}

// TestScanSummaries_RowsErr covers the post-iteration rows.Err()
// branch: iteration completes cleanly, then Err() surfaces a deferred
// driver error.
func TestScanSummaries_RowsErr(t *testing.T) {
	rows := &fakeRows{maxIter: 0, finalErr: errors.New("late driver error")}
	_, err := scanSummaries(rows)
	if err == nil {
		t.Fatal("expected rows.Err() to surface")
	}
	if !strings.Contains(err.Error(), "late driver error") {
		t.Errorf("error %q should expose underlying rows.Err()", err)
	}
}
