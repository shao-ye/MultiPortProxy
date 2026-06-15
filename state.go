package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Node 表示一个代理节点：保存原始分享链接、解析后的 mihomo 代理配置、以及分配的本地端口
type Node struct {
	ID       string `json:"id"`       // 节点唯一标识（导入时生成）
	Name     string `json:"name"`     // 节点显示名称（来自分享链接的备注）
	Link     string `json:"link"`     // 原始分享链接（vless:// 等），持久化时只保存这个
	Port     int    `json:"port"`     // 分配的本地监听端口，0 表示未分配
	Protocol string `json:"protocol"` // 协议类型：vless/vmess/trojan/ss/hysteria2/vless-xhttp

	// 以下字段运行时计算，不持久化
	Server   string         `json:"server"`   // 远程服务器地址（用于界面展示）
	Remote   int            `json:"remote"`   // 远程端口（用于界面展示）
	ParseErr string         `json:"parseErr"` // 链接解析失败时的错误信息
	Core     string         `json:"core"`     // 承载内核："mihomo"（默认）或 "xray"（xhttp 节点）
	Proxy    map[string]any `json:"-"`        // 解析后的 mihomo proxy 配置（xray 节点为桥接用的 socks5 配置）
	Xray     map[string]any `json:"-"`        // xray 节点的 outbound 配置（仅 Core=="xray" 时有值）
	DelayMs  int            `json:"delayMs"`  // 最近一次测试延迟（毫秒），0=未测，-1=失败
}

// Settings 保存应用级配置
type Settings struct {
	CorePath string `json:"corePath"` // mihomo.exe 内核路径
	XrayPath string `json:"xrayPath"` // xray.exe 内核路径（xhttp 节点需要）
	AllowLan bool   `json:"allowLan"` // 是否允许局域网连接（监听 0.0.0.0）
	BasePort int    `json:"basePort"` // 自动分配端口的起始值
}

// AppState 是应用的全局状态，所有读写都需要持有锁
type AppState struct {
	mu        sync.Mutex
	Settings  Settings `json:"settings"`
	Nodes     []*Node  `json:"nodes"`
	AutoStart bool     `json:"autoStart"` // 上次退出时服务是否在运行，应用启动时据此自动恢复服务
	file      string   // 持久化文件路径
}

// persistedState 是写入磁盘的精简结构（节点只保留必要字段）
type persistedState struct {
	Settings  Settings `json:"settings"`
	AutoStart bool     `json:"autoStart"`
	Nodes     []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Link string `json:"link"`
		Port int    `json:"port"`
	} `json:"nodes"`
}

// exeDir 返回当前可执行文件所在目录；获取失败时退回工作目录
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// findCore 在常见的 v2rayN 安装位置探测指定内核。
// kernel 取 "mihomo" 或 "xray"，对应 v2rayN 目录下的 bin\<kernel>\<kernel>.exe。
// 优先使用应用同目录下的内核，找不到时返回空串，由用户在设置中手动指定。
func findCore(kernel string) string {
	exeName := kernel + ".exe"
	candidates := []string{filepath.Join(exeDir(), exeName)}
	// 优先：通过运行中的 v2rayN 进程定位安装目录（不管装在哪都能找到）
	if dir := findV2rayNDir(); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "bin", kernel, exeName))
	}
	// 兜底：常见的 v2rayN 安装根目录
	roots := []string{
		`C:\Program Files\v2rayN`,
		`C:\Program Files (x86)\v2rayN`,
		`D:\Program Files\v2rayN`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "v2rayN"),
		filepath.Join(os.Getenv("USERPROFILE"), "v2rayN"),
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(r, "bin", kernel, exeName))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// detectCorePath 自动寻找 mihomo 内核路径
func detectCorePath() string {
	return findCore("mihomo")
}

// detectXrayPath 自动寻找 xray 内核路径（xhttp 节点需要）
func detectXrayPath() string {
	return findCore("xray")
}

