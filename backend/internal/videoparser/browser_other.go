//go:build !unix

package videoparser

import "os/exec"

const browserPipeSupported = false

func prepareBrowserCmd(*exec.Cmd) {}

func killBrowserTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
