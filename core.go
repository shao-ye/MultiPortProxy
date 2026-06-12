package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// CoreManager 负责生成内核配置、启动/停止内核进程、收集运行日志。
// 应用采用双内核架构：
//   - mihomo：承载常规协议节点，并提供所有对外的 mixed(http+socks) 监听端口
//   - xray：仅在存在 xhttp 节点时启动，在内部端口提供 socks 入站，由 mihomo 桥接
type CoreManager struct {
	mu        sync.Mutex
	cmdMihomo *exec.Cmd // mihomo 进程，nil 表示未运行
	cmdXray   *exec.Cmd // xray 进程，nil 表示未运行（没有 xhttp 节点时不启动）
	logs      []string  // 日志环形缓冲（两个内核共用，带前缀区分）
	running   bool
}

const maxLogLines = 500

var core = &CoreManager{}

// internalPort 计算 xray 节点的内部桥接端口：对外端口 + 10000
func internalPort(port int) int {
	return port + 10000
}

// appendLog 向日志缓冲追加一行，超出上限时丢弃最旧的
func (c *CoreManager) appendLog(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, time.Now().Format("15:04:05 ")+line)
	if len(c.logs) > maxLogLines {
		c.logs = c.logs[len(c.logs)-maxLogLines:]
	}
}

// Logs 返回日志缓冲的副本
func (c *CoreManager) Logs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.logs))
	copy(out, c.logs)
	return out
}

// IsRunning 返回服务是否在运行（以 mihomo 进程为准）
func (c *CoreManager) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// buildMihomoConfig 根据节点列表生成 mihomo 配置。
// 每个节点对应一个 mixed 类型的 listener（同端口同时支持 socks5 和 http 代理），
// listener 通过 proxy 字段直接绑定到对应节点出站。
// xray 节点在 mihomo 侧表现为一个指向内部桥接端口的 socks5 出站。
// 注意：JSON 是 YAML 的合法子集，用标准库 json 序列化后直接写成 .yaml 即可，无需第三方 YAML 库。
func buildMihomoConfig(st *AppState) ([]byte, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	listen := "127.0.0.1"
	if st.Settings.AllowLan {
		listen = "0.0.0.0"
	}
	var proxies []map[string]any
	var listeners []map[string]any
	usedNames := map[string]bool{}
	for i, n := range st.Nodes {
		if n.ParseErr != "" || n.Port <= 0 {
			continue
		}
		// 组装该节点的 mihomo 出站配置
		proxy := map[string]any{}
		if n.Core == "xray" {
			// xhttp 节点：mihomo 通过 socks5 桥接到 xray 的内部入站端口
			proxy["type"] = "socks5"
			proxy["server"] = "127.0.0.1"
			proxy["port"] = internalPort(n.Port)
			proxy["udp"] = true
			proxy["name"] = n.Name
		} else {
			if n.Proxy == nil {
				continue
			}
			for k, v := range n.Proxy {
				proxy[k] = v
			}
		}
		// mihomo 要求代理名称唯一，重名时追加序号
		name := fmt.Sprintf("%v", proxy["name"])
		for usedNames[name] {
			name = fmt.Sprintf("%s #%d", proxy["name"], i+1)
		}
		usedNames[name] = true
		proxy["name"] = name

		proxies = append(proxies, proxy)
		listeners = append(listeners, map[string]any{
			"name":   fmt.Sprintf("listener-%d", n.Port),
			"type":   "mixed",
			"port":   n.Port,
			"listen": listen,
			"proxy":  name,
		})
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("没有可用的节点（请先导入节点并分配端口）")
	}
	cfg := map[string]any{
		"mode":      "global",
		"log-level": "info",
		"ipv6":      true,
		"proxies":   proxies,
		"listeners": listeners,
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// buildXrayConfig 为所有 xhttp 节点生成 xray 配置；没有 xhttp 节点时返回 (nil, false)。
// 结构：每个节点一个内部 socks 入站（仅监听 127.0.0.1），
// 通过 routing 的 inboundTag 规则一对一绑定到对应的 outbound。
func buildXrayConfig(st *AppState) ([]byte, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	var inbounds []map[string]any
	var outbounds []map[string]any
	var rules []map[string]any
	for _, n := range st.Nodes {
		if n.ParseErr != "" || n.Core != "xray" || n.Port <= 0 || n.Xray == nil {
			continue
		}
		inTag := fmt.Sprintf("in-%d", n.Port)
		outTag := fmt.Sprintf("out-%d", n.Port)
		inbounds = append(inbounds, map[string]any{
			"tag":      inTag,
			"listen":   "127.0.0.1",
			"port":     internalPort(n.Port),
			"protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": true},
		})
		// 复制 outbound 并打上 tag
		outbound := map[string]any{}
		for k, v := range n.Xray {
			outbound[k] = v
		}
		outbound["tag"] = outTag
		outbounds = append(outbounds, outbound)
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{inTag},
			"outboundTag": outTag,
		})
	}
	if len(inbounds) == 0 {
		return nil, false, nil
	}
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   map[string]any{"rules": rules},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	return data, true, err
}

