package update

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelectAssetMatchesPattern(t *testing.T) {
	release := GitHubRelease{
		TagName: "v1.2.3",
		Assets: []GitHubAsset{
			{Name: "vpn-manager-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin"},
			{Name: "vpn-manager-linux-arm64.tar.gz", BrowserDownloadURL: "https://example.com/linux"},
		},
	}

	asset, err := SelectAsset(release, "vpn-manager-linux-arm64.tar.gz")
	if err != nil {
		t.Fatalf("SelectAsset() error = %v", err)
	}
	if asset.Name != "vpn-manager-linux-arm64.tar.gz" || asset.BrowserDownloadURL != "https://example.com/linux" {
		t.Fatalf("unexpected asset: %+v", asset)
	}
}

func TestExtractTarGzRejectsPathTraversal(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "bad.tar.gz")
	createTarGz(t, archivePath, map[string]string{
		"../escape": "owned",
	})

	err := ExtractTarGz(archivePath, filepath.Join(tempDir, "out"))
	if err == nil {
		t.Fatalf("expected traversal archive to be rejected")
	}
}

func TestValidateBundleAcceptsLinuxARM64Bundle(t *testing.T) {
	root := t.TempDir()
	writeValidBundle(t, root, "linux", "arm64")

	info, err := ValidateBundle(root)
	if err != nil {
		t.Fatalf("ValidateBundle() error = %v", err)
	}
	if info.Binary != "vpn-manager" || info.GOOS != "linux" || info.GOARCH != "arm64" {
		t.Fatalf("unexpected bundle info: %+v", info)
	}
}

func TestValidateBundleRejectsWrongPlatform(t *testing.T) {
	root := t.TempDir()
	writeValidBundle(t, root, "darwin", "arm64")

	if _, err := ValidateBundle(root); err == nil {
		t.Fatalf("expected wrong platform to be rejected")
	}
}

func TestInstallBundlePreservesDataAndCreatesBackup(t *testing.T) {
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	writeRuntimeFile(t, appDir, "vpn-manager", "old manager")
	writeRuntimeFile(t, appDir, "start.sh", "old start")
	writeRuntimeFile(t, appDir, "bin/openvpn", "old openvpn")
	writeRuntimeFile(t, appDir, "bin/sing-box", "old sing-box")
	writeRuntimeFile(t, appDir, "data/vpn-manager.db", "keep me")

	bundleRoot := filepath.Join(t.TempDir(), "bundle")
	writeValidBundle(t, bundleRoot, "linux", "arm64")
	writeRuntimeFile(t, bundleRoot, "vpn-manager", "new manager")
	info, err := ValidateBundle(bundleRoot)
	if err != nil {
		t.Fatalf("ValidateBundle() error = %v", err)
	}

	backupDir, err := installBundle(appDir, dataDir, info, time.Date(2026, 5, 13, 12, 34, 56, 0, time.UTC))
	if err != nil {
		t.Fatalf("installBundle() error = %v", err)
	}

	if got := readFile(t, filepath.Join(appDir, "data/vpn-manager.db")); got != "keep me" {
		t.Fatalf("data was not preserved, got %q", got)
	}
	if got := readFile(t, filepath.Join(appDir, "vpn-manager")); got != "new manager" {
		t.Fatalf("runtime binary was not replaced, got %q", got)
	}
	if got := readFile(t, filepath.Join(backupDir, "vpn-manager")); got != "old manager" {
		t.Fatalf("backup did not keep old runtime, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "data")); !os.IsNotExist(err) {
		t.Fatalf("backup must not copy data directory, stat err=%v", err)
	}
}

func TestManagerRejectsConcurrentOperation(t *testing.T) {
	manager := NewManager(Options{
		AppDir:  t.TempDir(),
		DataDir: t.TempDir(),
		Restart: func() {},
	})

	finish, err := manager.beginOperation("install")
	if err != nil {
		t.Fatalf("beginOperation() error = %v", err)
	}
	defer finish(nil)

	if _, err := manager.beginOperation("install"); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("expected ErrOperationInProgress, got %v", err)
	}
}

func createTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, body := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
}

func writeValidBundle(t *testing.T, root string, goos string, goarch string) {
	t.Helper()

	files := map[string]string{
		"vpn-manager":     "binary",
		"start.sh":        "#!/bin/sh\n",
		"bin/openvpn":     "openvpn",
		"bin/sing-box":    "sing-box",
		"bundle-info.txt": "binary=vpn-manager\ngoos=" + goos + "\ngoarch=" + goarch + "\nopenvpn_path=bin/openvpn\nsingbox_path=bin/sing-box\n",
	}

	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
}

func writeRuntimeFile(t *testing.T, root string, name string, body string) {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}
