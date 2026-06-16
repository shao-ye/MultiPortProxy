package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// uiPort 是网页界面的固定端口；被占用时会向后顺延
const uiPort = 23456

var (
	browserOpenMu       sync.Mutex
	lastBrowserOpenAt   time.Time
	browserState        *AppState
	activeAppBrowserExe string
	// focusPrefsPending 是一次性标记：托盘点"设置"时置位，网页轮询 /api/state 读到后滚动到
	// "界面偏好"区块并清除。这样无论界面窗口是已打开还是新开，点"设置"都能定位到偏好设置。
	focusPrefsPending atomic.Bool
)

// main 程序入口：加载状态 → 启动本地 HTTP 服务 → 打开浏览器窗口
func main() {
	st := LoadState()
	browserState = st

	if release, alreadyRunning := acquireSingleInstance(); alreadyRunning {
		for i := 0; i < 20; i++ {
			if openExistingInstance() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return
	} else {
		defer release()
	}

	// 单实例检测：如果端口上已经跑着本应用，直接打开浏览器并退出
	port := uiPort
	for i := 0; i < 10; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			break
		}
		if isSelfRunning(port) {
			showOrOpenAppWindow(fmt.Sprintf("http://127.0.0.1:%d", port))
			return
		}
		port++ // 端口被其它程序占用，顺延
	}

	mux := http.NewServeMux()
	registerAPI(mux, st)
	// 静态页面：embed 的 web 目录作为站点根目录
	webRoot, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	url := "http://" + addr

	// 接收退出信号时先停掉内核进程，避免 mihomo 残留
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		core.Stop()
		os.Exit(0)
	}()

	// 延迟打开浏览器，等服务先就绪
	go func() {
		time.Sleep(300 * time.Millisecond)
		openBrowser(url)
	}()

	// 上次退出时服务在运行，则自动恢复代理服务
	if st.AutoStart {
		go func() {
			if err := core.Start(st); err != nil {
				core.appendLog("[应用] 自动恢复服务失败: " + err.Error())
			}
		}()
	}

	fmt.Println("多节点端口代理已启动:", url)
	// HTTP 服务放到后台 goroutine，主线程留给系统托盘的消息循环
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			fmt.Println("服务启动失败:", err)
			os.Exit(1)
		}
	}()

	// 进入系统托盘消息循环（阻塞）。关闭界面窗口后按设置决定退出/最小化到托盘，
	// 仅当真正退出时才结束进程。
	runTray(url, st)
}

// openExistingInstance 尝试唤起已经运行的实例；用于第二次双击 exe 时不再新建后端。
func openExistingInstance() bool {
	for i := 0; i < 10; i++ {
		port := uiPort + i
		if isSelfRunning(port) {
			showOrOpenAppWindow(fmt.Sprintf("http://127.0.0.1:%d", port))
			return true
		}
	}
	return showAppWindow()
}

// isSelfRunning 检查指定端口上是否已经运行着本应用（通过 /api/ping 的标识判断）
func isSelfRunning(port int) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode == 200 && string(buf[:n]) != "" &&
		string(buf[:n]) != "{}" && containsStr(string(buf[:n]), "NodePortProxy")
}

// containsStr 简单的子串判断辅助函数
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// openBrowser 默认用 Chromium 应用模式打开（独立窗口、无地址栏，更像桌面应用）；
// 用户在托盘设置中选择系统默认浏览器时，会先解析 Windows 默认浏览器，再尽量用独立窗口打开。
// 不指定 --user-data-dir：沿用用户默认浏览器配置，避免新配置目录触发"同步浏览数据"等首启登录提示；
// "完全退出"时由 closeAppWindows 按窗口标题精确关闭本应用窗口（见 tray_windows.go）。
func openBrowser(url string) {
	browserOpenMu.Lock()
	defer browserOpenMu.Unlock()

	if showAppWindow() {
		return
	}
	if time.Since(lastBrowserOpenAt) < 2*time.Second {
		return
	}
	lastBrowserOpenAt = time.Now()

	choice := browserSystem
	if browserState != nil {
		choice = browserState.GetBrowserChoice()
	}
	if browser, ok := findInstalledBrowser(choice); ok {
		cmd := exec.Command(browser.Path, browserArgs(browser, url)...)
		if cmd.Start() == nil {
			if browser.AppMode {
				activeAppBrowserExe = browser.ExeName
			}
			return
		}
	}
	_ = exec.Command("cmd", "/c", "start", "", url).Start()
}
