package main

import "testing"

func TestBanner(t *testing.T) {
	t.Parallel()

	if got := banner("test"); got != "AI-GDM test" {
		t.Fatalf("banner() = %q", got)
	}
}
