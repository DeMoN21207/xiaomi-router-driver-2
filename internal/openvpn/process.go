package openvpn

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

func startProcess(ctx context.Context, executable, profilePath, interfaceName string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	profileDir := filepath.Dir(profilePath)
	args := []string{"--cd", profileDir, "--config", filepath.Base(profilePath), "--route-noexec"}
	if iface := strings.TrimSpace(interfaceName); iface != "" {
		args = append(args, "--dev", iface)
	}
	// The manager owns this process after startup. An apply request ending
	// must not terminate the tunnel; failed startup is cleaned up by Apply.
	cmd := exec.Command(executable, args...)
	cmd.Dir = profileDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("prepare openvpn stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, nil, nil, fmt.Errorf("prepare openvpn stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start openvpn: %w", err)
	}
	return cmd, stdout, stderr, nil
}
