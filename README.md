# MultiPortProxy · 多节点端口代理

[English](#english) | [中文](#中文)

一个 Windows 桌面小工具：把多个代理节点分别映射到本机的不同端口，每个端口同时提供 **socks5 和 http 代理**（mixed 模式），让指纹浏览器（AdsPower 等）的不同环境可以同时使用不同的节点出口。

> 复用 [v2rayN](https://github.com/2dust/v2rayN) 自带的 `mihomo` / `xray` 内核，无需额外下载内核；纯 Go 标准库实现，零第三方依赖，编译即单文件可执行程序。

---

## 中文

### 背景

v2rayN / Clash 这类客户端通常只暴露一个固定的本地端口，所有流量都走「当前激活」的那一个节点。当你需要让指纹浏览器的多个环境窗口**同时**走不同节点时，单一端口无法满足。

本工具为每个节点单独开一个本地端口，互不干扰：

```
指纹浏览器环境1 ──→ 127.0.0.1:20001 ──┐
指纹浏览器环境2 ──→ 127.0.0.1:20002 ──┤  内核（mihomo/xray）    节点1
指纹浏览器环境3 ──→ 127.0.0.1:20003 ──┼─（复用 v2rayN 自带）──→  节点2
指纹浏览器环境4 ──→ 127.0.0.1:20004 ──┘                        节点3...
```

### 特性

- **多端口映射**：每个节点一个本地端口，同时支持 socks5 与 http（mixed）。
- **批量导入**：直接粘贴 v2rayN 的节点分享链接，支持多行。
- **协议支持**：VLESS / VMess / Trojan / Shadowsocks / Hysteria2 / VLESS-xhttp。
- **双内核架构**：常规协议走 mihomo；xhttp 传输节点自动改由 xray 内核承载并桥接，对外端口用法完全一致。
- **延迟测试**：一键测试每个端口的实际连通性与延迟。
- **自动恢复**：上次退出时服务在运行的话，下次启动自动恢复。
- **内嵌网页界面**：以 Edge `--app` 模式弹出独立窗口，体验接近原生桌面应用。

### 依赖

- Windows 10/11
- `mihomo.exe` 内核（必需）：常规协议节点使用
- `xray.exe` 内核（可选）：仅当你导入 xhttp 节点时需要

> 两个内核都可以直接复用 v2rayN 安装目录下的 `bin\mihomo\mihomo.exe` 和 `bin\xray\xray.exe`。程序启动时会自动探测常见安装位置；找不到时可在「设置」里手动指定路径。本仓库**不分发任何内核**。

### 使用步骤

1. 从 [Releases](../../releases) 下载 `NodePortProxy.exe`（或自行编译，见下），双击运行，会自动弹出界面。
2. 在 v2rayN 中选中节点 → 右键 → 分享 → **复制分享链接到剪贴板**。
3. 粘贴到应用顶部的导入框 → 点击**导入**（支持多行批量）。
4. 点击**自动分配端口**（默认从 20001 起递增），或在表格中手动指定端口。
5. 点击**启动服务**。
6. 在指纹浏览器的代理设置中填写：
   - 代理类型：`socks5` 或 `http` 均可
   - 主机：`127.0.0.1`
   - 端口：节点对应的本地端口（表格中点击代理地址即可复制）

### 从源码编译

```bash
git clone https://github.com/<your-name>/MultiPortProxy.git
cd MultiPortProxy
go build -ldflags "-H windowsgui" -o NodePortProxy.exe .
```

仅依赖 Go 标准库。`-H windowsgui` 表示运行时不显示控制台黑窗口。

### 常见问题

- **AdsPower「检查代理」报失败，但浏览器实际能用？**
  若 v2rayN 同时开着系统代理，AdsPower 的代理检查器可能把系统代理叠加进检测链路而误报失败。直接打开浏览器环境访问 `https://ip.sb` 验证真实出口 IP 即可；实际浏览不受影响。

- **xhttp 节点连不上？**
  确认已正确设置 `xray.exe` 路径。xhttp 是 Xray 独有传输协议，mihomo 内核不支持，本工具会自动改用 xray 承载。

- **杀毒软件拦截 / 无法运行？**
  自行编译的无签名 exe 可能被 360、Windows Defender 等误报。把程序所在目录加入信任区/白名单即可。

### 免责声明

本工具仅用于在**本机**对你**自己拥有合法使用权**的代理节点做端口管理与分发，方便多环境调试等技术用途。使用者须遵守所在国家和地区的法律法规，并对自己的使用行为负全部责任。作者不对任何滥用行为承担责任。

---

## English

A small Windows desktop tool that maps multiple proxy nodes to **separate local ports**, each exposing both **socks5 and http** (mixed mode). This lets different profiles of an antidetect browser (e.g. AdsPower) use different proxy egress nodes **at the same time** — something a single shared local port cannot do.

### Highlights

- **One local port per node**, each speaking socks5 + http simultaneously.
- **Bulk import** of share links (VLESS / VMess / Trojan / Shadowsocks / Hysteria2 / VLESS-xhttp).
- **Dual-core**: regular protocols run on the `mihomo` core; `xhttp` transport nodes are transparently bridged through the `xray` core.
- **Latency test**, **auto-resume**, and an embedded web UI launched as an Edge `--app` window.

### Requirements

- Windows 10/11
- `mihomo.exe` (required) and `xray.exe` (only for xhttp nodes).
- Both cores can be reused from an existing v2rayN install (`bin\mihomo`, `bin\xray`). **This repo ships no cores.** The app auto-detects common install paths, or you can set them manually in Settings.

### Build

```bash
go build -ldflags "-H windowsgui" -o NodePortProxy.exe .
```

Pure Go standard library, no third-party dependencies.

### License & cores

Licensed under the [MIT License](LICENSE). This project only **invokes** the user's locally installed `mihomo` (GPL-3.0) and `xray` (MPL-2.0) cores via separate processes; it does **not** bundle, link, or redistribute them, and therefore is not a derivative work of either core.

### Disclaimer

This tool is intended solely for managing and fan-out of proxy nodes you are **legally entitled to use**, on your **own machine**, for technical purposes such as multi-profile testing. You are responsible for complying with the laws and regulations in your jurisdiction. The author assumes no liability for misuse.
