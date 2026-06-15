package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

// jsonResp 向客户端写出 JSON 响应
func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// jsonErr 写出统一格式的错误响应
func jsonErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// registerAPI 注册所有 REST 接口路由
func registerAPI(mux *http.ServeMux, st *AppState) {
	// 应用标识接口，用于单实例检测
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, map[string]string{"app": "NodePortProxy"})
	})

	// 获取完整状态：设置、节点列表、运行状态
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		resp := map[string]any{
			"settings": st.Settings,
			"nodes":    st.Nodes,
			"running":  core.IsRunning(),
		}
		buf, _ := json.Marshal(resp)
		st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(buf)
	})

	// 批量导入分享链接，逐行解析，返回成功/失败统计
	mux.HandleFunc("/api/import", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Links string `json:"links"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "请求格式错误")
			return
		}
		st.mu.Lock()
		existing := map[string]bool{}
		for _, n := range st.Nodes {
			existing[n.Link] = true
		}
		added, skipped := 0, 0
		var failed []string
		for _, line := range strings.Split(req.Links, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if existing[line] {
				skipped++ // 跳过完全相同的重复链接
				continue
			}
			node, err := ParseLink(line)
			if err != nil {
				failed = append(failed, err.Error())
				continue
			}
			existing[line] = true
			st.Nodes = append(st.Nodes, node)
			added++
		}
		_ = st.saveLocked()
		st.mu.Unlock()
		jsonResp(w, map[string]any{"added": added, "skipped": skipped, "failed": failed})
	})

	// 更新单个节点的名称或端口
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "请求格式错误")
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		for _, n := range st.Nodes {
			if n.ID == req.ID {
				if req.Name != "" {
					n.Name = req.Name
				}
				if req.Port >= 0 {
					n.Port = req.Port
				}
				_ = st.saveLocked()
				jsonResp(w, map[string]bool{"ok": true})
				return
			}
		}
		jsonErr(w, "节点不存在")
	})

	// 删除选中的节点
	mux.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "请求格式错误")
			return
		}
		del := map[string]bool{}
		for _, id := range req.IDs {
			del[id] = true
		}
		st.mu.Lock()
		var kept []*Node
		for _, n := range st.Nodes {
			if !del[n.ID] {
				kept = append(kept, n)
			}
		}
		removed := len(st.Nodes) - len(kept)
		st.Nodes = kept
		_ = st.saveLocked()
		st.mu.Unlock()
		jsonResp(w, map[string]int{"removed": removed})
	})

	// 自动为未分配端口的节点分配端口
	mux.HandleFunc("/api/assign", func(w http.ResponseWriter, r *http.Request) {
		count := st.AutoAssignPorts()
		jsonResp(w, map[string]int{"assigned": count})
	})

	// 自动探测 mihomo / xray 内核路径（优先通过运行中的 v2rayN 进程定位）
	mux.HandleFunc("/api/detect", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, map[string]string{
			"corePath": detectCorePath(),
			"xrayPath": detectXrayPath(),
		})
	})

	// 启动代理服务（已运行时相当于重启，应用最新配置）
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		// 启动前自动补齐未分配的端口，省去手动点"自动分配"
		st.AutoAssignPorts()
		if err := core.Start(st); err != nil {
			jsonErr(w, err.Error())
			return
		}
		// 记录运行状态，应用下次启动时自动恢复服务
		st.mu.Lock()
		st.AutoStart = true
		_ = st.saveLocked()
		st.mu.Unlock()
		jsonResp(w, map[string]bool{"ok": true})
	})

	// 停止代理服务
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		core.Stop()
		st.mu.Lock()
		st.AutoStart = false
		_ = st.saveLocked()
		st.mu.Unlock()
		jsonResp(w, map[string]bool{"ok": true})
	})

	// 并发测试所有节点的连通性和延迟
	mux.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		if !core.IsRunning() {
			jsonErr(w, "请先启动服务")
			return
		}
		st.mu.Lock()
		nodes := make([]*Node, len(st.Nodes))
		copy(nodes, st.Nodes)
		st.mu.Unlock()
		var wg sync.WaitGroup
		for _, n := range nodes {
			if n.Port <= 0 || n.ParseErr != "" {
				continue
			}
			wg.Add(1)
			go func(n *Node) {
				defer wg.Done()
				ms, err := TestNode(n.Port)
				st.mu.Lock()
				if err != nil {
					n.DelayMs = -1 // -1 表示测试失败
				} else {
					n.DelayMs = ms
				}
				st.mu.Unlock()
			}(n)
		}
		wg.Wait()
		jsonResp(w, map[string]bool{"ok": true})
	})

	// 获取内核运行日志
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, map[string]any{"logs": core.Logs()})
	})

	// 保存应用设置
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var req Settings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "请求格式错误")
			return
		}
		if req.CorePath != "" {
			if _, err := os.Stat(req.CorePath); err != nil {
				jsonErr(w, "mihomo 内核路径不存在: "+req.CorePath)
				return
			}
		}
		if req.XrayPath != "" {
			if _, err := os.Stat(req.XrayPath); err != nil {
				jsonErr(w, "xray 内核路径不存在: "+req.XrayPath)
				return
			}
		}
		if req.BasePort <= 0 || req.BasePort > 65000 {
			jsonErr(w, "起始端口必须在 1-65000 之间")
			return
		}
		st.mu.Lock()
		st.Settings = req
		_ = st.saveLocked()
		st.mu.Unlock()
		jsonResp(w, map[string]bool{"ok": true})
	})
}
