package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("prepare sqlite directory: %w", err)
	}
	db, err := openConfigured(path)
	if err == nil {
		if integrityErr := CheckIntegrity(db); integrityErr == nil {
			return db, nil
		} else {
			err = integrityErr
		}
	}
	if db != nil {
		_ = db.Close()
	}
	openErr := fmt.Errorf("open sqlite database: %w", err)
	if restoreErr := restoreBackup(path); restoreErr != nil {
		if errors.Is(restoreErr, os.ErrNotExist) {
			return nil, openErr
		}
		return nil, errors.Join(openErr, restoreErr)
	}
	db, err = openConfigured(path)
	if err != nil {
		return nil, fmt.Errorf("open restored sqlite database: %w", err)
	}
	if err := CheckIntegrity(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("check restored sqlite database: %w", err)
	}
	return db, nil
}

func openConfigured(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 15000",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply sqlite pragma %q: %w", pragma, err)
		}
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return db, nil
}

func CheckIntegrity(db *sql.DB) error {
	if db == nil {
		return errors.New("sqlite database is not configured")
	}
	rows, err := db.Query("PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("run sqlite quick_check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			return fmt.Errorf("sqlite quick_check: %s", result)
		}
	}
	return rows.Err()
}

func CheckFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return CheckIntegrity(db)
}

func Checkpoint(db *sql.DB) error {
	if db == nil {
		return errors.New("sqlite database is not configured")
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint sqlite WAL: %w", err)
	}
	return nil
}

// Maintain checks the live database, truncates its WAL and atomically writes a
// compact last-known-good backup beside the database.
func Maintain(db *sql.DB, path string) error {
	if err := CheckIntegrity(db); err != nil {
		return err
	}
	if err := Checkpoint(db); err != nil {
		return err
	}
	backupPath := BackupPath(path)
	tempPath := backupPath + ".tmp"
	_ = os.Remove(tempPath)
	quoted := strings.ReplaceAll(tempPath, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		return fmt.Errorf("create sqlite backup: %w", err)
	}
	file, err := os.Open(tempPath)
	if err != nil {
		return fmt.Errorf("open sqlite backup for sync: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return fmt.Errorf("sync sqlite backup: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sqlite backup: %w", closeErr)
	}
	if err := os.Rename(tempPath, backupPath); err != nil {
		return fmt.Errorf("publish sqlite backup: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

func BackupPath(path string) string {
	return filepath.Clean(path) + ".backup"
}

func RunMaintenance(ctx context.Context, db *sql.DB, path string, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := Maintain(db, path); err != nil && report != nil {
				report(err)
			}
		}
	}
}

func restoreBackup(path string) error {
	backupPath := BackupPath(path)
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		return fmt.Errorf("open sqlite backup: %w", err)
	}
	if _, statErr := os.Stat(backupPath); statErr != nil {
		_ = backup.Close()
		return statErr
	}
	if err := CheckIntegrity(backup); err != nil {
		_ = backup.Close()
		return fmt.Errorf("sqlite backup is not usable: %w", err)
	}
	if err := backup.Close(); err != nil {
		return err
	}
	tempPath := path + ".restore.tmp"
	if err := copyDurable(backupPath, tempPath); err != nil {
		return err
	}
	corruptPath := path + ".corrupt-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(path, corruptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tempPath)
		return fmt.Errorf("archive corrupt sqlite database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Rename(corruptPath, path)
		return fmt.Errorf("restore sqlite backup: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

func copyDurable(source string, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		// Some filesystems do not support directory fsync.
		if strings.Contains(strings.ToLower(err.Error()), "invalid argument") || strings.Contains(strings.ToLower(err.Error()), "not supported") {
			return nil
		}
		return err
	}
	return nil
}