// LoadState 从磁盘加载状态文件；文件不存在时返回带默认值的新状态
func LoadState() *AppState {
	st := &AppState{
		Settings: Settings{
			CorePath: detectCorePath(),
			XrayPath: detectXrayPath(),
			AllowLan: false,
			BasePort: 20001,
		},
		file: filepath.Join(exeDir(), "state.json"),
	}
	data, err := os.ReadFile(st.file)
	if err != nil {
		return st
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return st
	}
	if ps.Settings.BasePort > 0 {
		st.Settings = ps.Settings
	}
	st.AutoStart = ps.AutoStart
	// 如果保存的内核路径已失效，重新自动探测
	if _, err := os.Stat(st.Settings.CorePath); err != nil {
		st.Settings.CorePath = detectCorePath()
	}
	if _, err := os.Stat(st.Settings.XrayPath); err != nil {
		st.Settings.XrayPath = detectXrayPath()
	}
	for _, n := range ps.Nodes {
		node := &Node{ID: n.ID, Name: n.Name, Link: n.Link, Port: n.Port}
		// 重新解析分享链接，恢复协议信息和 mihomo 配置
		if err := node.reparse(); err != nil {
			node.ParseErr = err.Error()
		}
		st.Nodes = append(st.Nodes, node)
	}
	return st
}

// Save 将当前状态持久化到磁盘（调用方需已持有锁或确保无并发）
func (st *AppState) Save() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.saveLocked()
}

// saveLocked 在已持有锁的情况下执行实际的保存操作
func (st *AppState) saveLocked() error {
	var ps persistedState
	ps.Settings = st.Settings
	ps.AutoStart = st.AutoStart
	for _, n := range st.Nodes {
		ps.Nodes = append(ps.Nodes, struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Link string `json:"link"`
			Port int    `json:"port"`
		}{n.ID, n.Name, n.Link, n.Port})
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(st.file, data, 0o644)
}

// reparse 根据原始分享链接重新解析节点，刷新协议、服务器和 mihomo 配置字段
func (n *Node) reparse() error {
	parsed, err := ParseLink(n.Link)
	if err != nil {
		return err
	}
	n.Protocol = parsed.Protocol
	n.Server = parsed.Server
	n.Remote = parsed.Remote
	n.Proxy = parsed.Proxy
	n.Xray = parsed.Xray
	n.Core = parsed.Core
	if n.Name == "" {
		n.Name = parsed.Name
	}
	return nil
}

// nextFreePort 从 start 开始寻找一个未被其它节点占用的端口号
func (st *AppState) nextFreePort(start int) int {
	used := map[int]bool{}
	for _, n := range st.Nodes {
		if n.Port > 0 {
			used[n.Port] = true
		}
	}
	p := start
	for used[p] {
		p++
	}
	return p
}

// AutoAssignPorts 为所有未分配端口的节点按顺序分配端口，返回分配数量
func (st *AppState) AutoAssignPorts() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	count := 0
	for _, n := range st.Nodes {
		if n.Port == 0 && n.ParseErr == "" {
			n.Port = st.nextFreePort(st.Settings.BasePort)
			count++
		}
	}
	if count > 0 {
		_ = st.saveLocked()
	}
	return count
}

// validatePorts 检查所有节点端口是否合法且互不冲突，返回首个错误。
// xray 节点会额外占用"对外端口+10000"作为内部桥接端口，也一并检查冲突。
func (st *AppState) validatePorts() error {
	seen := map[int]string{}
	for _, n := range st.Nodes {
		if n.ParseErr != "" {
			continue
		}
		if n.Port <= 0 || n.Port > 55535 {
			return fmt.Errorf("节点「%s」未分配有效端口（须在 1-55535 之间）", n.Name)
		}
		if other, ok := seen[n.Port]; ok {
			return fmt.Errorf("端口 %d 被「%s」和「%s」重复使用", n.Port, other, n.Name)
		}
		seen[n.Port] = n.Name
	}
	// 检查 xray 节点的内部桥接端口是否与其它对外端口冲突
	for _, n := range st.Nodes {
		if n.ParseErr != "" || n.Core != "xray" {
			continue
		}
		ip := internalPort(n.Port)
		if other, ok := seen[ip]; ok {
			return fmt.Errorf("节点「%s」的内部桥接端口 %d 与「%s」的端口冲突，请调整端口", n.Name, ip, other)
		}
	}
	return nil
}
