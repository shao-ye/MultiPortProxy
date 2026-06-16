package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	gdi32   = syscall.NewLazyDLL("gdi32.dll")

	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")

	// 自定义对话框（关闭询问 / 设置）所需
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procShowWindow       = user32.NewProc("ShowWindow")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
)

// ---------- 常量 ----------
const (
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1 // 托盘图标的鼠标事件回调消息
	wmShowCloseTip = wmApp + 2 // 自定义消息：在托盘线程弹出"已最小化"气泡
	wmAskClose     = wmApp + 3 // 自定义消息：在托盘线程弹出"关闭方式"询问对话框

	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmSetFont       = 0x0030
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmDestroy       = 0x0002

	bmGetCheck = 0x00F0
	bstChecked = 1

	// 窗口/控件样式
	wsChild           = 0x40000000
	wsVisible         = 0x10000000
	wsTabStop         = 0x00010000
	wsCaption         = 0x00C00000
	wsSysMenu         = 0x00080000
	wsExDlgModalFrame = 0x00000001
	wsExTopmost       = 0x00000008
	wsExToolWindow    = 0x00000080
	bsPushButton      = 0x00000000
	bsDefPushButton   = 0x00000001
	bsAutoCheckBox    = 0x00000003
	bsGroupBox        = 0x00000007
	ssLeft            = 0x00000000
	wsVScroll         = 0x00200000
	cbsDropDownList   = 0x00000003 // 下拉列表（只读，不可手输）

	// 下拉框消息与通知码
	cbAddString = 0x0143
	cbSetCurSel = 0x014E
	cbGetCurSel = 0x0147
	cbnSelChange = 1

	defaultGuiFont = 17
	smCxScreen     = 0
	smCyScreen     = 1
	swShow         = 5
	swRestore      = 9

	// 对话框控件 ID
	idExitBtn       = 101 // 关闭询问：完全退出
	idTrayBtn       = 102 // 关闭询问：最小化到托盘
	idRemember      = 103 // 关闭询问：记住选择 勾选框
	idSetAsk        = 201 // 设置：每次都询问
	idSetExit       = 202 // 设置：直接完全退出
	idSetTray       = 203 // 设置：最小化到托盘
	idLangZh        = 204 // 设置：简体中文
	idLangEn        = 205 // 设置：English
	idSetClose      = 206 // 设置：关闭设置窗口
	idBrowserCombo  = 207 // 设置：界面浏览器下拉框

	// appWindowTitle 是浏览器 app 模式界面窗口的标题（取自网页 <title>），用于精确定位本应用窗口
	// chromiumWindowClass 是 Chromium 顶层窗口的窗口类名，配合标题排除本应用自己的隐藏托盘窗口
	chromiumWindowClass = "Chrome_WidgetWin_1"

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

	mfString       = 0x0000
	tpmReturnCmd   = 0x0100
	tpmRightButton = 0x0002

	menuShow     = 1 // 托盘右键菜单：显示界面
	menuSettings = 3 // 托盘右键菜单：设置（关闭按钮行为）
	menuExit     = 2 // 托盘右键菜单：完全退出
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
	traySt       *AppState // 全局应用状态，托盘读写关闭行为设置
	closeTipOnce sync.Once
	wndProcCB    = syscall.NewCallback(wndProc)

	dialogShowing bool // 正在显示关闭询问/设置对话框时为 true，避免重复弹出
)

var appWindowTitles = []string{"多节点端口代理", "MultiPortProxy"}

func trayLanguage() string {
	if traySt == nil {
		return "zh-CN"
	}
	return traySt.GetLanguage()
}

func trayText(zhCN, en string) string {
	if trayLanguage() == "en" {
		return en
	}
	return zhCN
}

func trayAppName() string {
	return trayText("多节点端口代理", "MultiPortProxy")
}

