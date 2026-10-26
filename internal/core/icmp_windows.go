package core

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps ping.exe from flashing a console window every check.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
