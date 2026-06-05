package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cache is the process-wide registry of opened vaults, keyed by
// absolute path. The 7 notes_* tools share one Cache so a vault opened
// by notes_get is reused by notes_search, notes_backlinks, etc.
//
// Opens are lazy + idempotent: calling Open(path) twice returns the
// SAME *VaultIndex without re-walking the tree. Concurrent first-opens
// for the same path are coalesced via sync.Once per entry.
type Cache struct {
	excludes []string

	mu     sync.Mutex
	vaults map[string]*cacheEntry
}

// cacheEntry wraps a VaultIndex with a sync.Once so the lazy build
// happens at most once per path. err is captured under the same Once
// so a failed first-open propagates to every concurrent caller without
// re-trying.
type cacheEntry struct {
	once  sync.Once
	index *VaultIndex
	err   error
}

// NewCache returns an empty Cache. excludes is the shared glob list
// from cfg.Vault.Exclude — every VaultIndex opened through this Cache
// uses the same patterns. Callers that need per-vault excludes (out of
// scope for v0) can construct a separate Cache.
func NewCache(excludes []string) *Cache {
	out := &Cache{
		excludes: append([]string(nil), excludes...),
		vaults:   map[string]*cacheEntry{},
	}
	return out
}

// Open returns the VaultIndex for vault rooted at path. Lazy build:
// first call walks the tree, later calls return the cached *VaultIndex.
//
// path is canonicalised (~ expansion + filepath.Abs + Clean) before
// lookup, so `~/.carlos/vault`, `/Users/me/.carlos/vault`, and
// `/Users/me/.carlos/vault/` all hit the same cache entry.
//
// An open failure (missing dir, parse error, walk error) is cached: a
// later Open(path) for the same broken path returns the same error
// without retrying. Use ResetPath if a caller wants to force a retry.
func (c *Cache) Open(path string) (*VaultIndex, error) {
	if path == "" {
		return nil, ErrNoVaultConfigured
	}
	abs, err := canonicalisePath(path)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	entry, ok := c.vaults[abs]
	if !ok {
		entry = &cacheEntry{}
		c.vaults[abs] = entry
	}
	c.mu.Unlock()

	entry.once.Do(func() {
		vi, oerr := newVaultIndex(abs, c.excludes)
		entry.index = vi
		entry.err = oerr
	})
	return entry.index, entry.err
}

// ResetPath drops the cached entry for path, letting a subsequent
// Open(path) re-walk from scratch. Useful in tests + reserved for a
// future "vault watcher detected a directory rename" code path.
func (c *Cache) ResetPath(path string) {
	abs, err := canonicalisePath(path)
	if err != nil {
		return
	}
	c.mu.Lock()
	delete(c.vaults, abs)
	c.mu.Unlock()
}

// Keys returns every cached vault path, sorted. Test-only; not used
// from the tool layer.
func (c *Cache) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.vaults))
	for k := range c.vaults {
		out = append(out, k)
	}
	return out
}

// canonicalisePath expands ~, resolves to an absolute path, and
// applies filepath.Clean. Returns the cleaned path or an error if the
// home directory cannot be resolved for a ~-prefixed path.
func canonicalisePath(path string) (string, error) {
	if path == "" {
		return "", ErrNoVaultConfigured
	}
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("notes: abs %s: %w", path, err)
	}
	return filepath.Clean(abs), nil
}

// expandHome resolves a leading `~` (alone or with `/`) against the
// current user's home directory. Other tilde forms (`~user/`) are not
// supported in v0 — callers using those should pass an absolute path.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return "", errors.New("notes: only `~` and `~/` tilde forms are supported")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("notes: user home: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// VaultConfig is the minimal interface the tool layer's resolve helper
// expects: just the configured vault Path. The actual config struct
// lives in internal/config but the notes package can't import that
// (config imports schedule which imports tools — circular). Duck-typing
// via an interface keeps the dependency arrow one-way.
type VaultConfig interface {
	VaultPath() string
}

// ResolveVaultPath returns the effective vault path for a tool call:
// per-call wins over cfg.Vault.Path; both empty returns
// ErrNoVaultConfigured. ~ expansion is applied to the chosen path.
//
// This is the load-bearing function for the "ONE vault by default,
// optional per-call override" contract from the proposal.
func ResolveVaultPath(cfgPath, perCallVault string) (string, error) {
	chosen := strings.TrimSpace(perCallVault)
	if chosen == "" {
		chosen = strings.TrimSpace(cfgPath)
	}
	if chosen == "" {
		return "", ErrNoVaultConfigured
	}
	return canonicalisePath(chosen)
}