// windowTitleMatchesApp 判断窗口标题是否正是本应用界面窗口的标题。
// 必须用"精确相等"而非"包含"：app 模式窗口标题就是网页 <title>（中文"多节点端口代理"/英文"MultiPortProxy"），
// 而官网标题"MultiPortProxy · 多节点端口代理"同时包含这两者——用包含匹配会误把用户开着官网的普通浏览器窗口
// 当成应用窗口，导致还原(缩小)该窗口并误判"应用已打开"。
func windowTitleMatchesApp(title string) bool {
	for _, appTitle := range appWindowTitles {
		if title == appTitle {
			return true
		}
	}
	return false
}

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
		if dialogShowing {
			return 0 // 对话框打开期间不响应托盘交互
		}
		// lParam 低位是具体的鼠标事件
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			showOrOpenAppWindow(trayURL) // 左键/双击：显示已有界面窗口
		case wmRButtonUp:
			showTrayMenu(hwnd) // 右键：弹出菜单
		}
		return 0
	case wmShowCloseTip:
		showCloseTip()
		return 0
	case wmAskClose:
		// 关闭界面窗口时弹出"完全退出/最小化到托盘"询问对话框
		if dialogShowing {
			return 0
		}
		dialogShowing = true
		action, remember := showCloseDialog()
		dialogShowing = false
		if remember && (action == "exit" || action == "tray") {
			traySt.SetCloseAction(action) // 勾选"记住"则持久化此选择
		}
		if action == "exit" {
			trayExit()
		} else {
			closeTipOnce.Do(showCloseTip) // 最小化到托盘：首次给个气泡提示
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

// showTrayMenu 在鼠标位置弹出托盘右键菜单（显示界面 / 设置 / 完全退出）
func showTrayMenu(hwnd uintptr) {
	hmenu, _, _ := procCreatePopupMenu.Call()
	procAppendMenuW.Call(hmenu, mfString, menuShow, uintptr(unsafe.Pointer(utf16Ptr(trayText("显示界面", "Show window")))))
	procAppendMenuW.Call(hmenu, mfString, menuSettings, uintptr(unsafe.Pointer(utf16Ptr(trayText("设置", "Settings")))))
	procAppendMenuW.Call(hmenu, mfString, menuExit, uintptr(unsafe.Pointer(utf16Ptr(trayText("完全退出", "Exit")))))

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
		showOrOpenAppWindow(trayURL)
	case menuSettings:
		dialogShowing = true
		showSettingsDialog()
		dialogShowing = false
	case menuExit:
		trayExit()
	}
}

// showCloseTip 弹出"已最小化到托盘"气泡提示（仅首次关闭窗口时触发）
func showCloseTip() {
	trayNID.uFlags = nifInfo
	copyUTF16(trayNID.szInfoTitle[:], trayAppName())
	copyUTF16(trayNID.szInfo[:], trayText(
		"已最小化到托盘，仍在后台运行。\n右键托盘图标可「显示界面」或「完全退出」。",
		"Minimized to the tray and still running in the background.\nRight-click the tray icon to show the window or exit.",
	))
	trayNID.dwInfoFlags = niifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&trayNID)))
	// 恢复常规标志，避免后续 Modify 再次触发气泡
	trayNID.uFlags = nifMessage | nifIcon | nifTip
}

func updateTrayTip() {
	if trayHwnd == 0 {
		return
	}
	trayNID.uFlags = nifMessage | nifIcon | nifTip
	copyUTF16(trayNID.szTip[:], trayAppName())
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&trayNID)))
}

// trayExit 彻底退出：移除托盘图标、关闭界面窗口、停止内核、结束进程
func trayExit() {
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&trayNID)))
	closeAppWindows() // 浏览器 app 窗口是独立进程，需主动关闭，否则会残留空壳窗口
	core.Stop()
	// 完全退出是用户的主动行为，清掉自动恢复标记，下次启动不再自动拉起服务
	if traySt != nil {
		traySt.SetAutoStart(false)
	}
	os.Exit(0)
}

// enumFoundWindows 收集 EnumWindows 回调中匹配到的界面窗口句柄
var (
	enumFoundWindows    []syscall.Handle
	enumBrowserExeNames map[string]bool
)

