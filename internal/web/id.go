package web

import (
	cryptorand "crypto/rand"
	"sync"

	"github.com/oklog/ulid/v2"
)

// newID mints a ULID for web-owned objects (currently thread groups).
// Mirrors cmd/carlos's session-id minting: monotonic entropy seeded from
// crypto/rand, guarded by a mutex because ulid.Monotonic is not
// goroutine-safe. Thread (session) ids are minted by the backend, not
// here - this is only for rows the web layer owns (web_groups).
var (
	idMu      sync.Mutex
	idEntropy = ulid.Monotonic(cryptorand.Reader, 0)
)

func newID() (string, error) {
	idMu.Lock()
	defer idMu.Unlock()
	id, err := ulid.New(ulid.Now(), idEntropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
