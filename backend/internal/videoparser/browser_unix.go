//go:build unix

package videoparser

import (
	"os/exec"
	"syscall"
)

// browserPipeSupported：调试管道依赖 ExtraFiles（fd 3 / fd 4），只在 Unix 上可用。
const browserPipeSupported = true

// prepareBrowserCmd 让浏览器及其子进程处于独立进程组，结束时整组清理。
func prepareBrowserCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killBrowserTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
