package onboarding

import "strings"

// ModelSuggestion is one entry in the onboarding model picker. Slug is
// the exact string the provider's API expects; Label is a friendly
// human name shown in the dropdown; Note is a short one-liner tag
// ("flagship", "fast", "reasoning") rendered dimly to the right.
//
// Order inside providerModels matters: position 0 is the suggested
// default for that provider. suggestedDefaultModel reads from there.
type ModelSuggestion struct {
	Slug  string
	Label string
	Note  string
}

// providerModels returns the curated list of model suggestions for the
// onboarding picker. The list is small by design — we're picking the
// "obvious starting choice" PLUS a tasteful spread of alternatives,
// not an exhaustive catalog.
//
// The picker is non-restrictive: users can ignore the dropdown and
// type any slug they want. The list is a discoverability aid, not a
// gate. That's why we don't have to chase every released model — a
// user who knows they want some niche fine-tune will type it in.
//
// Sources:
//
//   - Anthropic native API: model IDs as documented by Anthropic
//     (kebab-case, no provider prefix).
//   - OpenAI native API: model IDs as documented by OpenAI.
//   - OpenRouter: slugs from https://openrouter.ai/api/v1/models —
//     "<vendor>/<model>" namespacing. Every entry on OpenRouter that
//     accepts chat/completions supports streaming via the standard
//     stream: true parameter; we only include chat completion models.
//   - Ollama: tags from the public ollama registry; users pull these
//     with `ollama pull <tag>` before first use.
func providerModels(provider string) []ModelSuggestion {
	switch provider {
	case "anthropic":
		return []ModelSuggestion{
			{Slug: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Note: "workhorse — default"},
			{Slug: "claude-opus-4-7", Label: "Claude Opus 4.7", Note: "strongest reasoning"},
			{Slug: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Note: "fastest, cheapest"},
		}
	case "openai":
		return []ModelSuggestion{
			{Slug: "gpt-5", Label: "GPT-5", Note: "general-purpose — default"},
			{Slug: "gpt-5-mini", Label: "GPT-5 Mini", Note: "fast, cheap"},
			{Slug: "gpt-5-pro", Label: "GPT-5 Pro", Note: "extended thinking"},
			{Slug: "o4-mini", Label: "o4 Mini", Note: "reasoning, compact"},
		}
	case "gemini":
		// Native Google Generative Language API. Slugs match Google's
		// model IDs (no provider prefix — the prefix is OpenRouter
		// convention). Default is gemini-3.5-flash (non-lite) — the
		// "fast workhorse" tier most users want.
		return []ModelSuggestion{
			{Slug: "gemini-3.5-flash", Label: "Gemini 3.5 Flash", Note: "fast workhorse — default"},
			{Slug: "gemini-3.5-pro", Label: "Gemini 3.5 Pro", Note: "strongest reasoning"},
			{Slug: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash Lite", Note: "cheapest, fastest"},
			{Slug: "gemini-3.1-flash", Label: "Gemini 3.1 Flash", Note: "previous Flash"},
			{Slug: "gemini-3.1-pro", Label: "Gemini 3.1 Pro", Note: "previous Pro"},
			{Slug: "gemini-flash-latest", Label: "Gemini Flash (latest)", Note: "moving alias"},
			{Slug: "gemini-pro-latest", Label: "Gemini Pro (latest)", Note: "moving alias"},
			{Slug: "gemini-2.5-flash", Label: "Gemini 2.5 Flash", Note: "older but cheap"},
			{Slug: "gemini-2.5-pro", Label: "Gemini 2.5 Pro", Note: "older flagship"},
		}
	case "openrouter":
		// Default keeps Gemini 3.5 Flash (fast & cheap). The rest is a
		// spread across vendors so a user trying carlos for the first
		// time on OpenRouter can sample any major lab in one keystroke.
		return []ModelSuggestion{
			{Slug: "google/gemini-3.5-flash", Label: "Gemini 3.5 Flash", Note: "fast & cheap — default"},
			{Slug: "google/gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash Lite", Note: "cheapest"},
			{Slug: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Note: "Claude workhorse"},
			{Slug: "anthropic/claude-opus-4.8", Label: "Claude Opus 4.8", Note: "Claude flagship"},
			{Slug: "openai/gpt-5.5", Label: "GPT-5.5", Note: "OpenAI flagship"},
			{Slug: "openai/gpt-5.4-mini", Label: "GPT-5.4 Mini", Note: "fast OpenAI"},
			{Slug: "deepseek/deepseek-v4-pro", Label: "DeepSeek V4 Pro", Note: "open-weights flagship"},
			{Slug: "deepseek/deepseek-v4-flash", Label: "DeepSeek V4 Flash", Note: "fast DeepSeek"},
			{Slug: "qwen/qwen3.7-plus", Label: "Qwen 3.7 Plus", Note: "Qwen mainstream"},
			{Slug: "qwen/qwen3.6-flash", Label: "Qwen 3.6 Flash", Note: "fast Qwen"},
			{Slug: "minimax/minimax-m3", Label: "MiniMax M3", Note: "MiniMax latest"},
		}
	case "ollama":
		// Local: users pull these tags with `ollama pull <tag>`. The
		// list is tiny — Ollama's registry has hundreds and tastes
		// vary; we pick a few that work well on consumer hardware.
		return []ModelSuggestion{
			{Slug: "llama3.1:8b", Label: "Llama 3.1 8B", Note: "general — default"},
			{Slug: "llama3.3:70b", Label: "Llama 3.3 70B", Note: "needs lots of RAM"},
			{Slug: "qwen3:7b", Label: "Qwen 3 7B", Note: "good general"},
			{Slug: "qwen3:14b", Label: "Qwen 3 14B", Note: "stronger Qwen"},
			{Slug: "deepseek-r1:7b", Label: "DeepSeek R1 7B", Note: "reasoning"},
			{Slug: "mistral-nemo:12b", Label: "Mistral Nemo 12B", Note: "long context"},
		}
	}
	return nil
}

// filterModels returns suggestions for `provider` whose Slug or Label
// matches `query` case-insensitively. Empty query returns the full
// list. Used by the model screen to drive the dropdown.
//
// Matching is substring on Slug + Label combined so a user typing
// "flash" finds Gemini Flash, Gemini Flash Lite, and DeepSeek Flash
// across providers (within the active provider's list).
func filterModels(provider, query string) []ModelSuggestion {
	all := providerModels(provider)
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return all
	}
	out := make([]ModelSuggestion, 0, len(all))
	for _, s := range all {
		haystack := strings.ToLower(s.Slug + " " + s.Label + " " + s.Note)
		if strings.Contains(haystack, q) {
			out = append(out, s)
		}
	}
	return out
}
