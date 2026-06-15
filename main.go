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
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// uiPort 是网页界面的固定端口；被占用时会向后顺延
const uiPort = 23456

// main 程序入口：加载状态 → 启动本地 HTTP 服务 → 打开浏览器窗口
func main() {
	st := LoadState()

	// 单实例检测：如果端口上已经跑着本应用，直接打开浏览器并退出
	port := uiPort
	for i := 0; i < 10; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			break
		}
		if isSelfRunning(port) {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
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

	// 进入系统托盘消息循环（阻塞）。关闭界面窗口后程序最小化到托盘继续运行，
	// 仅当通过托盘菜单「完全退出」时才真正结束进程。
	runTray(url)
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

// openBrowser 优先用 Edge 的应用模式打开（独立窗口、无地址栏，更像桌面应用），
// 找不到 Edge 时退回系统默认浏览器
func openBrowser(url string) {
	edges := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, edge := range edges {
		if _, err := os.Stat(edge); err == nil {
			cmd := exec.Command(edge, "--app="+url)
			if cmd.Start() == nil {
				return
			}
		}
	}
	_ = exec.Command("cmd", "/c", "start", "", url).Start()
}
