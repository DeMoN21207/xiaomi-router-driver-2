package automation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderedServiceScriptsHaveValidShellSyntax(t *testing.T) {
	t.Parallel()
	for name, script := range map[string]string{
		"init": renderInitScript("/mnt/usb/vpn-manager/vpn-manager", "/mnt/usb/vpn-manager", "18080"),
		"cron": renderCronBootstrapScript("/mnt/usb/vpn-manager/vpn-manager", "/mnt/usb/vpn-manager", "18080"),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("sh", "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rendered script syntax error: %v: %s", err, output)
			}
		})
	}
}

func TestDetectServiceInstallMethodFromMounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mounts string
		want   serviceInstallMethod
	}{
		{
			name: "uses cron when etc is ramfs",
			mounts: `
none /etc ramfs rw,relatime 0 0
/dev/sda1 /mnt/usb ext4 rw,relatime 0 0
`,
			want: installMethodCron,
		},
		{
			name: "uses cron when etc is tmpfs",
			mounts: `
tmpfs /etc tmpfs rw,nosuid,nodev 0 0
overlay / overlay rw,relatime 0 0
`,
			want: installMethodCron,
		},
		{
			name: "uses init when etc is persistent",
			mounts: `
overlay / overlay rw,relatime 0 0
overlay /etc overlay rw,relatime 0 0
`,
			want: installMethodInit,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := detectServiceInstallMethodFromMounts(tt.mounts)
			if got != tt.want {
				t.Fatalf("detectServiceInstallMethodFromMounts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderCronBootstrapScript(t *testing.T) {
	t.Parallel()

	script := renderCronBootstrapScript("/mnt/usb/vpn-manager/vpn-manager", "/mnt/usb/vpn-manager", "18080")

	for _, fragment := range []string{
		`PROG="/mnt/usb/vpn-manager/vpn-manager"`,
		`ROOT_DIR="/mnt/usb/vpn-manager"`,
		`PORT="18080"`,
		`PID_FILE="/tmp/vpn-manager.pid"`,
		`PATH="$ROOT_DIR/bin:$ROOT_DIR/.vpn-manager/bin:/usr/sbin:/usr/bin:/sbin:/bin"`,
		`UPDATE_JOURNAL="$ROOT_DIR/.update-journal.json"`,
		`UPDATE_LOCK="/tmp/vpn-manager.updating"`,
		`RUNNING_EXE="$(readlink -f "/proc/$PID/exe" 2>/dev/null)"`,
		`RUNNING_EXE="${RUNNING_EXE% (deleted)}"`,
		`[ "$RUNNING_EXE" = "$EXPECTED_EXE" ] && exit 0`,
		`for PROC_EXE in /proc/[0-9]*/exe; do`,
		`echo "$PID" >"$PID_FILE"`,
		`[ -e "$UPDATE_JOURNAL" ] && exit 0`,
		`[ -e "$UPDATE_LOCK" ] && exit 0`,
		`export VPN_MANAGER_ROOT="$ROOT_DIR"`,
		`export VPN_MANAGER_PORT="$PORT"`,
		`export PATH`,
		`/sbin/start-stop-daemon -S -q -b -m -p "$PID_FILE" -x "$PROG"`,
		`"$PROG" >>"$LOG_FILE" 2>&1 </dev/null &`,
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("renderCronBootstrapScript() missing fragment %q", fragment)
		}
	}
	if strings.Contains(script, "pgrep") {
		t.Fatal("watchdog must validate its PID and executable instead of matching a process name")
	}
}

func TestRenderInitScriptIncludesBundleBinPath(t *testing.T) {
	t.Parallel()

	script := renderInitScript("/mnt/usb/vpn-manager/vpn-manager", "/mnt/usb/vpn-manager", "18080")

	for _, fragment := range []string{
		`PROG="/mnt/usb/vpn-manager/vpn-manager"`,
		`ROOT_DIR="/mnt/usb/vpn-manager"`,
		`PORT="18080"`,
		`PATH_ENV="/mnt/usb/vpn-manager/bin:/mnt/usb/vpn-manager/.vpn-manager/bin:/usr/sbin:/usr/bin:/sbin:/bin"`,
		`procd_set_param env VPN_MANAGER_ROOT="$ROOT_DIR" VPN_MANAGER_PORT="$PORT" PATH="$PATH_ENV"`,
		`procd_set_param respawn 3600 5 0`,
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("renderInitScript() missing fragment %q", fragment)
		}
	}
}

func TestUpsertCronEntryAddsOnlyOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "root")
	if err := os.WriteFile(path, []byte("*/5 * * * * existing-job\n"), 0o600); err != nil {
		t.Fatalf("write initial crontab: %v", err)
	}

	entry := "* * * * * '/mnt/usb/vpn-manager/vpn-manager-autostart.sh'"

	changed, err := upsertCronEntry(path, entry)
	if err != nil {
		t.Fatalf("upsertCronEntry() first call error: %v", err)
	}
	if !changed {
		t.Fatal("upsertCronEntry() first call did not report change")
	}

	changed, err = upsertCronEntry(path, entry)
	if err != nil {
		t.Fatalf("upsertCronEntry() second call error: %v", err)
	}
	if changed {
		t.Fatal("upsertCronEntry() second call reported change for duplicate entry")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read crontab: %v", err)
	}
	if strings.Count(string(data), entry) != 1 {
		t.Fatalf("crontab should contain %q exactly once, got:\n%s", entry, string(data))
	}
}

func TestRemoveCronEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "root")
	entry := "* * * * * '/mnt/usb/vpn-manager/vpn-manager-autostart.sh'"
	content := strings.Join([]string{
		"*/5 * * * * existing-job",
		entry,
		"0 1 * * * another-job",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write initial crontab: %v", err)
	}

	changed, err := removeCronEntry(path, entry)
	if err != nil {
		t.Fatalf("removeCronEntry() error: %v", err)
	}
	if !changed {
		t.Fatal("removeCronEntry() did not report change")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read crontab: %v", err)
	}
	if strings.Contains(string(data), entry) {
		t.Fatalf("crontab still contains removed entry:\n%s", string(data))
	}
	if !strings.Contains(string(data), "existing-job") || !strings.Contains(string(data), "another-job") {
		t.Fatalf("crontab removed unrelated entries:\n%s", string(data))
	}
}
