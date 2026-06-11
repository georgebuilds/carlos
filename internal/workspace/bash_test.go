package workspace

import "testing"

func TestIsReadOnly_AllowsAllowlistedVerbs(t *testing.T) {
	cases := []string{
		"ls",
		"ls -la",
		"ls /tmp",
		"pwd",
		"cat README.md",
		"head -n 20 main.go",
		"tail -f log.txt",
		"wc -l main.go",
		"file binary",
		"which go",
		"echo hello",
	}
	for _, c := range cases {
		if !IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = false; want true", c)
		}
	}
}

func TestIsReadOnly_AllowsGitInspection(t *testing.T) {
	cases := []string{
		"git status",
		"git status --short",
		"git diff",
		"git diff HEAD~1",
		"git log --oneline -10",
		"git show HEAD",
		"git blame main.go",
		"git branch",
		"git ls-files",
		"git rev-parse HEAD",
		"git remote -v",
	}
	for _, c := range cases {
		if !IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = false; want true", c)
		}
	}
}

func TestIsReadOnly_DeniesBuildAndTestTools(t *testing.T) {
	// User explicitly excluded these. Discipline check: if any of
	// these flip to true, the v1 allowlist policy has regressed.
	cases := []string{
		"cargo build",
		"cargo test",
		"cargo check",
		"go test ./...",
		"go build ./...",
		"npm install",
		"npm run build",
		"yarn install",
		"pnpm install",
		"make",
		"make test",
		"cmake .",
		"bazel build //...",
		"ninja",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; v1 allowlist must NOT include build/test tools", c)
		}
	}
}

func TestIsReadOnly_DeniesMutatingFilesystem(t *testing.T) {
	cases := []string{
		"rm file",
		"rm -rf dir",
		"mv a b",
		"cp a b",
		"mkdir foo",
		"touch file",
		"chmod 600 file",
		"chown me file",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; mutating fs verbs must prompt", c)
		}
	}
}

func TestIsReadOnly_DeniesGitWriteSubcommands(t *testing.T) {
	cases := []string{
		"git commit -m msg",
		"git push",
		"git push --force",
		"git reset --hard",
		"git checkout main",
		"git merge feat",
		"git rebase main",
		"git stash",
		"git pull",
		"git fetch",
		"git clean -fd",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; git write subcommands must prompt", c)
		}
	}
}

func TestIsReadOnly_DeniesGitWriteFlags(t *testing.T) {
	// `git config` is read-only by default but `--add` / `--unset`
	// turn it into a write.
	cases := []string{
		"git config --add user.name x",
		"git config --unset user.name",
		"git branch -D feature",
		"git branch --delete feature",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; write flag should deny verb", c)
		}
	}
}

func TestIsReadOnly_DeniesShellMetacharacters(t *testing.T) {
	cases := []string{
		"ls; rm -rf /",
		"ls && rm a",
		"ls || true",
		"ls | grep foo",
		"ls > out.txt",
		"ls < in",
		"echo `whoami`",
		"echo $(date)",
		"ls >> log",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; shell metacharacters must deny", c)
		}
	}
}

func TestIsReadOnly_DeniesUnknownVerbs(t *testing.T) {
	cases := []string{
		"docker ps",
		"kubectl get pods",
		"curl https://example.com",
		"wget url",
		"ssh host",
		"sudo ls",
		"some-random-tool",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; unknown verbs must deny", c)
		}
	}
}

func TestIsReadOnly_DeniesEmptyAndBlank(t *testing.T) {
	if IsReadOnly("") {
		t.Error("empty cmd should deny")
	}
	if IsReadOnly("   ") {
		t.Error("whitespace-only cmd should deny")
	}
}

