package rediscache

import "testing"

func TestKeyUsesNamespace(t *testing.T) {
	cache := &Cache{prefix: "ai-gdm"}
	if got := cache.key("hazard:latest"); got != "ai-gdm:hazard:latest" {
		t.Fatalf("key() = %q", got)
	}
}
