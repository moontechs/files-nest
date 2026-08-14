package filestore

import "testing"

func TestSafePathSegment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{".", ""},
		{"..", ""},
		{"../etc/passwd", "etc_passwd"}, // leading ".." trimmed, '/' -> '_'
		{"foo/bar", "foo_bar"},          // '/' -> '_'
		{"foo\\bar", "foo_bar"},         // backslash -> '_'
		{"  spaced  ", "spaced"},        // trim spaces
		{"...dots...", "dots"},          // trim dots
		{"a\x00b", "ab"},                // NULL removed
		{"a\x1Fb", "ab"},                // control char removed
		{"正常", "正常"},                    // unicode preserved
		{"2024-03-15", "2024-03-15"},    // normal date preserved
	}
	for _, c := range cases {
		got := SafePathSegment(c.in)
		if got != c.want {
			t.Errorf("SafePathSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPlanDestination_MalformedDateNoTraversal ensures a malformed/adversarial
// creation_date cannot escape the organized tree via path traversal.
func TestPlanDestination_MalformedDateNoTraversal(t *testing.T) {
	m := New(t.TempDir())
	plan := m.PlanDestination("../../../etc/passwd", "", "IMG.jpg", "bk-1")
	// The relative path must stay under organized/ and contain no ".." segment
	// and no raw '/' from the date (it must have been sanitized to '_').
	if !startsWith(plan.Rel, "organized/") {
		t.Fatalf("rel %q must start with organized/", plan.Rel)
	}
	if containsDoubleDot(plan.Rel) {
		t.Fatalf("rel %q must not contain '..' traversal segment", plan.Rel)
	}
	// The absolute path must remain under the storage root.
	if !startsWith(plan.Abs, m.StoragePath()) {
		t.Fatalf("abs %q must be under storage root %q", plan.Abs, m.StoragePath())
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func containsDoubleDot(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' && (i+2 == len(s) || s[i+2] == '/') && (i == 0 || s[i-1] == '/') {
			return true
		}
	}
	return false
}
