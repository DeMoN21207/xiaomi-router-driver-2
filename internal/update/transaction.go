package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	updateJournalName  = ".update-journal.json"
	updatePreviousName = ".update-previous"
)

type updateTarget struct {
	Name        string `json:"name"`
	HadOriginal bool   `json:"hadOriginal"`
}

type updateJournal struct {
	Version     int            `json:"version"`
	Phase       string         `json:"phase"`
	BackupDir   string         `json:"backupDir,omitempty"`
	PreviousDir string         `json:"previousDir"`
	StagingDir  string         `json:"stagingDir,omitempty"`
	Targets     []updateTarget `json:"targets"`
}

type runtimeTransaction struct {
	appDir  string
	journal updateJournal
}

// RecoverInterruptedUpdate restores the last known-good runtime after a crash
// during replacement. A journal marked installed only needs stale artifacts
// removed because every target rename completed before that phase was saved.
func RecoverInterruptedUpdate(appDir string) error {
	appDir = filepath.Clean(strings.TrimSpace(appDir))
	if appDir == "." || appDir == "" {
		return fmt.Errorf("recover update: app directory is empty")
	}
	journalPath := filepath.Join(appDir, updateJournalName)
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return cleanupAbandonedStaging(appDir)
	}
	if err != nil {
		return fmt.Errorf("read update journal: %w", err)
	}

	var journal updateJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("decode update journal: %w", err)
	}
	if err := validateJournal(appDir, journal); err != nil {
		return err
	}
	tx := runtimeTransaction{appDir: appDir, journal: journal}
	if journal.Phase == "installed" {
		return tx.cleanup()
	}
	return tx.rollback()
}

func beginRuntimeTransaction(appDir string, bundleRoot string, backupDir string) (*runtimeTransaction, error) {
	if err := RecoverInterruptedUpdate(appDir); err != nil {
		return nil, err
	}
	stagingDir, err := os.MkdirTemp(appDir, ".update-staging-")
	if err != nil {
		return nil, fmt.Errorf("create update staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	entries, err := os.ReadDir(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("read bundle directory: %w", err)
	}
	targets := make([]updateTarget, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipRuntimeEntry(name) {
			continue
		}
		if err := copyPath(filepath.Join(bundleRoot, name), filepath.Join(stagingDir, name)); err != nil {
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
		_, statErr := os.Lstat(filepath.Join(appDir, name))
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect current %s: %w", name, statErr)
		}
		targets = append(targets, updateTarget{Name: name, HadOriginal: statErr == nil})
	}
	if err := syncDirectory(stagingDir); err != nil {
		return nil, fmt.Errorf("sync update staging directory: %w", err)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	previousDir := filepath.Join(appDir, updatePreviousName)
	if err := os.RemoveAll(previousDir); err != nil {
		return nil, fmt.Errorf("remove previous update transaction: %w", err)
	}
	journal := updateJournal{
		Version: 1, Phase: "prepared", BackupDir: backupDir,
		PreviousDir: previousDir, StagingDir: stagingDir, Targets: targets,
	}
	if err := writeUpdateJournal(appDir, journal); err != nil {
		return nil, err
	}
	complete = true
	return &runtimeTransaction{appDir: appDir, journal: journal}, nil
}

func (tx *runtimeTransaction) apply() error {
	if err := os.MkdirAll(tx.journal.PreviousDir, 0o755); err != nil {
		return fmt.Errorf("prepare previous runtime directory: %w", err)
	}
	tx.journal.Phase = "applying"
	if err := writeUpdateJournal(tx.appDir, tx.journal); err != nil {
		return err
	}

	for _, item := range tx.journal.Targets {
		target := filepath.Join(tx.appDir, item.Name)
		previous := filepath.Join(tx.journal.PreviousDir, item.Name)
		staged := filepath.Join(tx.journal.StagingDir, item.Name)
		if item.HadOriginal {
			if err := os.MkdirAll(filepath.Dir(previous), 0o755); err != nil {
				return tx.fail(fmt.Errorf("prepare previous %s: %w", item.Name, err))
			}
			if err := os.Rename(target, previous); err != nil {
				return tx.fail(fmt.Errorf("preserve current %s: %w", item.Name, err))
			}
		}
		if err := os.Rename(staged, target); err != nil {
			return tx.fail(fmt.Errorf("activate staged %s: %w", item.Name, err))
		}
		if err := syncDirectory(tx.appDir); err != nil {
			return tx.fail(fmt.Errorf("sync activated %s: %w", item.Name, err))
		}
	}
	return nil
}

func (tx *runtimeTransaction) commit() error {
	tx.journal.Phase = "installed"
	if err := writeUpdateJournal(tx.appDir, tx.journal); err != nil {
		return tx.fail(err)
	}
	return tx.cleanup()
}

func (tx *runtimeTransaction) fail(cause error) error {
	if rollbackErr := tx.rollback(); rollbackErr != nil {
		return fmt.Errorf("%w; update rollback failed: %v", cause, rollbackErr)
	}
	return cause
}

func (tx *runtimeTransaction) rollback() error {
	for index := len(tx.journal.Targets) - 1; index >= 0; index-- {
		item := tx.journal.Targets[index]
		target := filepath.Join(tx.appDir, item.Name)
		previous := filepath.Join(tx.journal.PreviousDir, item.Name)
		if _, err := os.Lstat(previous); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove interrupted %s: %w", item.Name, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("prepare restored %s: %w", item.Name, err)
			}
			if err := os.Rename(previous, target); err != nil {
				return fmt.Errorf("restore previous %s: %w", item.Name, err)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect previous %s: %w", item.Name, err)
		}
		if item.HadOriginal && tx.journal.BackupDir != "" {
			backup := filepath.Join(tx.journal.BackupDir, item.Name)
			if _, err := os.Lstat(backup); err == nil {
				if err := os.RemoveAll(target); err != nil {
					return fmt.Errorf("remove interrupted %s: %w", item.Name, err)
				}
				if err := copyPath(backup, target); err != nil {
					return fmt.Errorf("restore backup %s: %w", item.Name, err)
				}
				continue
			}
		}
		if !item.HadOriginal {
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove newly installed %s: %w", item.Name, err)
			}
		}
	}
	if err := syncDirectory(tx.appDir); err != nil {
		return err
	}
	return tx.cleanup()
}

