package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBundleInfoReadsBuildMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle-info.txt")
	if err := os.WriteFile(path, []byte("binary=vpn-manager\ngoos=linux\ngoarch=arm64\nversion=v1.2.3\ncommit=abc1234-dirty\nbuilt_at=2026-07-08T10:30:00Z\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	info, err := ParseBundleInfo(path)
	if err != nil {
		t.Fatalf("ParseBundleInfo() error = %v", err)
	}
	if info.Version != "v1.2.3" {
		t.Fatalf("Version = %q, want v1.2.3", info.Version)
	}
	if info.Commit != "abc1234-dirty" {
		t.Fatalf("Commit = %q, want abc1234-dirty", info.Commit)
	}
	if info.BuiltAt != "2026-07-08T10:30:00Z" {
		t.Fatalf("BuiltAt = %q, want timestamp", info.BuiltAt)
	}
}
