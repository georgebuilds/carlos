package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// ModelsEndpoint is the OpenRouter catalog endpoint. Override (var, not
// const) so tests can point at an httptest.Server without monkey-patching
// the http client.
var ModelsEndpoint = "https://openrouter.ai/api/v1/models"

// cacheFileName is the on-disk filename inside the configured cacheDir.
// 24h freshness is enforced by mtime, not by an in-file timestamp, so a
// caller can blow the cache by `touch -t`'ing the file.
const cacheFileName = "openrouter-models.json"

// ModelInfo is the curated subset of the OpenRouter /models response the
// onboarding picker actually renders. Pricing is normalized to dollars
// per million tokens so the dropdown rendering can format uniformly.
type ModelInfo struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	PromptUSDPerM     float64 `json:"prompt_usd_per_m"`
	CompletionUSDPerM float64 `json:"completion_usd_per_m"`
	CtxLen            int     `json:"ctx_len"`
}

// rawModelsResponse is the wire shape of GET /api/v1/models. We only
// pull the fields the picker needs; new fields land here as the
// catalog grows. Pricing comes as decimal strings (USD per token).
type rawModelsResponse struct {
	Data []rawModel `json:"data"`
}

type rawModel struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ContextLength int        `json:"context_length"`
	Pricing       rawPricing `json:"pricing"`
}

type rawPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// FetchModels returns the OpenRouter catalog, sorted by ascending input
// price. The 24h cache lives at <cacheDir>/openrouter-models.json; a
// corrupt or expired cache file falls back to a fresh HTTP fetch. On any
// fetch error the caller decides whether to surface or to ignore - the
// onboarding picker treats it as "use the curated list instead".
func FetchModels(ctx context.Context, cacheDir string, ttl time.Duration) ([]ModelInfo, error) {
	if cacheDir != "" {
		if cached, ok := readCache(cacheDir, ttl); ok {
			return cached, nil
		}
	}
	models, err := httpFetch(ctx)
	if err != nil {
		return nil, err
	}
	if cacheDir != "" {
		_ = writeCache(cacheDir, models)
	}
	return models, nil
}

// readCache reads + decodes the cache file when fresh. ok=false on a
// stale, missing, or corrupt cache - callers refetch.
func readCache(cacheDir string, ttl time.Duration) ([]ModelInfo, bool) {
	path := filepath.Join(cacheDir, cacheFileName)
	st, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(st.ModTime()) > ttl {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var out []ModelInfo
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// writeCache writes models atomically (temp + rename) under cacheDir.
// Failures are swallowed by the caller; the next FetchModels just
// refetches.
func writeCache(cacheDir string, models []ModelInfo) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(cacheDir, cacheFileName)
	tmp := path + ".tmp"
	b, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// httpFetch does the actual GET. Honors ctx for cancellation; uses a
// short timeout on the underlying client so a slow OpenRouter doesn't
// stall onboarding past the 800ms budget the picker enforces.
func httpFetch(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("openrouter: models endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw rawModelsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("openrouter: decode models: %w", err)
	}
	out := make([]ModelInfo, 0, len(raw.Data))
	for _, r := range raw.Data {
		out = append(out, ModelInfo{
			ID:                r.ID,
			Name:              r.Name,
			PromptUSDPerM:     parsePricePerMillion(r.Pricing.Prompt),
			CompletionUSDPerM: parsePricePerMillion(r.Pricing.Completion),
			CtxLen:            r.ContextLength,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PromptUSDPerM < out[j].PromptUSDPerM
	})
	return out, nil
}

// parsePricePerMillion converts the OpenRouter per-token decimal string
// into USD per million tokens. Returns 0 on parse failure - the
// renderer prints "$0" which reads correctly for free tiers and
// degrades politely if the upstream format ever changes.
func parsePricePerMillion(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f * 1_000_000
}