func (tx *runtimeTransaction) cleanup() error {
	if tx.journal.StagingDir != "" {
		if err := os.RemoveAll(tx.journal.StagingDir); err != nil {
			return fmt.Errorf("remove update staging directory: %w", err)
		}
	}
	if err := os.RemoveAll(tx.journal.PreviousDir); err != nil {
		return fmt.Errorf("remove previous runtime directory: %w", err)
	}
	if err := os.Remove(filepath.Join(tx.appDir, updateJournalName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove update journal: %w", err)
	}
	return syncDirectory(tx.appDir)
}

func writeUpdateJournal(appDir string, journal updateJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(appDir, ".update-journal-*.tmp")
	if err != nil {
		return fmt.Errorf("create update journal: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(appDir, updateJournalName)); err != nil {
		return fmt.Errorf("publish update journal: %w", err)
	}
	return syncDirectory(appDir)
}

func validateJournal(appDir string, journal updateJournal) error {
	if journal.Version != 1 {
		return fmt.Errorf("unsupported update journal version %d", journal.Version)
	}
	if journal.Phase != "prepared" && journal.Phase != "applying" && journal.Phase != "installed" {
		return fmt.Errorf("invalid update journal phase %q", journal.Phase)
	}
	for _, path := range []string{journal.PreviousDir, journal.StagingDir, journal.BackupDir} {
		if path != "" && !pathWithin(appDir, path) {
			return fmt.Errorf("update journal path escapes application directory: %s", path)
		}
	}
	for _, item := range journal.Targets {
		if item.Name == "" || item.Name == "." || filepath.Base(item.Name) != item.Name || shouldSkipRuntimeEntry(item.Name) {
			return fmt.Errorf("invalid update target %q", item.Name)
		}
	}
	return nil
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func cleanupAbandonedStaging(appDir string) error {
	matches, err := filepath.Glob(filepath.Join(appDir, ".update-staging-*"))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.RemoveAll(match); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