func windowProcessBaseName(hwnd uintptr) string {
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	const procQueryLimitedInformation = 0x1000
	h, _, _ := procOpenProcess.Call(procQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	var buf [520]uint16
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
}

// enumWindowsProc 是 EnumWindows 的回调：按"标题精确等于应用界面标题 + Chromium 顶层窗口类 + 目标浏览器进程"匹配本应用窗口。
// 标题须精确相等以排除"开着官网的普通浏览器窗口"（见 windowTitleMatchesApp）。
func enumWindowsProc(hwnd uintptr, lparam uintptr) uintptr {
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		return 1
	}
	var title [256]uint16
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	titleText := syscall.UTF16ToString(title[:])
	var cls [128]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
	classText := syscall.UTF16ToString(cls[:])
	if !windowTitleMatchesApp(titleText) || !strings.HasPrefix(classText, chromiumWindowClass) {
		return 1
	}
	if !enumBrowserExeNames[windowProcessBaseName(hwnd)] {
		return 1
	}
	enumFoundWindows = append(enumFoundWindows, syscall.Handle(hwnd))
	return 1
}

var enumWindowsProcCB = syscall.NewCallback(enumWindowsProc)

func appWindows() []syscall.Handle {
	enumFoundWindows = nil
	enumBrowserExeNames = browserAppWindowExeNames()
	if len(enumBrowserExeNames) == 0 {
		return nil
	}
	procEnumWindows.Call(enumWindowsProcCB, 0)
	return append([]syscall.Handle(nil), enumFoundWindows...)
}

func browserAppWindowExeNames() map[string]bool {
	names := map[string]bool{}
	if activeAppBrowserExe != "" {
		names[strings.ToLower(activeAppBrowserExe)] = true
	}
	if browserState != nil {
		if browser, ok := findInstalledBrowser(browserState.GetBrowserChoice()); ok && browser.AppMode {
			names[strings.ToLower(browser.ExeName)] = true
		}
	}
	return names
}

func showAppWindow() bool {
	windows := appWindows()
	if len(windows) == 0 {
		return false
	}
	h := windows[0]
	procShowWindow.Call(uintptr(h), swRestore)
	procSetForegroundWindow.Call(uintptr(h))
	for _, extra := range windows[1:] {
		procPostMessageW.Call(uintptr(extra), wmClose, 0, 0)
	}
	return true
}

func showOrOpenAppWindow(url string) {
	if showAppWindow() {
		return
	}
	openBrowser(url)
}

// closeAppWindows 关闭本应用的浏览器 app 模式界面窗口（"完全退出"时调用）。
// 用 PostMessage(WM_CLOSE) 只关闭匹配到的那个浏览器窗口，既不结束浏览器进程、
// 也不影响用户的其它浏览器窗口或标签页。PostMessage 是投递到浏览器进程的消息队列，
// 即便本进程随后 os.Exit，浏览器也会照常处理该消息关闭窗口。
func closeAppWindows() {
	for _, h := range appWindows() {
		procPostMessageW.Call(uintptr(h), wmClose, 0, 0)
	}
}

// trayOnUIClose 由 /api/ui-closed 在界面窗口关闭时调用，按 CloseAction 设置决定行为：
//   - exit：直接完全退出
//   - tray：最小化到托盘（首次给气泡提示）
//   - ask（默认）：弹出询问对话框
//
// UI 操作统一通过 PostMessage 投递回托盘线程执行，避免跨线程操作窗口/托盘。
func trayOnUIClose() {
	if trayHwnd == 0 || traySt == nil {
		return
	}
	switch traySt.GetCloseAction() {
	case "exit":
		trayExit()
	case "tray":
		closeTipOnce.Do(func() {
			procPostMessageW.Call(uintptr(trayHwnd), wmShowCloseTip, 0, 0)
		})
	default: // ask
		procPostMessageW.Call(uintptr(trayHwnd), wmAskClose, 0, 0)
	}
}

