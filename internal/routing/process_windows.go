//go:build windows

package routing

import (
	"os/exec"
	"time"
)

func configureRoutingProcess(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
