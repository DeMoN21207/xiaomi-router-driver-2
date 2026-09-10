package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverInterruptedUpdateRestoresOriginalRuntime(t *testing.T) {
	appDir := t.TempDir()
	previousDir := filepath.Join(appDir, ".update-previous")
	stagingDir := filepath.Join(appDir, ".update-staging-test")
	writeRuntimeFixture(t, filepath.Join(appDir, "vpn-manager"), "partial-new")
	writeRuntimeFixture(t, filepath.Join(previousDir, "vpn-manager"), "known-good")
	writeRuntimeFixture(t, filepath.Join(stagingDir, "start.sh"), "staged")
	journal := `{"version":1,"phase":"applying","previousDir":"` + previousDir + `","stagingDir":"` + stagingDir + `","targets":[{"name":"vpn-manager","hadOriginal":true}]}`
	if err := os.WriteFile(filepath.Join(appDir, ".update-journal.json"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedUpdate(appDir); err != nil {
		t.Fatalf("RecoverInterruptedUpdate() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(appDir, "vpn-manager"))
	if err != nil || string(data) != "known-good" {
		t.Fatalf("runtime was not restored: %q / %v", data, err)
	}
	for _, path := range []string{previousDir, stagingDir, filepath.Join(appDir, ".update-journal.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("transaction artifact remains at %s: %v", path, err)
		}
	}
}

func TestRecoverCompletedUpdateKeepsInstalledRuntime(t *testing.T) {
	appDir := t.TempDir()
	previousDir := filepath.Join(appDir, ".update-previous")
	writeRuntimeFixture(t, filepath.Join(appDir, "vpn-manager"), "installed")
	writeRuntimeFixture(t, filepath.Join(previousDir, "vpn-manager"), "old")
	journal := `{"version":1,"phase":"installed","previousDir":"` + previousDir + `","targets":[{"name":"vpn-manager","hadOriginal":true}]}`
	if err := os.WriteFile(filepath.Join(appDir, ".update-journal.json"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInterruptedUpdate(appDir); err != nil {
		t.Fatalf("RecoverInterruptedUpdate() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(appDir, "vpn-manager"))
	if err != nil || string(data) != "installed" {
		t.Fatalf("completed update was rolled back: %q / %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(appDir, ".update-journal.json")); !os.IsNotExist(err) {
		t.Fatalf("journal remains after completed update: %v", err)
	}
}

func writeRuntimeFixture(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