// runTray 在主线程创建隐藏窗口与托盘图标，并进入消息循环（阻塞直到「完全退出」）。
// 关闭界面窗口不会退出本进程，程序最小化到托盘继续提供服务。
func runTray(url string, st *AppState) {
	trayURL = url
	traySt = st
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
		uintptr(unsafe.Pointer(utf16Ptr(trayAppName()))),
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
	copyUTF16(trayNID.szTip[:], trayAppName())
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

// ---------- 自定义对话框（关闭询问 / 设置）----------
// 纯 Win32 自绘对话框：标准 MessageBox 不支持"记住选择"勾选框，
// 而 TaskDialog 需要 ComCtl32 v6 清单依赖，故这里手动创建窗口+控件实现，保持零依赖且可在调试构建中正常运行。

var (
	dlgDone         bool           // 对话框是否已得到结果
	dlgClickedID    uint32         // 被点击的按钮控件 ID（0 表示直接关闭）
	dlgCheckHwnd    syscall.Handle // "记住选择"勾选框句柄（无则为 0）
	dlgClassOnce    sync.Once
	dlgClassNamePtr = utf16Ptr("NodePortProxyDlg")
	dlgProcCB       = syscall.NewCallback(dlgProc)
)

// dlgProc 是自定义对话框的消息处理函数
func dlgProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmCommand:
		id := uint32(wparam) & 0xFFFF
		if id == idRemember {
			return 0 // 勾选框：自动切换状态即可，不结束对话框
		}
		if id == idBrowserCombo {
			// 浏览器下拉框：选中即保存，不结束对话框（lParam 为下拉框句柄）
			if uint32(wparam)>>16 == cbnSelChange {
				applyBrowserComboChoice(lparam)
			}
			return 0
		}
		// 仅置标志，窗口销毁推迟到调用方读取完勾选框状态后再做，
		// 否则 DestroyWindow 会连带销毁勾选框，导致读不到"记住"状态
		dlgClickedID = id
		dlgDone = true
		return 0
	case wmClose:
		// 直接关闭对话框（点对话框自身的 X 或 Esc）：视为"最小化到托盘"，不记忆
		dlgClickedID = 0
		dlgDone = true
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

// registerDlgClassOnce 注册对话框窗口类（仅一次）
func registerDlgClassOnce(hInstance uintptr) {
	dlgClassOnce.Do(func() {
		var wc wndClassExW
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		wc.lpfnWndProc = dlgProcCB
		wc.hInstance = syscall.Handle(hInstance)
		wc.hbrBackground = syscall.Handle(6) // COLOR_WINDOW+1，与系统对话框背景一致
		wc.hIcon = loadAppIcon(hInstance)
		wc.lpszClassName = dlgClassNamePtr
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	})
}

// createDlg 创建一个屏幕居中、置顶的对话框窗口（不在任务栏显示）
func createDlg(hInstance uintptr, title string, w, h int) uintptr {
	registerDlgClassOnce(hInstance)
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	x := (int(sw) - w) / 2
	y := (int(sh) - h) / 2
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExDlgModalFrame|wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(dlgClassNamePtr)),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		wsCaption|wsSysMenu,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		0, 0, hInstance, 0,
	)
	return hwnd
}

// dlgAddControl 在对话框上创建一个子控件（STATIC 文本 / BUTTON 按钮或勾选框），并设置系统默认字体
func dlgAddControl(parent, hInstance uintptr, class, text string, style uintptr, id uintptr, x, y, w, h int) syscall.Handle {
	hctl, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, hInstance, 0,
	)
	font, _, _ := procGetStockObject.Call(defaultGuiFont)
	procSendMessageW.Call(hctl, wmSetFont, font, 1)
	return syscall.Handle(hctl)
}

