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
