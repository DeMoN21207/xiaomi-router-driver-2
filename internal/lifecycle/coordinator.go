package lifecycle

import (
	"context"
	"errors"
	"fmt"
)

type ShutdownHooks struct {
	CancelWorkers func()
	ShutdownHTTP  func(context.Context) error
	CloseDNS      func() error
	WaitWorkers   func()
	CheckpointDB  func() error
	CloseDB       func() error
}

func Shutdown(ctx context.Context, hooks ShutdownHooks) error {
	if hooks.CancelWorkers != nil {
		hooks.CancelWorkers()
	}
	var errs []error
	if hooks.ShutdownHTTP != nil {
		if err := hooks.ShutdownHTTP(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown HTTP server: %w", err))
		}
	}
	if hooks.CloseDNS != nil {
		if err := hooks.CloseDNS(); err != nil {
			errs = append(errs, fmt.Errorf("close DNS proxy: %w", err))
		}
	}
	if hooks.WaitWorkers != nil {
		hooks.WaitWorkers()
	}
	if hooks.CheckpointDB != nil {
		if err := hooks.CheckpointDB(); err != nil {
			errs = append(errs, fmt.Errorf("checkpoint database: %w", err))
		}
	}
	if hooks.CloseDB != nil {
		if err := hooks.CloseDB(); err != nil {
			errs = append(errs, fmt.Errorf("close database: %w", err))
		}
	}
	return errors.Join(errs...)
}

type RestartHooks struct {
	Exec    func(path string, argv []string, env []string) error
	Restore func(appDir string, backupDir string) error
}

func ExecWithRollback(hooks RestartHooks, appDir string, backupDir string, binaryPath string, argv []string, env []string) error {
	if hooks.Exec == nil {
		return errors.New("restart executable hook is required")
	}
	if err := hooks.Exec(binaryPath, argv, env); err == nil {
		return nil
	} else if hooks.Restore == nil || backupDir == "" {
		return fmt.Errorf("start updated executable: %w", err)
	} else if restoreErr := hooks.Restore(appDir, backupDir); restoreErr != nil {
		return errors.Join(fmt.Errorf("start updated executable: %w", err), fmt.Errorf("restore runtime backup: %w", restoreErr))
	}
	if err := hooks.Exec(binaryPath, argv, env); err != nil {
		return fmt.Errorf("start restored executable: %w", err)
	}
	return nil
}
