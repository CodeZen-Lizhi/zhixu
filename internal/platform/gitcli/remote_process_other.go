//go:build !unix

package gitcli

import (
	"os/exec"
	"time"
)

func configureRemoteCommandCancellation(command *exec.Cmd) {
	command.WaitDelay = 2 * time.Second
}
