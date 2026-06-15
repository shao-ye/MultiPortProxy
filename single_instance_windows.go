package main

import (
	"syscall"
	"unsafe"
)

var procCreateMutexW = kernel32.NewProc("CreateMutexW")

const errorAlreadyExists syscall.Errno = 183

// acquireSingleInstance 使用 Windows 命名互斥锁保证后端进程只有一个。
// 返回的 release 需要由首个实例持有到进程退出；第二个返回值表示已有实例在运行。
func acquireSingleInstance() (release func(), alreadyRunning bool) {
	name := utf16Ptr(`Local\MultiPortProxy.SingleInstance`)
	h, _, err := procCreateMutexW.Call(
		0,
		1,
		uintptr(unsafe.Pointer(name)),
	)
	if h == 0 {
		// 创建失败时退回原有端口探测逻辑，避免误伤正常启动。
		return func() {}, false
	}
	return func() {
		syscall.CloseHandle(syscall.Handle(h))
	}, err == errorAlreadyExists
}
