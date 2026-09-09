package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xiomi-router-driver/internal/subscription"
)

func TestStandbyProbeReapsProcessWhenListenerNeverStarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX process signals")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pid")
	binary := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(binary, []byte(fmt.Sprintf("#!/bin/sh\necho $$ > '%s'\nexec sleep 60\n", marker)), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(marker); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	result := probeStandbyEntry(ctx, binary, subscription.Entry{Outbound: map[string]any{"type": "direct"}})
	if result.Healthy {
		t.Fatal("process without SOCKS listener was accepted")
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("standby process %d survived cleanup: %v", pid, err)
	}
}
