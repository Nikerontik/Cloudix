package app

import "testing"

// The @ is what makes a username one, and four different paths write this field
// — onboarding, Settings, the profile editor and an imported file. They all go
// through here, so this is where the shape is pinned down.
func TestNormalizeUsername(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nikerontik", "@nikerontik"},
		{"@nikerontik", "@nikerontik"},
		{"  @nikerontik  ", "@nikerontik"},
		// The UI once prefixed an @ onto a value that already had one.
		{"@@nikerontik", "@nikerontik"},
		{"@@@nikerontik", "@nikerontik"},
		{"@ nikerontik", "@nikerontik"},
		// Nothing to work with stays nothing; callers decide if that is allowed.
		{"", ""},
		{"   ", ""},
		{"@", ""},
		{"@@@", ""},
	}
	for _, c := range cases {
		if got := normalizeUsername(c.in); got != c.want {
			t.Errorf("normalizeUsername(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}
