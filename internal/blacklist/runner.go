package blacklist

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Runner struct {
	scriptPath string
	workingDir string
}

func NewRunner(scriptPath string) *Runner {
	workingDir := filepath.Dir(filepath.Dir(scriptPath))
	if filepath.Base(filepath.Dir(scriptPath)) != generatedDirName {
		workingDir = filepath.Dir(scriptPath)
	}
	return &Runner{scriptPath: scriptPath, workingDir: workingDir}
}

type RunOptions struct {
	DomainsListPath string
	IPsListPath     string
	DnsmasqFile     string
	LANIface        string
}

func (r *Runner) Run(ctx context.Context, action string, opts RunOptions) error {
	shell := os.Getenv("VPN_SCRIPT_SHELL")
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "bash"
		} else {
			shell = "sh"
		}
	}

	scriptPath := r.scriptPath
	if runtime.GOOS == "windows" {
		scriptPath = filepath.ToSlash(scriptPath)
	}

	dnsmasqFile := opts.DnsmasqFile
	if dnsmasqFile == "" {
		dnsmasqFile = "/tmp/dnsmasq.d/blacklist_dns.conf"
	}
	lanIface := opts.LANIface
	if lanIface == "" {
		lanIface = "br-lan"
	}

	cmd := exec.CommandContext(ctx, shell, scriptPath, action)
	cmd.Dir = r.workingDir
	cmd.Env = withEnv(os.Environ(), map[string]string{
		"BLACKLIST_DOMAINS_FILE": opts.DomainsListPath,
		"BLACKLIST_IPS_FILE":     opts.IPsListPath,
		"BLACKLIST_DNSMASQ_FILE": dnsmasqFile,
		"LAN_IFACE":             lanIface,
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

func withEnv(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, item := range env {
		skip := false
		for key := range values {
			if strings.HasPrefix(item, key+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, item)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