// spawnCore 启动一个内核进程并把它的输出（带前缀）接入日志缓冲，
// 返回进程对象；onExit 在进程退出时回调
func (c *CoreManager) spawnCore(prefix string, exePath string, args []string, onExit func()) (*exec.Cmd, error) {
	cmd := exec.Command(exePath, args...)
	hideWindow(cmd) // 隐藏内核的控制台窗口
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s 启动失败: %v", prefix, err)
	}
	c.appendLog(fmt.Sprintf("[应用] %s 内核已启动 (PID %d)", prefix, cmd.Process.Pid))
	// 后台协程持续读取内核输出写入日志缓冲
	go func(out io.ReadCloser) {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		for scanner.Scan() {
			c.appendLog(prefix + " " + scanner.Text())
		}
		_ = cmd.Wait()
		if onExit != nil {
			onExit()
		}
	}(stdout)
	return cmd, nil
}

// Start 生成配置并启动内核进程；若已在运行则先停止再启动。
// 启动顺序：先 xray（如有 xhttp 节点）后 mihomo，保证桥接目标先就绪。
func (c *CoreManager) Start(st *AppState) error {
	if c.IsRunning() {
		c.Stop()
	}
	st.mu.Lock()
	corePath := st.Settings.CorePath
	xrayPath := st.Settings.XrayPath
	st.mu.Unlock()
	if corePath == "" {
		return fmt.Errorf("未找到 mihomo 内核，请在设置中指定 mihomo.exe 路径")
	}
	if _, err := os.Stat(corePath); err != nil {
		return fmt.Errorf("mihomo 内核路径无效: %s", corePath)
	}
	if err := st.validatePorts(); err != nil {
		return err
	}

	mihomoCfg, err := buildMihomoConfig(st)
	if err != nil {
		return err
	}
	xrayCfg, hasXray, err := buildXrayConfig(st)
	if err != nil {
		return err
	}
	if hasXray {
		if xrayPath == "" {
			return fmt.Errorf("存在 xhttp 节点但未找到 xray 内核，请在设置中指定 xray.exe 路径")
		}
		if _, err := os.Stat(xrayPath); err != nil {
			return fmt.Errorf("xray 内核路径无效: %s", xrayPath)
		}
	}

	// 配置和缓存写到应用目录下的 runtime 子目录
	runtimeDir := filepath.Join(exeDir(), "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	mihomoCfgPath := filepath.Join(runtimeDir, "config.yaml")
	if err := os.WriteFile(mihomoCfgPath, mihomoCfg, 0o644); err != nil {
		return err
	}

	c.mu.Lock()
	c.logs = nil
	c.mu.Unlock()

	// 先启动 xray（如需要）
	if hasXray {
		xrayCfgPath := filepath.Join(runtimeDir, "xray.json")
		if err := os.WriteFile(xrayCfgPath, xrayCfg, 0o644); err != nil {
			return err
		}
		cmdXray, err := c.spawnCore("[xray]", xrayPath, []string{"run", "-c", xrayCfgPath}, func() {
			c.appendLog("[应用] xray 内核进程已退出")
		})
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.cmdXray = cmdXray
		c.mu.Unlock()
	}

	// 再启动 mihomo
	cmdMihomo, err := c.spawnCore("[mihomo]", corePath, []string{"-d", runtimeDir, "-f", mihomoCfgPath}, func() {
		c.mu.Lock()
		wasRunning := c.running
		c.running = false
		c.cmdMihomo = nil
		c.mu.Unlock()
		if wasRunning {
			c.appendLog("[应用] mihomo 内核进程已退出")
		}
	})
	if err != nil {
		c.Stop() // mihomo 启动失败时把已启动的 xray 也停掉
		return err
	}
	c.mu.Lock()
	c.cmdMihomo = cmdMihomo
	c.running = true
	c.mu.Unlock()

	// 短暂等待，确认内核没有因配置错误立即退出
	time.Sleep(800 * time.Millisecond)
	if !c.IsRunning() {
		logs := c.Logs()
		tail := ""
		if len(logs) > 0 {
			tail = logs[len(logs)-1]
		}
		return fmt.Errorf("内核启动后立即退出，请检查日志: %s", tail)
	}
	return nil
}

// Stop 停止所有内核进程
func (c *CoreManager) Stop() {
	c.mu.Lock()
	cmdMihomo := c.cmdMihomo
	cmdXray := c.cmdXray
	c.running = false
	c.cmdMihomo = nil
	c.cmdXray = nil
	c.mu.Unlock()
	if cmdMihomo != nil && cmdMihomo.Process != nil {
		_ = cmdMihomo.Process.Kill()
	}
	if cmdXray != nil && cmdXray.Process != nil {
		_ = cmdXray.Process.Kill()
	}
	if cmdMihomo != nil || cmdXray != nil {
		c.appendLog("[应用] 内核已停止")
	}
}

// TestNode 通过指定本地端口发起一次 HTTP 请求测试节点连通性，返回延迟毫秒数
func TestNode(port int) (int, error) {
	proxyURL := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   8 * time.Second,
	}
	start := time.Now()
	resp, err := client.Get("http://www.gstatic.com/generate_204")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return int(time.Since(start).Milliseconds()), nil
}
