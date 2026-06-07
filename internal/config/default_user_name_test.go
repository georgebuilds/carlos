package config

import "testing"

// TestDefaultUserNameForEnv covers the three branches the resolver
// promises: $USER set → return $USER; $USER unset → fallback Boss;
// $USER == "root" → fallback Boss (we don't want carlos calling the
// user "root" by default).
func TestDefaultUserNameForEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		set  bool
		want string
	}{
		{"unset", "", false, DefaultUserName},
		{"empty", "", true, DefaultUserName},
		{"whitespace", "  \t ", true, DefaultUserName},
		{"root", "root", true, DefaultUserName},
		{"george", "george", true, "george"},
		{"alice", "alice", true, "alice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("USER", c.env)
			} else {
				t.Setenv("USER", "")
				// t.Setenv can't unset; empty is the equivalent
				// path the resolver takes.
			}
			if got := DefaultUserNameForEnv(); got != c.want {
				t.Errorf("DefaultUserNameForEnv() = %q, want %q", got, c.want)
			}
		})
	}
}
