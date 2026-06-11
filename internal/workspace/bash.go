// bash.go - the curated read-only verb classifier used by Policy.
//
// The premise: when the user trusts the current workspace, carlos can
// run a small, deliberate set of read-only shell commands without
// prompting on every call. The set is intentionally tiny. The user's
// directive on the v1 allowlist (verbatim):
//
//	"i think initial allowlist should only be read stuff like git
//	 status, git diff, ls, pwd, cat, tail, head, etc. not cargo/npm/
//	 yarn operations or make anything like that"
//
// Discipline:
//   - No build/test/install tools. Ever. cargo/npm/yarn/pnpm/go test/
//     pytest/make/cmake/ninja/bazel all stay behind a prompt because
//     they can execute arbitrary code paths (scripts, plugins,
//     codegen) that read-only classifiers can't audit.
//   - No commands that mutate the filesystem. cp/mv/rm/mkdir/touch
//     are all denied even though some forms are technically read-
//     ish.
//   - No commands with shell metacharacters (;, &&, ||, |, >, <, `,
//     $(). The classifier rejects the entire string, not just the
//     verb. If the user wants to chain reads, they prompt once.
//   - git is special: only an explicit subcommand whitelist passes.
//     `git config`, `git push`, `git reset` etc. all fall through.
//
// Adding a verb here is a security review. Removing one is free.

package workspace

import (
	"strings"
)

// readOnlyVerbs maps a top-level command name to the set of allowed
// FIRST POSITIONAL ARGUMENT values. nil means "all forms of this
// verb are allowed" (only true for verbs that can't take a sub-
// command, like `pwd`).
//
// git is the only multi-tier entry: its second token (the subcommand)
// is the gate. Subcommand allowlist must stay read-only.
var readOnlyVerbs = map[string]map[string]bool{
	// File system reads.
	"ls":    nil,
	"pwd":   nil,
	"cat":   nil,
	"head":  nil,
	"tail":  nil,
	"wc":    nil,
	"file":  nil,
	"which": nil,
	"echo":  nil,

	// git - only inspection subcommands. Anything that writes
	// (commit, push, reset, checkout, merge, rebase, stash, config
	// --add) is denied.
	"git": {
		"status":    true,
		"diff":      true,
		"log":       true,
		"show":      true,
		"blame":     true,
		"branch":    true, // listing form is read; --delete still prompts via metachar/flag check below
		"ls-files":  true,
		"ls-tree":   true,
		"rev-parse": true,
		"describe":  true,
		"remote":    true, // `git remote -v` is informational
		"config":    true, // read-only forms only - see disallowedFlags below
	},
}

// disallowedShellMetacharacters - presence of any of these in the
// command string disqualifies it from the read-only allowlist. The
// goal is to make the classifier dumb on purpose: "git status" is
// allowed; `"git status && rm -rf /"` is not.
var disallowedShellMetacharacters = []string{
	";", "&&", "||", "|", ">", "<", "`", "$(", "$((", ">>", "<<", "\n", "\r",
}

// disallowedFlagsByVerb - fine-grained denial for verbs that have a
// hidden write form behind a flag (typically `--delete`, `--add`,
// `--unset`). When a token in the command matches any string here,
// the verb-level allow is overridden and the classifier rejects.
var disallowedFlagsByVerb = map[string][]string{
	"git": {
		"--add",
		"--unset",
		"--unset-all",
		"--delete",
		"-D",
		"--replace-all",
		"--rename-section",
		"--remove-section",
		"--edit",
		"--output",
	},
}

// gitRemoteWriteSubcommands - positional sub-subcommands of
// `git remote` that mutate config. Bare `git remote` (list) and
// `git remote -v` / `git remote show NAME` remain read-only.
var gitRemoteWriteSubcommands = map[string]bool{
	"add":          true,
	"remove":       true,
	"rm":           true,
	"set-url":      true,
	"set-head":     true,
	"set-branches": true,
	"rename":       true,
	"update":       true,
	"prune":        true,
	"add-fetch":    true,
}

// gitBranchReadValueFlags - `git branch` flags that consume the next
// token as their value. We skip the value so it isn't counted as a
// positional (which would otherwise look like a branch-create form).
var gitBranchReadValueFlags = map[string]bool{
	"--contains":    true,
	"--no-contains": true,
	"--merged":      true,
	"--no-merged":   true,
	"--points-at":   true,
}

