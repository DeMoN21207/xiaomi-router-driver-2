package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestShutdownRunsEveryStepInSafeOrder(t *testing.T) {
	var calls []string
	hooks := ShutdownHooks{
		CancelWorkers: func() { calls = append(calls, "cancel") },
		ShutdownHTTP:  func(context.Context) error { calls = append(calls, "http"); return nil },
		CloseDNS:      func() error { calls = append(calls, "dns"); return nil },
		WaitWorkers:   func() { calls = append(calls, "wait") },
		CheckpointDB:  func() error { calls = append(calls, "checkpoint"); return nil },
		CloseDB:       func() error { calls = append(calls, "db"); return nil },
	}

	if err := Shutdown(context.Background(), hooks); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	want := []string{"cancel", "http", "dns", "wait", "checkpoint", "db"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("shutdown calls = %v, want %v", calls, want)
	}
}

func TestExecWithRollbackRestoresBackupWhenNewBinaryCannotStart(t *testing.T) {
	var calls []string
	newBinaryErr := errors.New("exec format error")
	hooks := RestartHooks{
		Exec: func(path string, _ []string, _ []string) error {
			calls = append(calls, "exec:"+path)
			if len(calls) == 1 {
				return newBinaryErr
			}
			return nil
		},
		Restore: func(appDir string, backupDir string) error {
			calls = append(calls, "restore:"+appDir+":"+backupDir)
			return nil
		},
	}

	if err := ExecWithRollback(hooks, "/app", "/app/backups/old", "/app/vpn-manager", []string{"vpn-manager"}, nil); err != nil {
		t.Fatalf("ExecWithRollback() error = %v", err)
	}
	want := []string{"exec:/app/vpn-manager", "restore:/app:/app/backups/old", "exec:/app/vpn-manager"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("restart calls = %v, want %v", calls, want)
	}
}
