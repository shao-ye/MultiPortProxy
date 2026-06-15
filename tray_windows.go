package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	_ "embed"
)

// app.ico 嵌入二进制，运行时写到临时文件供 LoadImageW 加载为托盘图标。
// 嵌入而非依赖 exe 的图标资源：本地未走 goversioninfo 的调试构建也能正常显示图标。
//
//go:embed app.ico
var appIcoData []byte

// ---------- Win32 API 句柄 ----------
// 复用 detect_windows.go 中已声明的 kernel32；这里补充托盘所需的 user32/shell32 过程。
var (
	user32  = syscall.NewLazyDLL("user32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procLoadImageW         = user32.NewProc("LoadImageW")
	procLoadIconW          = user32.NewProc("LoadIconW")
	procCreatePopupMenu    = user32.NewProc("CreatePopupMenu")
	procAppendMenuW        = user32.NewProc("AppendMenuW")
	procTrackPopupMenu     = user32.NewProc("TrackPopupMenu")
	procDestroyMenu        = user32.NewProc("DestroyMenu")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
)

// ---------- 常量 ----------
const (
	wmApp            = 0x8000
	wmTrayCallback   = wmApp + 1 // 托盘图标的鼠标事件回调消息
	wmShowCloseTip   = wmApp + 2 // 自定义消息：在托盘线程弹出"已最小化"气泡

	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmDestroy       = 0x0002

	nimAdd    = 0x0000
	nimModify = 0x0001
	nimDelete = 0x0002

	nifMessage = 0x0001
	nifIcon    = 0x0002
	nifTip     = 0x0004
	nifInfo    = 0x0010

	niifInfo = 0x0001

	imageIcon      = 1
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040

	idiApplication = 32512

	mfString        = 0x0000
	tpmReturnCmd    = 0x0100
	tpmRightButton  = 0x0002

	menuShow = 1 // 托盘右键菜单：显示界面
	menuExit = 2 // 托盘右键菜单：完全退出
)

// ---------- Win32 结构体（64 位布局） ----------

type point struct{ x, y int32 }

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type msgStruct struct {
	hwnd     syscall.Handle
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

// notifyIconData 对应 NOTIFYICONDATAW（含气泡相关字段，cbSize 取完整结构大小）
type notifyIconData struct {
	cbSize           uint32
	hWnd             syscall.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            syscall.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeoutVersion  uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     syscall.Handle
}

// ---------- 托盘运行时状态（单实例） ----------
var (
	trayHwnd     syscall.Handle
	trayNID      notifyIconData
	trayURL      string
	closeTipOnce sync.Once
	wndProcCB    = syscall.NewCallback(wndProc)
)

// utf16Ptr 把 Go 字符串转成以 NUL 结尾的 UTF-16 指针（出错时返回空字符串指针）
func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

// copyUTF16 把字符串写入定长 UTF-16 数组，先清零避免残留旧内容，并保证截断后仍以 NUL 结尾
func copyUTF16(dst []uint16, s string) {
	for i := range dst {
		dst[i] = 0
	}
	u := utf16.Encode([]rune(s))
	n := copy(dst, u)
	if n >= len(dst) {
		dst[len(dst)-1] = 0
	}
}

// loadAppIcon 把嵌入的 app.ico 写到临时文件并加载为 HICON；失败时退回系统默认应用图标
func loadAppIcon(hInstance uintptr) syscall.Handle {
	icoPath := filepath.Join(os.TempDir(), "nodeportproxy_tray.ico")
	if err := os.WriteFile(icoPath, appIcoData, 0o644); err == nil {
		h, _, _ := procLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(utf16Ptr(icoPath))),
			imageIcon, 0, 0,
			lrLoadFromFile|lrDefaultSize,
		)
		if h != 0 {
			return syscall.Handle(h)
		}
	}
	h, _, _ := procLoadIconW.Call(0, idiApplication)
	return syscall.Handle(h)
}

