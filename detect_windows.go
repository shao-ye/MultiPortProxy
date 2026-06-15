package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 通过 Windows API 枚举进程来定位 v2rayN 安装目录。
// 全程进程内调用，不启动任何子进程（避免被杀软当成可疑行为）。
var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot  = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW           = kernel32.NewProc("Process32FirstW")
	procProcess32NextW            = kernel32.NewProc("Process32NextW")
	procOpenProcess               = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

// processEntry32 对应 Windows 的 PROCESSENTRY32W 结构
type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

// findV2rayNDir 枚举系统进程，找到 v2rayN.exe 并返回它所在的目录；找不到返回空串。
// 用户通常是开着 v2rayN 使用本应用的，所以这是最可靠的内核定位方式。
func findV2rayNDir() string {
	const th32csSnapProcess = 0x00000002
	const procQueryLimitedInformation = 0x1000

	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == uintptr(syscall.InvalidHandle) {
		return ""
	}
	defer syscall.CloseHandle(syscall.Handle(snap))

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	ret, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "v2rayN.exe") {
			// 打开进程，查询其可执行文件的完整路径
			h, _, _ := procOpenProcess.Call(procQueryLimitedInformation, 0, uintptr(entry.ProcessID))
			if h != 0 {
				var buf [520]uint16
				size := uint32(len(buf))
				r, _, _ := procQueryFullProcessImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
				syscall.CloseHandle(syscall.Handle(h))
				if r != 0 {
					return filepath.Dir(syscall.UTF16ToString(buf[:size]))
				}
			}
		}
		ret, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
	return ""
}