func TestIsReadOnly_DeniesNewlineBypass(t *testing.T) {
	// strings.Fields treats \n and \r as whitespace - without an
	// explicit metachar block, `git status\nrm -rf /tmp/x` would
	// tokenize back to `git status` and slip through.
	cases := []string{
		"git status\nrm -rf /tmp/x",
		"git status\r\nrm -rf /tmp/x",
		"git status\rrm -rf /tmp/x",
		"ls\necho pwned",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; newline/CR must deny", c)
		}
	}
}

func TestIsReadOnly_DeniesBackgroundAmpersand(t *testing.T) {
	// `cat foo & rm` is a plain `&` (shell backgrounding), not `&&`,
	// so the substring metachar check misses it. Token-level reject.
	cases := []string{
		"cat foo & rm",
		"ls & pwd",
		"git status & whoami",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; bare & must deny", c)
		}
	}
}

func TestIsReadOnly_DeniesGitOutputFlag(t *testing.T) {
	// `git log --output=PATH` and `git blame --output=PATH` write
	// arbitrary files. Must catch both `--output=PATH` (one token
	// with =) and `--output PATH` (two tokens).
	cases := []string{
		"git log --output=/tmp/x",
		"git log --output /tmp/x",
		"git blame --output=/tmp/x file",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; --output flag must deny", c)
		}
	}
}

func TestIsReadOnly_DeniesGitPositionalWrites(t *testing.T) {
	// Positional write forms that the verb/subcommand allowlist
	// would otherwise pass through.
	cases := []string{
		"git remote add origin git@x:y/z",
		"git remote remove origin",
		"git remote rm origin",
		"git remote set-url origin URL",
		"git remote rename a b",
		"git branch newbranch",
		"git config user.email a@b.com",
	}
	for _, c := range cases {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; positional write form must deny", c)
		}
	}
}

func TestIsReadOnly_AllowsGitReadFormsAfterPositionalRejector(t *testing.T) {
	// Sanity: the positional gate must not over-reject legitimate
	// read forms.
	cases := []string{
		"git remote",
		"git remote -v",
		"git remote show origin",
		"git branch",
		"git branch -a",
		"git branch --list",
		"git branch --show-current",
		"git branch --contains HEAD",
		"git config user.email",
		"git log --oneline -5",
	}
	for _, c := range cases {
		if !IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = false; legitimate read form must allow", c)
		}
	}
}

func TestIsReadOnly_GitConfig_CredentialHelper(t *testing.T) {
	// `git config` is allowed for benign reads (`git config user.email`)
	// but must reject anything that reveals credential.* keys or dumps
	// the whole config. credential.helper=store on Linux holds plaintext
	// tokens, so even reading which helper is configured is leaky.
	cases := []struct {
		cmd  string
		want bool // want IsReadOnly to return this
	}{
		// Should BLOCK: broad dumpers.
		{"git config --list", false},
		{"git config -l", false},
		{"git config --list-all", false},
		// Should BLOCK: explicit credential.* reads.
		{"git config --get credential.helper", false},
		{"git config --get-all credential.helper", false},
		{"git config --get-urlmatch credential.helper https://github.com/", false},
		{"git config credential.helper", false},
		// Should BLOCK: case-insensitive credential.* match.
		{"git config Credential.Helper", false},
		{"git config CREDENTIAL.HELPER", false},
		// Should BLOCK: dotted subkey under credential.
		{"git config credential.helper.https://github.com", false},
		// Should BLOCK: regex dumpers (could pattern-match credential.*).
		{"git config --get-regexp .*", false},
		{"git config --get-regex credential.helper", false},
		{"git config --get-regexp credential\\..*", false},

		// Should ALLOW: benign single-key reads.
		{"git config user.email", true},
		{"git config --get user.email", true},
		{"git config user.name", true},
		{"git config core.editor", true},
		{"git config --show-origin user.email", true},
	}
	for _, c := range cases {
		got := IsReadOnly(c.cmd)
		if got != c.want {
			t.Errorf("IsReadOnly(%q) = %v; want %v", c.cmd, got, c.want)
		}
	}
}
