package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type resultLevel string

const (
	levelOK   resultLevel = "OK"
	levelWarn resultLevel = "WARN"
	levelFail resultLevel = "FAIL"
)

type checkResult struct {
	Level  resultLevel
	Name   string
	Detail string
}

// Run executes lightweight diagnostics intended to be run on the router as:
//
//	vpn-manager doctor
//
// It avoids changing firewall/routing state and only reads system status.
func Run(w io.Writer) int {
	checks := []checkResult{
		{Level: levelOK, Name: "host", Detail: fmt.Sprintf("%s/%s at %s", runtime.GOOS, runtime.GOARCH, time.Now().Format(time.RFC3339))},
	}

	if runtime.GOOS != "linux" {
		checks = append(checks, checkResult{Level: levelWarn, Name: "platform", Detail: "router dataplane checks are designed for Linux/OpenWrt"})
	}

	if os.Geteuid() != 0 {
		checks = append(checks, checkResult{Level: levelWarn, Name: "root", Detail: "not running as root; some router checks may be incomplete"})
	} else {
		checks = append(checks, checkResult{Level: levelOK, Name: "root", Detail: "running as root"})
	}

	for _, command := range []string{"ip", "iptables", "ipset", "dnsmasq", "flock"} {
		checks = append(checks, checkCommand(command, true))
	}
	for _, command := range []string{"conntrack", "nft"} {
		checks = append(checks, checkCommand(command, false))
	}

	checks = append(checks, checkCommandOutput("iptables backend", "iptables", "--version"))
	checks = append(checks, checkCommandOutput("ip rules", "ip", "rule", "show"))
	checks = append(checks, checkCommandOutput("main route", "ip", "route", "show", "table", "main"))

	lanIface := firstNonEmpty(os.Getenv("LAN_IFACE"), "br-lan")
	vpnIface := firstNonEmpty(os.Getenv("VPN_IFACE"), "tun0")
	checks = append(checks, checkInterface("LAN interface", lanIface, true))
	checks = append(checks, checkInterface("VPN interface", vpnIface, false))

	checks = append(checks, checkSysctl("IPv4 forwarding", "net.ipv4.ip_forward", "1", true))
	checks = append(checks, checkSysctl("IPv6 forwarding", "net.ipv6.conf.all.forwarding", "0", false))

	dnsmasqPath := firstNonEmpty(os.Getenv("DNSMASQ_CONFIG_FILE"), "/tmp/dnsmasq.d/vpn_dns.conf")
	checks = append(checks, checkDNSMasqPath(dnsmasqPath))

	failures := 0
	fmt.Fprintln(w, "VPN Manager doctor")
	fmt.Fprintln(w, "==================")
	for _, check := range checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", check.Level, check.Name, check.Detail)
		if check.Level == levelFail {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(w, "\n%d critical check(s) failed.\n", failures)
		return 1
	}
	fmt.Fprintln(w, "\nNo critical checks failed.")
	return 0
}

func checkCommand(command string, critical bool) checkResult {
	path, err := exec.LookPath(command)
	if err != nil {
		level := levelWarn
		if critical {
			level = levelFail
		}
		return checkResult{Level: level, Name: command, Detail: "not found in PATH"}
	}
	return checkResult{Level: levelOK, Name: command, Detail: path}
}

func checkCommandOutput(name string, command string, args ...string) checkResult {
	if _, err := exec.LookPath(command); err != nil {
		return checkResult{Level: levelWarn, Name: name, Detail: command + " not found"}
	}
	output, err := runCommand(command, args...)
	detail := strings.TrimSpace(output)
	if detail == "" {
		detail = "command completed"
	}
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	if err != nil {
		return checkResult{Level: levelWarn, Name: name, Detail: err.Error() + ": " + detail}
	}
	firstLine := strings.Split(detail, "\n")[0]
	return checkResult{Level: levelOK, Name: name, Detail: firstLine}
}

func checkInterface(name string, iface string, critical bool) checkResult {
	if strings.TrimSpace(iface) == "" {
		return checkResult{Level: levelWarn, Name: name, Detail: "interface name is empty"}
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return checkResult{Level: levelWarn, Name: name, Detail: "ip command not found"}
	}
	output, err := runCommand("ip", "-o", "link", "show", iface)
	if err != nil {
		level := levelWarn
		if critical {
			level = levelFail
		}
		return checkResult{Level: level, Name: name, Detail: iface + " is missing"}
	}
	detail := strings.TrimSpace(output)
	if detail == "" {
		detail = iface + " exists"
	}
	return checkResult{Level: levelOK, Name: name, Detail: detail}
}

func checkSysctl(name string, key string, expected string, critical bool) checkResult {
	output, err := runCommand("sysctl", "-n", key)
	if err != nil {
		return checkResult{Level: levelWarn, Name: name, Detail: "sysctl unavailable for " + key}
	}
	value := strings.TrimSpace(output)
	if value == expected {
		return checkResult{Level: levelOK, Name: name, Detail: key + "=" + value}
	}
	level := levelWarn
	if critical {
		level = levelFail
	}
	return checkResult{Level: level, Name: name, Detail: key + "=" + value + ", expected " + expected}
}

func checkDNSMasqPath(path string) checkResult {
	path = strings.TrimSpace(path)
	if path == "" {
		return checkResult{Level: levelWarn, Name: "dnsmasq config", Detail: "DNSMASQ_CONFIG_FILE is empty"}
	}
	dir := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		dir = path[:idx]
	}
	if dir == "" {
		dir = "."
	}
	info, err := os.Stat(dir)
	if err != nil {
		return checkResult{Level: levelWarn, Name: "dnsmasq config", Detail: dir + " does not exist"}
	}
	if !info.IsDir() {
		return checkResult{Level: levelWarn, Name: "dnsmasq config", Detail: dir + " is not a directory"}
	}
	return checkResult{Level: levelOK, Name: "dnsmasq config", Detail: "directory exists: " + dir}
}

func runCommand(command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(output), context.DeadlineExceeded
	}
	return string(output), err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
