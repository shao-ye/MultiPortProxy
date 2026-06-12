package main

import (
	"os/exec"
	"syscall"
)

// hideWindow 设置子进程启动参数，避免 mihomo 内核弹出黑色控制台窗口
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
