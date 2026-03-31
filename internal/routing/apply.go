package routing

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"xiomi-router-driver/internal/config"
)

type Runner struct {
	scriptPath string
	workingDir string
}

type RunOptions struct {
	Settings       config.RoutingSettings
	DomainListPath string
}

func NewRunner(scriptPath string) *Runner {
	workingDir := filepath.Dir(filepath.Dir(scriptPath))
	if filepath.Base(filepath.Dir(scriptPath)) != generatedDirName {
		workingDir = filepath.Dir(scriptPath)
	}

	return &Runner{
		scriptPath: scriptPath,
		workingDir: workingDir,
	}
}

func (r *Runner) Run(ctx context.Context, action string, settings config.RoutingSettings) error {
	return r.RunWithOptions(ctx, action, RunOptions{Settings: settings})
}

func (r *Runner) RunWithOptions(ctx context.Context, action string, options RunOptions) error {
	shell := os.Getenv("VPN_SCRIPT_SHELL")
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "bash"
		} else {
			shell = "sh"
		}
	}

	scriptPath := r.scriptPath
	domainListPath := strings.TrimSpace(options.DomainListPath)
	if domainListPath == "" {
		domainListPath = "domains.list"
	}

	if runtime.GOOS == "windows" {
		scriptPath = normalizePathForShell(shell, scriptPath)
		if filepath.IsAbs(domainListPath) {
			domainListPath = normalizePathForShell(shell, domainListPath)
		}
	}

	cmd := exec.CommandContext(ctx, shell, scriptPath, action)
	cmd.Dir = r.workingDir
	cmd.Env = withEnvMap(os.Environ(), map[string]string{
		"DOMAIN_LIST":               domainListPath,
		"DOMAIN_STATS_MAX_DOMAINS":  strconv.Itoa(DomainStatsMaxDomains()),
		"VPN_GATEWAY":               options.Settings.VPNGateway,
		"VPN_ROUTE_MODE":            options.Settings.VPNRouteMode,
		"VPN_MASQUERADE":            boolToScriptValue(options.Settings.VPNMasquerade),
		"LAN_IFACE":                 options.Settings.LANIface,
		"VPN_IFACE":                 options.Settings.VPNIface,
		"TABLE_NUM":                 strconv.Itoa(options.Settings.TableNum),
		"FW_ZONE_CHAIN":             options.Settings.FWZoneChain,
		"IPSET_NAME":                options.Settings.IPSetName,
		"FWMARK":                    options.Settings.FWMark,
		"DNSMASQ_CONFIG_FILE":       options.Settings.DNSMasqConfigFile,
		"DOMAIN_STATS_CHAIN":        DomainStatsChainName(options.Settings.IPSetName),
		"LEGACY_DOMAIN_STATS_CHAIN": LegacyDomainStatsChainName(options.Settings.IPSetName),
	})
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("run %s %s: %s", r.scriptPath, action, message)
	}

	return nil
}

func normalizePathForShell(shell string, scriptPath string) string {
	shellPath, err := exec.LookPath(shell)
	if err != nil {
		return filepath.ToSlash(scriptPath)
	}

	lowerShellPath := strings.ToLower(filepath.Clean(shellPath))
	switch {
	case strings.HasSuffix(lowerShellPath, `\windows\system32\bash.exe`):
		return toWSLPath(scriptPath)
	case strings.Contains(lowerShellPath, `\git\bin\bash.exe`), strings.Contains(lowerShellPath, `\git\usr\bin\bash.exe`):
		return filepath.ToSlash(scriptPath)
	default:
		return filepath.ToSlash(scriptPath)
	}
}

func toWSLPath(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) == 2 && volume[1] == ':' {
		drive := strings.ToLower(volume[:1])
		rest := strings.TrimPrefix(path, volume)
		rest = strings.TrimLeft(rest, `\/`)
		rest = filepath.ToSlash(rest)
		if rest == "" {
			return "/mnt/" + drive
		}
		return "/mnt/" + drive + "/" + rest
	}

	return filepath.ToSlash(path)
}

func withEnvMap(env []string, values map[string]string) []string {
	prefixes := make([]string, 0, len(values))
	for key := range values {
		prefixes = append(prefixes, key+"=")
	}

	out := make([]string, 0, len(env)+1)

	for _, item := range env {
		if hasAnyPrefix(item, prefixes) {
			continue
		}
		out = append(out, item)
	}

	for key, value := range values {
		out = append(out, key+"="+value)
	}

	return out
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}

	return false
}

func boolToScriptValue(value bool) string {
	if value {
		return "1"
	}

	return "0"
}
