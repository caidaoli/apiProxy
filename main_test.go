package main

import "testing"

func TestFindMatchingPrefixPrefersLongest(t *testing.T) {
	path := "/openai/v1/chat"
	prefixes := []string{"/openai/v1", "/openai"}

	match, ok := findMatchingPrefix(path, prefixes)
	if !ok {
		t.Fatal("expected to find matching prefix")
	}
	if match != "/openai/v1" {
		t.Fatalf("expected /openai/v1, got %s", match)
	}
}

func TestMatchesPrefix(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		prefix  string
		expects bool
	}{
		{"exact", "/api", "/api", true},
		{"nested", "/api/v1", "/api", true},
		{"boundary", "/api2", "/api", false},
		{"trailingSlash", "/api/v1", "/api/", true},
		{"root", "/anything", "/", true},
		{"noMatch", "/foo", "/bar", false},
	}

	for _, tt := range tests {
		if got := matchesPrefix(tt.path, tt.prefix); got != tt.expects {
			t.Fatalf("%s: expected %v got %v", tt.name, tt.expects, got)
		}
	}
}

func TestRemainingPathAfterPrefix(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		prefix   string
		expected string
	}{
		{"withLeadingSlash", "/api/v1", "/api", "/v1"},
		{"root", "/foo/bar", "/", "/foo/bar"},
		{"trailingSlash", "/api/v1", "/api/", "/v1"},
		{"exact", "/api", "/api", ""},
	}

	for _, tt := range tests {
		if got := remainingPathAfterPrefix(tt.path, tt.prefix); got != tt.expected {
			t.Fatalf("%s: expected %s got %s", tt.name, tt.expected, got)
		}
	}
}
