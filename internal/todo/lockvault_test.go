package todo

import (
	"sync"
	"testing"
)

// lockVault returns a working unlock and serializes concurrent holders of
// the same root - exercising the comma-ok mutex retrieval added in the
// type-safety pass.
func TestLockVault_SerializesSameRoot(t *testing.T) {
	const root = "/tmp/vault-A"
	var counter int
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockVault(root)
			defer unlock()
			counter++ // protected by the per-root mutex
		}()
	}
	wg.Wait()
	if counter != 50 {
		t.Errorf("counter = %d, want 50 (lost updates imply broken mutual exclusion)", counter)
	}
}

// Distinct roots get distinct mutexes (no false contention) and both unlock
// cleanly.
func TestLockVault_DistinctRootsIndependent(t *testing.T) {
	u1 := lockVault("/tmp/vault-X")
	u2 := lockVault("/tmp/vault-Y") // must not deadlock against the first
	u2()
	u1()
}