// wndProc 是托盘隐藏窗口的消息处理函数
func wndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		// lParam 低位是具体的鼠标事件
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			openBrowser(trayURL) // 左键/双击：重新打开界面窗口
		case wmRButtonUp:
			showTrayMenu(hwnd) // 右键：弹出菜单
		}
		return 0
	case wmShowCloseTip:
		showCloseTip()
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

// showTrayMenu 在鼠标位置弹出托盘右键菜单（显示界面 / 完全退出）
func showTrayMenu(hwnd uintptr) {
	hmenu, _, _ := procCreatePopupMenu.Call()
	procAppendMenuW.Call(hmenu, mfString, menuShow, uintptr(unsafe.Pointer(utf16Ptr("显示界面"))))
	procAppendMenuW.Call(hmenu, mfString, menuExit, uintptr(unsafe.Pointer(utf16Ptr("完全退出"))))

	// 必须先把窗口设为前台，否则菜单在点击别处时不会自动消失
	procSetForegroundWindow.Call(hwnd)
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// TPM_RETURNCMD：直接返回被选中的菜单项 ID，无需再处理 WM_COMMAND
	cmd, _, _ := procTrackPopupMenu.Call(
		hmenu, tpmReturnCmd|tpmRightButton,
		uintptr(pt.x), uintptr(pt.y), 0, hwnd, 0,
	)
	procDestroyMenu.Call(hmenu)

	switch cmd {
	case menuShow:
		openBrowser(trayURL)
	case menuExit:
		trayExit()
	}
}

// showCloseTip 弹出"已最小化到托盘"气泡提示（仅首次关闭窗口时触发）
func showCloseTip() {
	trayNID.uFlags = nifInfo
	copyUTF16(trayNID.szInfoTitle[:], "多节点端口代理")
	copyUTF16(trayNID.szInfo[:], "已最小化到托盘，仍在后台运行。\n右键托盘图标可「显示界面」或「完全退出」。")
	trayNID.dwInfoFlags = niifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&trayNID)))
	// 恢复常规标志，避免后续 Modify 再次触发气泡
	trayNID.uFlags = nifMessage | nifIcon | nifTip
}

// trayExit 彻底退出：移除托盘图标、停止内核、结束进程
func trayExit() {
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&trayNID)))
	core.Stop()
	os.Exit(0)
}

// trayOnUIClose 由 /api/ui-closed 在界面窗口关闭时调用：首次关闭弹气泡提示。
// 通过 PostMessage 把动作投递回托盘线程执行，避免跨线程操作 Shell_NotifyIcon。
func trayOnUIClose() {
	if trayHwnd == 0 {
		return
	}
	closeTipOnce.Do(func() {
		procPostMessageW.Call(uintptr(trayHwnd), wmShowCloseTip, 0, 0)
	})
}

// runTray 在主线程创建隐藏窗口与托盘图标，并进入消息循环（阻塞直到「完全退出」）。
// 关闭界面窗口不会退出本进程，程序最小化到托盘继续提供服务。
func runTray(url string) {
	trayURL = url
	// GUI 消息循环必须固定在同一个 OS 线程
	runtime.LockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16Ptr("NodePortProxyTray")
	hIcon := loadAppIcon(hInstance)

	// 注册窗口类
	var wc wndClassExW
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	wc.lpfnWndProc = wndProcCB
	wc.hInstance = syscall.Handle(hInstance)
	wc.hIcon = hIcon
	wc.lpszClassName = className
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// 创建一个不显示的窗口，仅用于接收托盘消息
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("多节点端口代理"))),
		0, 0, 0, 0, 0, 0, 0, hInstance, 0,
	)
	trayHwnd = syscall.Handle(hwnd)

	// 添加托盘图标
	trayNID.cbSize = uint32(unsafe.Sizeof(trayNID))
	trayNID.hWnd = trayHwnd
	trayNID.uID = 1
	trayNID.uFlags = nifMessage | nifIcon | nifTip
	trayNID.uCallbackMessage = wmTrayCallback
	trayNID.hIcon = hIcon
	copyUTF16(trayNID.szTip[:], "多节点端口代理")
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&trayNID)))

	// 消息循环
	var msg msgStruct
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0=收到 WM_QUIT，-1=出错，均退出循环
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	trayExit()
}
