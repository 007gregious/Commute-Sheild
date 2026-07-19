package main

import "testing"

func TestParseCSVEnvTrimsValuesAndDropsEmptyEntries(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", " https://app.example.com,  https://admin.example.com ,, ")

	origins := parseCSVEnv("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"})

	if len(origins) != 2 {
		t.Fatalf("expected 2 parsed origins, got %d: %#v", len(origins), origins)
	}
	if origins[0] != "https://app.example.com" || origins[1] != "https://admin.example.com" {
		t.Fatalf("origins were not trimmed as expected: %#v", origins)
	}
}

func TestParseCSVEnvFallsBackWhenUnsetOrEmpty(t *testing.T) {
	fallback := []string{"http://localhost:3000"}

	if origins := parseCSVEnv("CORS_ALLOWED_ORIGINS", fallback); len(origins) != 1 || origins[0] != fallback[0] {
		t.Fatalf("expected unset env to use fallback, got %#v", origins)
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", " , ")
	if origins := parseCSVEnv("CORS_ALLOWED_ORIGINS", fallback); len(origins) != 1 || origins[0] != fallback[0] {
		t.Fatalf("expected empty env to use fallback, got %#v", origins)
	}
}

func TestAllowConfiguredOriginRejectsUnknownOrigins(t *testing.T) {
	allowOrigin := allowConfiguredOrigin([]string{"https://app.example.com"})

	if !allowOrigin("https://app.example.com") {
		t.Fatal("expected configured origin to be allowed")
	}
	if !allowOrigin("") {
		t.Fatal("expected non-browser requests without an Origin header to be allowed")
	}
	if allowOrigin("https://evil.example.com") {
		t.Fatal("expected unknown browser origin to be rejected")
	}
}
