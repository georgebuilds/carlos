// scrub.go — model-name scrubber for provider error envelopes.
//
// When a provider returns an error message that names the underlying
// model ("I am Gemini, ..."), forwarding it verbatim into the chat
// undermines the "you are carlos" framing the system prompt sets up.
// ScrubModelName rewrites those name reveals to "carlos" before the
// agent loop sees the event.
//
// Scope: this is a conservative pattern scrub, NOT a general-purpose
// content filter. It targets standalone "I am X" / "I'm X" / bare
// "<ModelName>:" prefixes where X is a known provider/model identity
// word. We deliberately do NOT scrub substrings inside larger words
// (so "gpt-4-turbo-anthropic-edition" or "claude-sonnet-4-6" in a
// model-id context stays intact); the only goal is to catch the
// "model reveals its own name to the user" failure mode.
//
// If the scrub would mangle a legitimate message (e.g. a code review
// of an Anthropic SDK file), we prefer to leave the leak rather than
// damage the content — per the house rule "better to leave a leak
// than scrub a legit message".

package providers

import (
	"regexp"
)

// modelNamePatterns is the list of (regex, replacement) pairs we run
// over an error message. Order matters: the longer / more specific
// patterns ("I am Gemini") come first so the bare-word fallback only
// fires on the residue. All patterns are case-insensitive and require
// word-boundary anchoring on the model name to avoid mangling
// substrings inside larger identifiers ("claude-sonnet-4-6" is fine;
// "Claude:" alone gets rewritten).
var modelNamePatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// "I am <Model>" / "I'm <Model>" — the canonical identity reveal.
	// Greedy on the model token so "I am Gemini Pro" still scrubs.
	{
		re:   regexp.MustCompile(`(?i)\bI(?:'m| am)\s+(?:Gemini|Claude|ChatGPT|GPT-?\d+(?:\.\d+)?(?:-turbo)?|GPT|Llama|Mistral|Qwen)\b`),
		repl: "I am carlos",
	},
	// "My name is <Model>" — second-most-common identity reveal.
	{
		re:   regexp.MustCompile(`(?i)\bMy name is\s+(?:Gemini|Claude|ChatGPT|GPT-?\d+(?:\.\d+)?(?:-turbo)?|GPT|Llama|Mistral|Qwen)\b`),
		repl: "My name is carlos",
	},
	// Bare standalone model name as a sentence-leading identity claim
	// ("Gemini: <message>" or "Claude here, ..."). Anchored on a
	// punctuation/whitespace boundary so it doesn't fire on
	// "claude-sonnet-4-6" or "the openai/gpt-4o route".
	{
		re:   regexp.MustCompile(`(?i)(^|[\s(\[])(?:Gemini|ChatGPT)(\s+(?:here|speaking)\b|:)`),
		repl: "${1}carlos${2}",
	},
}

// ScrubModelName rewrites a provider error message so model-name
// reveals are replaced with "carlos". The original underlying error
// is preserved as an unwrap target so callers can still errors.Is /
// errors.As against it, but its Error() string is no longer
// reachable through the scrubbed envelope's Error() (that would
// undo the scrub at the display layer). nil in → nil out.
//
// The returned error's Error() reads as the scrubbed string with a
// "carlos: " prefix when scrubbing changed it; the original error is
// returned unchanged when nothing matched. This keeps low-noise paths
// (the vast majority of provider errors, which don't leak model
// names) free of an extra "carlos:" prefix the rest of the code
// already adds at the cmd boundary.
func ScrubModelName(err error) error {
	if err == nil {
		return nil
	}
	orig := err.Error()
	scrubbed := scrubModelNameString(orig)
	if scrubbed == orig {
		return err
	}
	return &scrubbedError{msg: "carlos: " + scrubbed, cause: err}
}

// scrubbedError carries the scrubbed display string while keeping the
// original error reachable via Unwrap (so errors.Is / errors.As keep
// working against sentinels and typed errors deeper in the chain).
// We deliberately do NOT format the cause into Error() — that would
// reintroduce the model-name leak we just scrubbed.
type scrubbedError struct {
	msg   string
	cause error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.cause }

// ScrubModelNameString is the string-level scrub the cmd/carlos
// stderr path uses. Exposed for the cmd boundary where the error has
// already been Error()-formatted into a string (the wrapping path
// ScrubModelName uses isn't needed there).
func ScrubModelNameString(s string) string {
	return scrubModelNameString(s)
}

func scrubModelNameString(s string) string {
	out := s
	for _, p := range modelNamePatterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}
