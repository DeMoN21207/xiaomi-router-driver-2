package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDataLayoutMovesRuntimeEntries(t *testing.T) {
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")

	mustWriteFile(t, filepath.Join(appDir, "vpn-state.json"), `{"ok":true}`)
	mustWriteFile(t, filepath.Join(appDir, "domains.list"), "example.com\n")
	mustWriteFile(t, filepath.Join(appDir, "events.json"), "[]\n")
	mustWriteFile(t, filepath.Join(appDir, "traffic-history.json"), "{}\n")
	mustWriteFile(t, filepath.Join(appDir, ".vpn-manager", "update_routes.sh"), "#!/bin/sh\n")
	mustWriteFile(t, filepath.Join(appDir, "profiles", "demo.ovpn"), "client\n")

	if err := EnsureDataLayout(Paths{AppDir: appDir, DataDir: dataDir}); err != nil {
		t.Fatalf("EnsureDataLayout() error = %v", err)
	}

	for _, relativePath := range []string{
		"vpn-state.json",
		"domains.list",
		"events.json",
		"traffic-history.json",
		filepath.Join(".vpn-manager", "update_routes.sh"),
		filepath.Join("profiles", "demo.ovpn"),
	} {
		if _, err := os.Stat(filepath.Join(dataDir, relativePath)); err != nil {
			t.Fatalf("expected migrated file %s: %v", relativePath, err)
		}
		if _, err := os.Stat(filepath.Join(appDir, relativePath)); !os.IsNotExist(err) {
			t.Fatalf("expected original file %s to be moved, err=%v", relativePath, err)
		}
	}
}

func TestEnsureDataLayoutKeepsExistingDataFiles(t *testing.T) {
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")

	mustWriteFile(t, filepath.Join(appDir, "vpn-state.json"), `{"old":true}`)
	mustWriteFile(t, filepath.Join(dataDir, "vpn-state.json"), `{"new":true}`)

	if err := EnsureDataLayout(Paths{AppDir: appDir, DataDir: dataDir}); err != nil {
		t.Fatalf("EnsureDataLayout() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dataDir, "vpn-state.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != `{"new":true}` {
		t.Fatalf("unexpected migrated content: %s", string(got))
	}

	if _, err := os.Stat(filepath.Join(appDir, "vpn-state.json")); err != nil {
		t.Fatalf("expected original file to remain when destination exists: %v", err)
	}
}

func TestArchiveLegacyDataMovesOldFilesIntoBackups(t *testing.T) {
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")

	mustWriteFile(t, filepath.Join(dataDir, "vpn-state.json"), `{"ok":true}`)
	mustWriteFile(t, filepath.Join(dataDir, "domains.list"), "example.com\n")
	mustWriteFile(t, filepath.Join(dataDir, "events.json"), "[]\n")
	mustWriteFile(t, filepath.Join(dataDir, "traffic-history.json"), "{}\n")

	if err := ArchiveLegacyData(Paths{AppDir: appDir, DataDir: dataDir}); err != nil {
		t.Fatalf("ArchiveLegacyData() error = %v", err)
	}

	for _, relativePath := range []string{
		"vpn-state.json",
		"domains.list",
		"events.json",
		"traffic-history.json",
	} {
		if _, err := os.Stat(filepath.Join(dataDir, relativePath)); !os.IsNotExist(err) {
			t.Fatalf("expected legacy file %s to be moved out of data dir, err=%v", relativePath, err)
		}
		if _, err := os.Stat(filepath.Join(appDir, "backups", "legacy-data", relativePath)); err != nil {
			t.Fatalf("expected archived file %s: %v", relativePath, err)
		}
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
