package automation

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"xiomi-router-driver/internal/config"
)

const serviceName = "vpn-manager"

const (
	procMountsPath                         = "/proc/mounts"
	cronFilePath                           = "/etc/crontabs/root"
	cronServicePath                        = "/etc/init.d/cron"
	cronBootstrapName                      = "vpn-manager-autostart.sh"
	installMethodInit serviceInstallMethod = "init"
	installMethodCron serviceInstallMethod = "cron"
)

type serviceInstallMethod string

type Manager struct {
	rootDir    string
	binaryPath string
	port       string
	scriptPath string
}

func NewManager(rootDir string, binaryPath string, port string) *Manager {
	port = strings.TrimSpace(port)
	if port == "" {
		port = "8080"
	}

	return &Manager{
		rootDir:    filepath.Clean(rootDir),
		binaryPath: filepath.Clean(binaryPath),
		port:       port,
		scriptPath: filepath.Join("/etc/init.d", serviceName),
	}
}

func (m *Manager) Validate(settings config.AutomationSettings) error {
	if !settings.InstallService {
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("system service install is supported only on Linux/OpenWrt")
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if _, err := os.Stat(m.binaryPath); err != nil {
		return fmt.Errorf("check binary path: %w", err)
	}

	method, err := detectServiceInstallMethod(procMountsPath)
	if err != nil {
		return err
	}

	switch method {
	case installMethodInit:
		info, err := os.Stat(filepath.Dir(m.scriptPath))
		if err != nil {
			return fmt.Errorf("check init.d directory: %w", err)
		}
		if !info.IsDir() {
			return errors.New("/etc/init.d is not a directory")
		}
	case installMethodCron:
		info, err := os.Stat(filepath.Dir(cronFilePath))
		if err != nil {
			return fmt.Errorf("check crontab directory: %w", err)
		}
		if !info.IsDir() {
			return errors.New("/etc/crontabs is not a directory")
		}
	default:
		return fmt.Errorf("unsupported service install method: %s", method)
	}
	return nil
}

func (m *Manager) Sync(settings config.AutomationSettings) error {
	if !settings.InstallService {
		return m.disable()
	}
	if err := m.Validate(settings); err != nil {
		return err
	}
	return m.install()
}

func (m *Manager) ServicePath() string {
	method, err := detectServiceInstallMethod(procMountsPath)
	if err == nil && method == installMethodCron {
		return m.bootstrapScriptPath()
	}
	return m.scriptPath
}

func (m *Manager) install() error {
	method, err := detectServiceInstallMethod(procMountsPath)
	if err != nil {
		return err
	}

	switch method {
	case installMethodCron:
		if err := m.installCronWatchdog(); err != nil {
			return err
		}
		return m.disableInitScript()
	case installMethodInit:
		if err := m.installInitScript(); err != nil {
			return err
		}
		return m.disableCronWatchdog()
	default:
		return fmt.Errorf("unsupported service install method: %s", method)
	}
}

func (m *Manager) disable() error {
	if runtime.GOOS != "linux" {
		return nil
	}

	if err := m.disableInitScript(); err != nil {
		return err
	}
	if err := m.disableCronWatchdog(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) installInitScript() error {
	if err := os.MkdirAll(filepath.Dir(m.scriptPath), 0o755); err != nil {
		return fmt.Errorf("prepare init.d directory: %w", err)
	}

	script := renderInitScript(m.binaryPath, m.rootDir, m.port)
	if err := os.WriteFile(m.scriptPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write init script: %w", err)
	}

	enableCmd := exec.Command(m.scriptPath, "enable")
	if output, err := enableCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("enable init script: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) disableInitScript() error {
	if _, err := os.Stat(m.scriptPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check init script: %w", err)
	}

	disableCmd := exec.Command(m.scriptPath, "disable")
	if output, err := disableCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("disable init script: %s", strings.TrimSpace(string(output)))
	}

	if err := os.Remove(m.scriptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove init script: %w", err)
	}
	return nil
}

func (m *Manager) installCronWatchdog() error {
	if err := os.MkdirAll(filepath.Dir(cronFilePath), 0o755); err != nil {
		return fmt.Errorf("prepare crontab directory: %w", err)
	}

	script := renderCronBootstrapScript(m.binaryPath, m.rootDir, m.port)
	if err := os.WriteFile(m.bootstrapScriptPath(), []byte(script), 0o755); err != nil {
		return fmt.Errorf("write cron bootstrap script: %w", err)
	}

	changed, err := upsertCronEntry(cronFilePath, m.cronEntry())
	if err != nil {
		return err
	}
	if changed {
		if err := reloadCronService(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) disableCronWatchdog() error {
	changed, err := removeCronEntry(cronFilePath, m.cronEntry())
	if err != nil {
		return err
	}

	if err := os.Remove(m.bootstrapScriptPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove cron bootstrap script: %w", err)
	}

	if changed {
		if err := reloadCronService(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) cronEntry() string {
	return fmt.Sprintf("* * * * * %s", shellSingleQuoted(m.bootstrapScriptPath()))
}

func (m *Manager) bootstrapScriptPath() string {
	return filepath.Join(m.rootDir, cronBootstrapName)
}

func renderInitScript(binaryPath string, rootDir string, port string) string {
	return fmt.Sprintf(`#!/bin/sh /etc/rc.common

USE_PROCD=1
START=99
STOP=10

PROG="%s"
ROOT_DIR="%s"
PORT="%s"
PATH_ENV="%s/bin:%s/.vpn-manager/bin:/usr/sbin:/usr/bin:/sbin:/bin"

start_service() {
	[ -x "$PROG" ] || return 1

	procd_open_instance
	procd_set_param command "$PROG"
	procd_set_param env VPN_MANAGER_ROOT="$ROOT_DIR" VPN_MANAGER_PORT="$PORT" PATH="$PATH_ENV"
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_set_param respawn 3600 5 5
	procd_close_instance
}
`, escapeShellDoubleQuoted(binaryPath), escapeShellDoubleQuoted(rootDir), escapeShellDoubleQuoted(port), escapeShellDoubleQuoted(rootDir), escapeShellDoubleQuoted(rootDir))
}

func renderCronBootstrapScript(binaryPath string, rootDir string, port string) string {
	return fmt.Sprintf(`#!/bin/sh

PROG="%s"
ROOT_DIR="%s"
PORT="%s"
LOG_FILE="/tmp/vpn-manager.log"
PID_FILE="/tmp/vpn-manager.pid"
PATH="$ROOT_DIR/bin:$ROOT_DIR/.vpn-manager/bin:/usr/sbin:/usr/bin:/sbin:/bin"

[ -x "$PROG" ] || exit 0
pgrep -x "%s" >/dev/null 2>&1 && exit 0

export VPN_MANAGER_ROOT="$ROOT_DIR"
export VPN_MANAGER_PORT="$PORT"
export PATH

if [ -x /sbin/start-stop-daemon ]; then
	/sbin/start-stop-daemon -S -q -b -m -p "$PID_FILE" -x "$PROG"
	exit 0
fi

"$PROG" >>"$LOG_FILE" 2>&1 </dev/null &
`, escapeShellDoubleQuoted(binaryPath), escapeShellDoubleQuoted(rootDir), escapeShellDoubleQuoted(port), serviceName)
}

func detectServiceInstallMethod(mountsPath string) (serviceInstallMethod, error) {
	data, err := os.ReadFile(mountsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return installMethodInit, nil
		}
		return "", fmt.Errorf("read mount table: %w", err)
	}
	return detectServiceInstallMethodFromMounts(string(data)), nil
}

func detectServiceInstallMethodFromMounts(mounts string) serviceInstallMethod {
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[1] != "/etc" {
			continue
		}
		if slices.Contains([]string{"ramfs", "tmpfs"}, fields[2]) {
			return installMethodCron
		}
	}
	return installMethodInit
}

func upsertCronEntry(path string, entry string) (bool, error) {
	lines, mode, err := readLinesWithMode(path)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == strings.TrimSpace(entry) {
			return false, nil
		}
	}
	lines = append(lines, entry)
	if err := writeLines(path, lines, mode); err != nil {
		return false, err
	}
	return true, nil
}

func removeCronEntry(path string, entry string) (bool, error) {
	lines, mode, err := readLinesWithMode(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	trimmedEntry := strings.TrimSpace(entry)
	filtered := lines[:0]
	changed := false
	for _, line := range lines {
		if strings.TrimSpace(line) == trimmedEntry {
			changed = true
			continue
		}
		filtered = append(filtered, line)
	}
	if !changed {
		return false, nil
	}
	if err := writeLines(path, filtered, mode); err != nil {
		return false, err
	}
	return true, nil
}

func readLinesWithMode(path string) ([]string, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text == "" {
		return nil, info.Mode().Perm(), nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return lines, info.Mode().Perm(), nil
}

func writeLines(path string, lines []string, mode os.FileMode) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write crontab file: %w", err)
	}
	return nil
}

func reloadCronService() error {
	if _, err := os.Stat(cronServicePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check cron service: %w", err)
	}

	reloadCmd := exec.Command(cronServicePath, "reload")
	if output, err := reloadCmd.CombinedOutput(); err == nil {
		return nil
	} else if strings.TrimSpace(string(output)) != "" {
		restartCmd := exec.Command(cronServicePath, "restart")
		if restartOutput, restartErr := restartCmd.CombinedOutput(); restartErr != nil {
			return fmt.Errorf("reload cron service: %s; restart cron service: %s", strings.TrimSpace(string(output)), strings.TrimSpace(string(restartOutput)))
		}
		return nil
	}

	restartCmd := exec.Command(cronServicePath, "restart")
	if restartOutput, err := restartCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restart cron service: %s", strings.TrimSpace(string(restartOutput)))
	}
	return nil
}

func escapeShellDoubleQuoted(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "$", `\$`)
	value = strings.ReplaceAll(value, "`", "\\`")
	return value
}

func shellSingleQuoted(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func requireRoot() error {
	cmd := exec.Command("id", "-u")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check effective user: %w", err)
	}
	if strings.TrimSpace(string(output)) != "0" {
		return errors.New("root permissions are required to install the system service")
	}
	return nil
}
