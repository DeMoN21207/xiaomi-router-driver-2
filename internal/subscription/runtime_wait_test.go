package subscription

import (
	"strings"
	"testing"
	"time"
)

func TestWaitForInterfaceWaitsUntilInterfaceAppears(t *testing.T) {
	originalAlive := interfaceAlive
	originalPoll := waitForInterfacePollInterval
	t.Cleanup(func() {
		interfaceAlive = originalAlive
		waitForInterfacePollInterval = originalPoll
	})

	waitForInterfacePollInterval = 5 * time.Millisecond
	start := time.Now()
	interfaceAlive = func(name string) bool {
		return time.Since(start) >= 20*time.Millisecond
	}

	if err := waitForInterface("sb-test0", 200*time.Millisecond); err != nil {
		t.Fatalf("waitForInterface() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("waitForInterface() returned too early after %s", elapsed)
	}
}

func TestWaitForInterfaceTimesOut(t *testing.T) {
	originalAlive := interfaceAlive
	originalPoll := waitForInterfacePollInterval
	t.Cleanup(func() {
		interfaceAlive = originalAlive
		waitForInterfacePollInterval = originalPoll
	})

	waitForInterfacePollInterval = 5 * time.Millisecond
	interfaceAlive = func(name string) bool { return false }

	err := waitForInterface("sb-missing0", 20*time.Millisecond)
	if err == nil {
		t.Fatal("waitForInterface() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "sb-missing0") {
		t.Fatalf("waitForInterface() error = %q, want interface name", err.Error())
	}
}
