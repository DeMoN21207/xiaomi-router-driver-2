package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultDataDirName = "data"
const legacyBackupDirName = "backups"

var migratedEntries = []string{
	"vpn-state.json",
	"domains.list",
	"events.json",
	"traffic-history.json",
	".vpn-manager",
	"profiles",
}

type Paths struct {
	AppDir  string
	DataDir string
}

func Resolve(executablePath string) (Paths, error) {
	appDir, err := ResolveAppDir(executablePath)
	if err != nil {
		return Paths{}, err
	}

	dataDir, err := ResolveDataDir(appDir)
	if err != nil {
		return Paths{}, err
	}

	return Paths{
		AppDir:  appDir,
		DataDir: dataDir,
	}, nil
}

func ResolveAppDir(executablePath string) (string, error) {
	if root := strings.TrimSpace(os.Getenv("VPN_MANAGER_ROOT")); root != "" {
		return filepath.Abs(root)
	}

	if strings.TrimSpace(executablePath) == "" {
		path, err := os.Executable()
		if err != nil {
			return "", err
		}
		executablePath = path
	}

	return filepath.Abs(filepath.Dir(executablePath))
}

func ResolveDataDir(appDir string) (string, error) {
	if root := strings.TrimSpace(os.Getenv("VPN_MANAGER_DATA_DIR")); root != "" {
		return filepath.Abs(root)
	}

	return filepath.Abs(filepath.Join(appDir, defaultDataDirName))
}

func EnsureDataLayout(paths Paths) error {
	if samePath(paths.AppDir, paths.DataDir) {
		return nil
	}

	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		return fmt.Errorf("prepare data directory: %w", err)
	}

	for _, name := range migratedEntries {
		if err := moveIntoDataDir(paths.AppDir, paths.DataDir, name); err != nil {
			return err
		}
	}

	return nil
}

func ArchiveLegacyData(paths Paths) error {
	if samePath(paths.AppDir, paths.DataDir) {
		return nil
	}

	for _, name := range legacyDataEntries() {
		if err := moveToBackups(paths.AppDir, paths.DataDir, name); err != nil {
			return err
		}
	}

	return nil
}

func moveIntoDataDir(appDir string, dataDir string, name string) error {
	sourcePath := filepath.Join(appDir, name)
	targetPath := filepath.Join(dataDir, name)

	if samePath(sourcePath, targetPath) {
		return nil
	}

	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check %s: %w", sourcePath, err)
	}

	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check %s: %w", targetPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("prepare %s: %w", targetPath, err)
	}

	if err := os.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("move %s to %s: %w", sourcePath, targetPath, err)
	}

	return nil
}

func moveToBackups(appDir string, dataDir string, name string) error {
	sourcePath := filepath.Join(dataDir, name)

	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check %s: %w", sourcePath, err)
	}

	backupPath := filepath.Join(appDir, legacyBackupDirName, "legacy-data", name)
	if _, err := os.Stat(backupPath); err == nil {
		backupPath = filepath.Join(appDir, legacyBackupDirName, "legacy-data", fmt.Sprintf("%s.%d", filepath.Base(name), time.Now().UTC().Unix()))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check %s: %w", backupPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return fmt.Errorf("prepare %s: %w", backupPath, err)
	}

	if err := os.Rename(sourcePath, backupPath); err != nil {
		return fmt.Errorf("move %s to %s: %w", sourcePath, backupPath, err)
	}

	return nil
}

func legacyDataEntries() []string {
	return []string{
		"vpn-state.json",
		"domains.list",
		"events.json",
		"traffic-history.json",
	}
}

func samePath(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