// runDlgModal 显示对话框并进入嵌套消息循环，直到对话框得到结果
func runDlgModal(hwnd uintptr) {
	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	var msg msgStruct
	for !dlgDone {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// dlgGetRemember 读取"记住选择"勾选框是否勾选
func dlgGetRemember() bool {
	if dlgCheckHwnd == 0 {
		return false
	}
	r, _, _ := procSendMessageW.Call(uintptr(dlgCheckHwnd), bmGetCheck, 0, 0)
	return r == bstChecked
}

// showCloseDialog 弹出"关闭方式"询问对话框，返回选择（"exit"/"tray"）与是否勾选记住
func showCloseDialog() (string, bool) {
	dlgDone = false
	dlgClickedID = 0
	dlgCheckHwnd = 0
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hwnd := createDlg(hInstance, trayText("退出 - 多节点端口代理", "Exit - MultiPortProxy"), 430, 252)
	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText(
			"关闭窗口后，你希望如何处理本程序？\r\n\r\n- 完全退出：停止代理服务并结束程序\r\n- 最小化到托盘：保留后台代理服务，可随时从托盘恢复",
			"What should happen when the window is closed?\r\n\r\n- Exit: stop proxy service and quit the app\r\n- Minimize to tray: keep proxy service running in the background",
		),
		wsChild|wsVisible|ssLeft, 0, 22, 18, 384, 104)
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("完全退出", "Exit"),
		wsChild|wsVisible|wsTabStop|bsPushButton, idExitBtn, 44, 130, 150, 36)
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("最小化到托盘", "Minimize to tray"),
		wsChild|wsVisible|wsTabStop|bsDefPushButton, idTrayBtn, 216, 130, 170, 36)
	dlgCheckHwnd = dlgAddControl(hwnd, hInstance, "BUTTON", trayText("记住我的选择，不再询问", "Remember my choice"),
		wsChild|wsVisible|wsTabStop|bsAutoCheckBox, idRemember, 44, 178, 340, 26)
	runDlgModal(hwnd)
	remember := dlgGetRemember() // 必须在销毁窗口前读取勾选框状态
	procDestroyWindow.Call(hwnd)
	switch dlgClickedID {
	case idExitBtn:
		return "exit", remember
	case idTrayBtn:
		return "tray", remember
	default:
		return "tray", false // 直接关闭对话框 = 最小化到托盘，且不记忆
	}
}

// settingsBrowserChoices 把"界面浏览器"下拉框的每个表项索引映射到对应的 BrowserChoice 值。
// 索引 0 固定是"跟随系统默认"，其后依次是各个支持应用窗口的浏览器。
var settingsBrowserChoices []string