// IsReadOnly reports whether `cmd` is a single invocation of a known
// read-only verb with no shell-metachar escape hatch. False is the
// safe default: any uncertainty (parse failure, unknown verb,
// metacharacter, redirect) returns false and lets the LayeredApprover
// fall through to the prompt path.
func IsReadOnly(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	for _, bad := range disallowedShellMetacharacters {
		if strings.Contains(cmd, bad) {
			return false
		}
	}
	tokens := tokenize(cmd)
	if len(tokens) == 0 {
		return false
	}
	// Plain `&` as its own token is shell backgrounding -
	// `cat foo & rm` splits into ["cat","foo","&","rm"] and slips
	// past the `&&` substring check above.
	for _, tok := range tokens {
		if tok == "&" {
			return false
		}
	}
	verb := tokens[0]
	subAllow, ok := readOnlyVerbs[verb]
	if !ok {
		return false
	}
	// Multi-tier verbs: require the second token to be in the
	// allowed subcommand set. (nil map means "no subcommand
	// constraint" - only true for single-form verbs like pwd.)
	if subAllow != nil {
		if len(tokens) < 2 {
			return false
		}
		if !subAllow[tokens[1]] {
			return false
		}
	}
	// Flag-level denial: a verb with hidden write forms can still
	// be rejected by a token-level scan. We match both bare
	// (`--output`) and value-attached (`--output=PATH`) forms.
	if bad := disallowedFlagsByVerb[verb]; len(bad) > 0 {
		for _, tok := range tokens[1:] {
			for _, denied := range bad {
				if tok == denied || strings.HasPrefix(tok, denied+"=") {
					return false
				}
			}
		}
	}
	// git is special: several subcommands have positional write
	// forms (`git remote add ORIGIN URL`, `git branch NEWBRANCH`,
	// `git config user.email VAL`) that pass the subcommand and
	// flag gates above. Catch them here.
	if verb == "git" && !isReadOnlyGitPositional(tokens) {
		return false
	}
	return true
}

// isReadOnlyGitPositional inspects the positional arguments of a
// `git <subcommand> ...` invocation and rejects forms that mutate
// state. Returns true if the form is read-only (or not one of the
// subcommands we need to gate positionally).
func isReadOnlyGitPositional(tokens []string) bool {
	if len(tokens) < 2 {
		return true
	}
	switch tokens[1] {
	case "remote":
		// Bare `git remote`, `git remote -v`, and `git remote show NAME`
		// are read-only. Any of the write sub-subcommands appearing as
		// a non-flag positional rejects.
		for _, tok := range tokens[2:] {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			if gitRemoteWriteSubcommands[tok] {
				return false
			}
			// `show` and any name argument after it are fine; first
			// non-flag positional decides.
			return true
		}
		return true
	case "branch":
		// Bare `git branch` lists. A non-flag positional after
		// `branch` is the new branch name = create. Some read flags
		// take a value (--contains HEAD); skip that value.
		for i := 2; i < len(tokens); i++ {
			tok := tokens[i]
			if strings.HasPrefix(tok, "-") {
				// `--contains=HEAD` carries its value inline.
				if idx := strings.IndexByte(tok, '='); idx >= 0 {
					continue
				}
				if gitBranchReadValueFlags[tok] {
					i++ // consume the value
				}
				continue
			}
			// Non-flag positional = branch creation.
			return false
		}
		return true
	case "config":
		// Two-part gate.
		//
		// (1) Credential-helper exfiltration block. `credential.helper`
		// on Linux defaults to `store` for many users, which holds
		// plaintext tokens in ~/.git-credentials. A read-only-looking
		// `git config --get credential.helper` reveals which helper
		// is in use, and `--list` / `--get-regexp` forms can dump
		// the whole config including credential.* keys. Reject any
		// positional that names a credential.* key (case-insensitive,
		// including dotted subkeys like `credential.helper.https://...`)
		// and reject the broad dumpers `--list`/`-l`/`--list-all` and
		// the regex getters `--get-regexp`/`--get-regex`. Scoped to
		// the config case so `-l` etc. don't clobber other git verbs.
		for i := 2; i < len(tokens); i++ {
			tok := tokens[i]
			lower := strings.ToLower(tok)
			if strings.HasPrefix(lower, "credential.") {
				return false
			}
			switch lower {
			case "--list", "-l", "--list-all", "--get-regexp", "--get-regex":
				return false
			}
		}
		// (2) 1 non-flag positional after `config` is a read
		// (`git config user.email`). 2+ is a write
		// (`git config user.email VAL`). Flags don't count.
		positionals := 0
		for _, tok := range tokens[2:] {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			positionals++
		}
		return positionals <= 1
	}
	return true
}

// tokenize is a deliberately simple word splitter - we already
// rejected shell metacharacters above, so plain whitespace splitting
// is correct here. We strip surrounding quotes on each token so
// `cat "Makefile"` tokenizes to ["cat", "Makefile"].
func tokenize(cmd string) []string {
	parts := strings.Fields(cmd)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
