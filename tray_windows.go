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
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
)

// ---------- 常量 ----------
const (
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1 // 托盘图标的鼠标事件回调消息
	wmShowCloseTip = wmApp + 2 // 自定义消息：在托盘线程弹出"已最小化"气泡
	wmAskClose     = wmApp + 3 // 自定义消息：在托盘线程弹出"关闭方式"询问对话框

	wmClose          = 0x0010
	wmCommand        = 0x0111
	wmSetFont        = 0x0030
	wmCtlColorStatic = 0x0138 // STATIC 控件绘制前询问父窗口要画刷/文字色

	transparentBkMode = 1 // SetBkMode：TRANSPARENT，文字背景不填充
	whiteBrush        = 0  // GetStockObject：WHITE_BRUSH
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

	defaultGuiFont = 17

	// Segoe UI 字体字重（CreateFontW 的 cWeight 参数）
	fwNormal = 400 // 正文常规
	// CreateFontW 其它固定参数
	defaultCharset   = 1 // DEFAULT_CHARSET：让系统按字符自动做 CJK 字体回退
	clearTypeQuality = 5 // CLEARTYPE_QUALITY：开启抗锯齿，文字更顺滑

	smCxScreen = 0
	smCyScreen     = 1
	swShow         = 5
	swRestore      = 9

	// 对话框控件 ID
	idExitBtn  = 101 // 关闭询问：完全退出
	idTrayBtn  = 102 // 关闭询问：最小化到托盘
	idRemember = 103 // 关闭询问：记住选择 勾选框

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
	menuSettings = 3 // 托盘右键菜单：设置（打开独立的网页设置窗口）
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

// 界面窗口标题集合：主界面与独立设置窗口各一组（中/英），用于精确匹配本应用窗口。
// 设置窗口标题须与 web/settings.html 的 <title>/docTitle 完全一致。
var (
	mainWindowTitles     = []string{"多节点端口代理", "MultiPortProxy"}
	settingsWindowTitles = []string{"设置 - 多节点端口代理", "Settings - MultiPortProxy"}
)

// allWindowTitles 返回主界面 + 设置窗口的全部标题（"完全退出"时据此关闭所有界面窗口）。
func allWindowTitles() []string {
	titles := append([]string{}, mainWindowTitles...)
	return append(titles, settingsWindowTitles...)
}

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

// windowTitleMatches 判断窗口标题是否精确等于 enumTitles（本轮枚举的目标标题集合）中的某一项。
// 必须用"精确相等"而非"包含"：app 模式窗口标题就是网页 <title>（如"多节点端口代理"/"MultiPortProxy"），
// 而官网标题"MultiPortProxy · 多节点端口代理"同时包含这两者——用包含匹配会误把用户开着官网的普通浏览器窗口
// 当成应用窗口，导致还原(缩小)该窗口并误判"应用已打开"。
func windowTitleMatches(title string) bool {
	for _, want := range enumTitles {
		if title == want {
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
		// 设置是独立的网页设置窗口（与主界面分开），已开则聚焦，否则新开
		showOrOpenSettingsWindow(trayURL + "/settings.html")
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

// enumFoundWindows 收集 EnumWindows 回调中匹配到的界面窗口句柄；enumTitles 是本轮匹配的目标标题集合
var (
	enumFoundWindows    []syscall.Handle
	enumBrowserExeNames map[string]bool
	enumTitles          []string
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
// 标题须精确相等以排除"开着官网的普通浏览器窗口"（见 windowTitleMatches）。
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
	if !windowTitleMatches(titleText) || !strings.HasPrefix(classText, chromiumWindowClass) {
		return 1
	}
	if !enumBrowserExeNames[windowProcessBaseName(hwnd)] {
		return 1
	}
	enumFoundWindows = append(enumFoundWindows, syscall.Handle(hwnd))
	return 1
}

var enumWindowsProcCB = syscall.NewCallback(enumWindowsProc)

// appWindowsMatching 枚举标题命中 titles 的本应用界面窗口句柄
func appWindowsMatching(titles []string) []syscall.Handle {
	enumFoundWindows = nil
	enumTitles = titles
	enumBrowserExeNames = browserAppWindowExeNames()
	if len(enumBrowserExeNames) == 0 {
		return nil
	}
	procEnumWindows.Call(enumWindowsProcCB, 0)
	return append([]syscall.Handle(nil), enumFoundWindows...)
}

// appWindows 返回全部界面窗口（主界面 + 设置窗口），供"完全退出"统一关闭
func appWindows() []syscall.Handle {
	return appWindowsMatching(allWindowTitles())
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

// focusAppWindow 把标题命中 titles 的窗口还原并置前；存在多个时只留一个、关掉多余的。返回是否找到窗口。
func focusAppWindow(titles []string) bool {
	windows := appWindowsMatching(titles)
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

// showAppWindow 聚焦主界面窗口（供单实例唤起复用）
func showAppWindow() bool {
	return focusAppWindow(mainWindowTitles)
}

// showOrOpenAppWindow 显示主界面窗口：已开则聚焦，否则新开
func showOrOpenAppWindow(url string) {
	if focusAppWindow(mainWindowTitles) {
		return
	}
	openBrowser(mainWindowTitles, url)
}

// showOrOpenSettingsWindow 显示独立设置窗口：已开则聚焦，否则以较小尺寸新开
func showOrOpenSettingsWindow(url string) {
	if focusAppWindow(settingsWindowTitles) {
		return
	}
	openBrowser(settingsWindowTitles, url, "--window-size=520,560")
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
	case wmCtlColorStatic:
		// 让 STATIC 文本背景透明并返回白色画刷，避免在白色窗口上出现灰色底条
		procSetBkMode.Call(wparam, transparentBkMode)
		br, _, _ := procGetStockObject.Call(whiteBrush)
		return br
	case wmCommand:
		id := uint32(wparam) & 0xFFFF
		if id == idRemember {
			return 0 // 勾选框：自动切换状态即可，不结束对话框
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

// ---------- 现代化字体 ----------
// 系统自带的 DEFAULT_GUI_FONT 是老式点阵字体（观感陈旧），这里统一改用 Windows 现代界面字体
// Segoe UI（中文由系统字体回退渲染），并按字号/字重缓存复用。

var (
	uiFontOnce sync.Once
	uiFont     uintptr // 正文：Segoe UI 9pt 常规
)

// makeUIFont 创建一个指定字号(pt)与字重的 Segoe UI 字体。
// 返回的 HFONT 为进程内长期复用的 GDI 资源，不主动释放（进程退出时由系统回收）。
func makeUIFont(pt, weight int) uintptr {
	// 96 DPI 下把磅值换算为逻辑高度；负值表示按字符高度（而非单元格高度）取字号
	height := -(pt * 96) / 72
	name := utf16Ptr("Segoe UI")
	h, _, _ := procCreateFontW.Call(
		uintptr(int32(height)), 0, 0, 0, uintptr(weight),
		0, 0, 0, // 斜体 / 下划线 / 删除线
		defaultCharset, 0, 0, clearTypeQuality, 0,
		uintptr(unsafe.Pointer(name)),
	)
	return h
}

// ensureUIFonts 惰性初始化界面字体（仅一次）
func ensureUIFonts() {
	uiFontOnce.Do(func() {
		uiFont = makeUIFont(9, fwNormal)
	})
}

// dlgAddControl 在对话框上创建一个子控件（STATIC 文本 / BUTTON 按钮或勾选框），并应用现代界面字体
func dlgAddControl(parent, hInstance uintptr, class, text string, style uintptr, id uintptr, x, y, w, h int) syscall.Handle {
	ensureUIFonts()
	hctl, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, hInstance, 0,
	)
	procSendMessageW.Call(hctl, wmSetFont, uiFont, 1)
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

// 注：旧的原生"设置"对话框（关闭行为/界面浏览器/界面语言三项下拉框）已迁移到网页界面，
// 由 /api/preferences 读写。托盘"设置"菜单改为打开界面并定位到"界面偏好"区块（见 showTrayMenu）。