// comboAddString 向下拉框追加一个表项
func comboAddString(combo syscall.Handle, text string) {
	procSendMessageW.Call(uintptr(combo), cbAddString, 0, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

// applyBrowserComboChoice 在下拉框选择变化时读取当前选项并立即保存
func applyBrowserComboChoice(comboHwnd uintptr) {
	if traySt == nil {
		return
	}
	idx, _, _ := procSendMessageW.Call(comboHwnd, cbGetCurSel, 0, 0)
	i := int(idx)
	if i >= 0 && i < len(settingsBrowserChoices) {
		traySt.SetBrowserChoice(settingsBrowserChoices[i])
	}
}

// showSettingsDialog 弹出"设置"对话框，修改关闭按钮(X)、界面语言和加载浏览器。
func showSettingsDialog() {
	dlgDone = false
	dlgClickedID = 0
	dlgCheckHwnd = 0
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	closeAction := traySt.GetCloseAction()
	language := trayLanguage()
	browserChoice := traySt.GetBrowserChoice()
	installedBrowsers := detectInstalledBrowsers()
	browsers := appModeBrowsers(installedBrowsers)
	systemBrowser, systemBrowserOK := systemDefaultAppModeBrowser(installedBrowsers)
	// 下拉框当前选中索引：0=跟随系统默认，其后依次是各浏览器；
	// 若保存的浏览器已不可用则回退到"跟随系统默认"。
	browserSel := 0
	normChoice := normalizeBrowserChoice(browserChoice)
	if normChoice != browserSystem {
		for i, browser := range browsers {
			if browser.ID == normChoice {
				browserSel = i + 1
				break
			}
		}
	}
	systemLabel := trayText("跟随系统默认", "Use system default")
	if systemBrowserOK {
		systemLabel = trayText("跟随系统默认（", "System default (") + systemBrowser.Name + trayText("）", ")")
	}
	curText := map[string]string{
		"ask":  trayText("每次都询问", "Ask every time"),
		"exit": trayText("直接完全退出", "Exit directly"),
		"tray": trayText("最小化到托盘", "Minimize to tray"),
	}[closeAction]
	curLang := map[string]string{"zh-CN": "简体中文", "en": "English"}[language]
	mark := func(on bool) string {
		if on {
			return "✓ "
		}
		return ""
	}

	hwnd := createDlg(hInstance, trayText("设置 - MultiPortProxy", "Settings - MultiPortProxy"), 620, 512)
	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText("应用偏好", "Application preferences")+"\r\n"+trayText("从托盘快速调整窗口关闭行为、界面浏览器和界面语言。", "Quickly adjust close behavior, browser, and interface language from the tray."),
		wsChild|wsVisible|ssLeft, 0, 24, 18, 540, 48)

	// 关闭按钮行为（少量固定选项，保留按钮）
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("关闭按钮行为", "Close button behavior"),
		wsChild|wsVisible|bsGroupBox, 0, 20, 76, 570, 110)
	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText("当前：", "Current: ")+curText,
		wsChild|wsVisible|ssLeft, 0, 42, 102, 500, 24)
	dlgAddControl(hwnd, hInstance, "BUTTON", mark(closeAction == "ask")+trayText("每次询问", "Ask"),
		wsChild|wsVisible|wsTabStop|bsPushButton, idSetAsk, 42, 130, 150, 34)
	dlgAddControl(hwnd, hInstance, "BUTTON", mark(closeAction == "exit")+trayText("直接退出", "Exit"),
		wsChild|wsVisible|wsTabStop|bsPushButton, idSetExit, 218, 130, 150, 34)
	dlgAddControl(hwnd, hInstance, "BUTTON", mark(closeAction == "tray")+trayText("最小化托盘", "Tray"),
		wsChild|wsVisible|wsTabStop|bsPushButton, idSetTray, 394, 130, 150, 34)

	// 界面浏览器（可变长列表，用下拉框，避免平铺）
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("界面浏览器", "Browser"),
		wsChild|wsVisible|bsGroupBox, 0, 20, 200, 570, 96)
	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText("打开界面使用的浏览器：", "Browser used to open the interface:"),
		wsChild|wsVisible|ssLeft, 0, 42, 228, 510, 22)
	combo := dlgAddControl(hwnd, hInstance, "COMBOBOX", "",
		wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropDownList, idBrowserCombo, 42, 254, 320, 260)
	settingsBrowserChoices = []string{browserSystem}
	comboAddString(combo, systemLabel)
	for _, browser := range browsers {
		settingsBrowserChoices = append(settingsBrowserChoices, browser.ID)
		comboAddString(combo, browser.Name)
	}
	procSendMessageW.Call(uintptr(combo), cbSetCurSel, uintptr(browserSel), 0)

	// 界面语言（两个固定选项，保留按钮）
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("界面语言", "Interface language"),
		wsChild|wsVisible|bsGroupBox, 0, 20, 312, 570, 92)
	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText("当前：", "Current: ")+curLang,
		wsChild|wsVisible|ssLeft, 0, 42, 338, 500, 24)
	dlgAddControl(hwnd, hInstance, "BUTTON", mark(language == "zh-CN")+"简体中文",
		wsChild|wsVisible|wsTabStop|bsPushButton, idLangZh, 150, 364, 150, 34)
	dlgAddControl(hwnd, hInstance, "BUTTON", mark(language == "en")+"English",
		wsChild|wsVisible|wsTabStop|bsPushButton, idLangEn, 322, 364, 150, 34)

	dlgAddControl(hwnd, hInstance, "STATIC",
		trayText("选择会立即保存。下拉框只列出支持独立应用窗口的浏览器。", "Changes are saved immediately. The list only shows browsers that support app windows."),
		wsChild|wsVisible|ssLeft, 0, 24, 422, 430, 36)
	dlgAddControl(hwnd, hInstance, "BUTTON", trayText("完成", "Done"),
		wsChild|wsVisible|wsTabStop|bsDefPushButton, idSetClose, 488, 420, 100, 36)
	runDlgModal(hwnd)
	clicked := dlgClickedID
	procDestroyWindow.Call(hwnd)
	switch clicked {
	case idSetAsk:
		traySt.SetCloseAction("ask")
	case idSetExit:
		traySt.SetCloseAction("exit")
	case idSetTray:
		traySt.SetCloseAction("tray")
	case idLangZh:
		traySt.SetLanguage("zh-CN")
		updateTrayTip()
	case idLangEn:
		traySt.SetLanguage("en")
		updateTrayTip()
	case idSetClose:
		return
	}
	// 注：界面浏览器在下拉框 CBN_SELCHANGE 时即时保存（见 applyBrowserComboChoice），此处无需处理。
}
