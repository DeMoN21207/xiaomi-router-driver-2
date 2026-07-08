package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var executableBundlePaths = []string{
	"vpn-manager",
	"start.sh",
	"bin/openvpn",
	"bin/sing-box",
}

func installBundle(appDir string, dataDir string, bundle BundleInfo, now time.Time) (string, error) {
	if strings.TrimSpace(appDir) == "" {
		return "", fmt.Errorf("%w: app directory is empty", ErrInvalidBundle)
	}
	if strings.TrimSpace(bundle.Root) == "" {
		return "", fmt.Errorf("%w: bundle root is empty", ErrInvalidBundle)
	}

	backupDir, err := createBackup(appDir, now)
	if err != nil {
		return "", err
	}

	if err := backupRuntime(appDir, backupDir); err != nil {
		return "", err
	}

	for _, stale := range []string{"openvpn", "sing-box"} {
		if err := os.RemoveAll(filepath.Join(appDir, stale)); err != nil {
			return "", fmt.Errorf("remove stale runtime file %s: %w", stale, err)
		}
	}

	if err := replaceRuntime(appDir, bundle.Root); err != nil {
		return "", err
	}

	if strings.TrimSpace(dataDir) != "" {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return "", fmt.Errorf("ensure data directory: %w", err)
		}
	}

	for _, name := range executableBundlePaths {
		_ = os.Chmod(filepath.Join(appDir, name), 0o755)
	}

	return backupDir, nil
}

func createBackup(appDir string, now time.Time) (string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	base := filepath.Join(appDir, "backups")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("prepare backup directory: %w", err)
	}

	name := "update-" + now.UTC().Format("20060102-150405")
	backupDir := filepath.Join(base, name)
	for index := 1; ; index++ {
		err := os.Mkdir(backupDir, 0o755)
		if err == nil {
			return backupDir, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("create backup directory: %w", err)
		}
		backupDir = filepath.Join(base, fmt.Sprintf("%s-%d", name, index))
	}
}

func backupRuntime(appDir string, backupDir string) error {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return fmt.Errorf("read app directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipRuntimeEntry(name) {
			continue
		}
		if err := copyPath(filepath.Join(appDir, name), filepath.Join(backupDir, name)); err != nil {
			return fmt.Errorf("backup %s: %w", name, err)
		}
	}

	return nil
}

func replaceRuntime(appDir string, bundleRoot string) error {
	entries, err := os.ReadDir(bundleRoot)
	if err != nil {
		return fmt.Errorf("read bundle directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipRuntimeEntry(name) {
			continue
		}
		target := filepath.Join(appDir, name)
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove old %s: %w", name, err)
		}
		if err := copyPath(filepath.Join(bundleRoot, name), target); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	return nil
}

func shouldSkipRuntimeEntry(name string) bool {
	switch name {
	case "data", "backups":
		return true
	default:
		return false
	}
}

func copyPath(source string, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks are not supported: %s", source)
	}
	if info.IsDir() {
		return copyDir(source, target, info.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported file type: %s", source)
	}
	return copyFile(source, target, info.Mode().Perm())
}

func copyDir(source string, target string, mode os.FileMode) error {
	if err := os.MkdirAll(target, modeOrDefault(mode, 0o755)); err != nil {
		return err
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source string, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, modeOrDefault(mode, 0o644))
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
