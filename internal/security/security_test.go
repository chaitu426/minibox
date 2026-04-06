package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidContainerID(t *testing.T) {
	if !ValidContainerID("a1b2c3d4") {
		t.Fatal("expected valid id")
	}
	if ValidContainerID("../../../etc") {
		t.Fatal("expected invalid id")
	}
	if ValidContainerID("short") {
		t.Fatal("expected invalid id")
	}
}

func TestResolveAllowedPath(t *testing.T) {
	tmp := t.TempDir()
	prefixes := []string{tmp}
	sub := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveAllowedPath(sub, prefixes)
	if err != nil {
		t.Fatal(err)
	}
	if got != sub {
		t.Fatalf("got %q want %q", got, sub)
	}
	_, err = ResolveAllowedPath(filepath.Join(tmp, "..", "..", "etc"), prefixes)
	if err == nil {
		t.Fatal("expected error for path outside prefix")
	}
}
