package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParseLink 解析一条节点分享链接（v2rayN 右键节点"复制分享链接"得到的格式），
// 返回包含 mihomo proxy 配置的 Node。支持 vless / vmess / trojan / ss / hysteria2 协议。
func ParseLink(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	switch {
	case strings.HasPrefix(link, "vless://"):
		return parseVless(link)
	case strings.HasPrefix(link, "vmess://"):
		return parseVmess(link)
	case strings.HasPrefix(link, "trojan://"):
		return parseTrojan(link)
	case strings.HasPrefix(link, "ss://"):
		return parseSS(link)
	case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):
		return parseHysteria2(link)
	case link == "":
		return nil, fmt.Errorf("空行")
	default:
		scheme := link
		if i := strings.Index(link, "://"); i > 0 {
			scheme = link[:i]
		}
		return nil, fmt.Errorf("不支持的协议: %s", scheme)
	}
}

// newID 生成 8 字节随机十六进制字符串作为节点 ID
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// b64Decode 尝试用多种 base64 变体解码（标准/URL安全/带或不带填充），
// 因为不同客户端生成的分享链接编码方式不统一
func b64Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("base64 解码失败")
}

// nodeName 从 URL fragment 中提取节点备注名，为空时用"协议-服务器:端口"兜底
func nodeName(fragment, protocol, server string, port int) string {
	name := strings.TrimSpace(fragment)
	if decoded, err := url.QueryUnescape(name); err == nil {
		name = strings.TrimSpace(decoded)
	}
	if name == "" {
		name = fmt.Sprintf("%s-%s:%d", protocol, server, port)
	}
	return name
}

// applyTransport 处理 vless/trojan 链接中的传输层参数（ws/grpc/httpupgrade 等），
// 将其转换为 mihomo 的 network 和对应 opts 配置
func applyTransport(p map[string]any, q url.Values) error {
	network := q.Get("type")
	if network == "" || network == "tcp" || network == "raw" {
		return nil // tcp 是默认值，无需额外配置
	}
	// xhttp/splithttp 是 Xray 独有的传输层，mihomo 内核不支持
	if network == "xhttp" || network == "splithttp" {
		return fmt.Errorf("mihomo 内核不支持 xhttp/splithttp 传输协议")
	}
	switch network {
	case "ws", "httpupgrade":
		p["network"] = network
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if host := q.Get("host"); host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		p[network+"-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]any{"grpc-service-name": q.Get("serviceName")}
	case "h2", "http":
		p["network"] = "h2"
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if host := q.Get("host"); host != "" {
			opts["host"] = []string{host}
		}
		p["h2-opts"] = opts
	default:
		p["network"] = network
	}
	return nil
}

// applyTLS 处理 security/sni/fp/alpn/allowInsecure 等 TLS 相关参数，
// 包括 REALITY 的 public-key 和 short-id
func applyTLS(p map[string]any, q url.Values) {
	security := q.Get("security")
	if security == "tls" || security == "reality" {
		p["tls"] = true
	}
	if sni := q.Get("sni"); sni != "" {
		p["servername"] = sni
	}
	if fp := q.Get("fp"); fp != "" {
		p["client-fingerprint"] = fp
	}
	if security == "reality" {
		p["reality-opts"] = map[string]any{
			"public-key": q.Get("pbk"),
			"short-id":   q.Get("sid"),
		}
	}
	if alpn := q.Get("alpn"); alpn != "" {
		p["alpn"] = strings.Split(alpn, ",")
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
		p["skip-cert-verify"] = true
	}
}

// parseVless 解析 vless:// 链接，格式：vless://uuid@host:port?参数#备注
func parseVless(link string) (*Node, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("链接格式错误: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	if u.Hostname() == "" || port == 0 {
		return nil, fmt.Errorf("缺少服务器地址或端口")
	}
	q := u.Query()
	// xhttp/splithttp 是 Xray 独有传输协议，mihomo 不支持，改用 xray 内核承载
	if network := q.Get("type"); network == "xhttp" || network == "splithttp" {
		return parseVlessXhttp(u, q, link, port)
	}
	name := nodeName(u.Fragment, "vless", u.Hostname(), port)
	p := map[string]any{
		"name":   name,
		"type":   "vless",
		"server": u.Hostname(),
		"port":   port,
		"uuid":   u.User.Username(),
		"udp":    true,
	}
	if flow := q.Get("flow"); flow != "" {
		p["flow"] = flow
	}
	applyTLS(p, q)
	if err := applyTransport(p, q); err != nil {
		return nil, err
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "vless",
		Server: u.Hostname(), Remote: port, Proxy: p}, nil
}

// parseVlessXhttp 解析使用 xhttp/splithttp 传输的 vless 链接。
// 这类节点由 xray 内核承载：生成 xray 的 outbound 配置，
// 运行时 xray 在"对外端口+10000"开一个内部 socks 入站，mihomo 通过 socks5 桥接过去，
// 因此对外端口的使用方式与其它节点完全一致（http+socks 均可）。
func parseVlessXhttp(u *url.URL, q url.Values, link string, port int) (*Node, error) {
	name := nodeName(u.Fragment, "vless-xhttp", u.Hostname(), port)

	// 构造 xhttp 传输层配置
	xhttpSettings := map[string]any{}
	if path := q.Get("path"); path != "" {
		xhttpSettings["path"] = path
	}
	if host := q.Get("host"); host != "" {
		xhttpSettings["host"] = host
	}
	// mode 可选值：auto / packet-up / stream-up / stream-one，auto 为默认可省略
	if mode := q.Get("mode"); mode != "" && mode != "auto" {
		xhttpSettings["mode"] = mode
	}
	stream := map[string]any{
		"network":       "xhttp",
		"xhttpSettings": xhttpSettings,
	}

	// SNI 回退顺序：显式 sni → host（CDN 节点常省略 sni，此时用回源域名）→ 连接地址
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("host")
	}
	if sni == "" {
		sni = u.Hostname()
	}
	// 指纹（utls）缺省时补 chrome，CF 等 CDN 多数要求 utls 伪装，否则握手被拒
	fp := q.Get("fp")
	if fp == "" {
		fp = "chrome"
	}
	// TLS / REALITY 配置
	switch q.Get("security") {
	case "tls":
		stream["security"] = "tls"
		tls := map[string]any{
			"serverName":  sni,
			"fingerprint": fp,
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		if q.Get("allowInsecure") == "1" {
			tls["allowInsecure"] = true
		}
		stream["tlsSettings"] = tls
	case "reality":
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName":  sni,
			"fingerprint": fp,
			"publicKey":   q.Get("pbk"),
			"shortId":     q.Get("sid"),
			"spiderX":     q.Get("spx"),
		}
	}

	user := map[string]any{
		"id":         u.User.Username(),
		"encryption": "none",
	}
	if flow := q.Get("flow"); flow != "" {
		user["flow"] = flow
	}
	outbound := map[string]any{
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": u.Hostname(),
				"port":    port,
				"users":   []any{user},
			}},
		},
		"streamSettings": stream,
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "vless-xhttp",
		Core: "xray", Server: u.Hostname(), Remote: port, Xray: outbound}, nil
}

// parseVmess 解析 vmess:// 链接，v2rayN 格式为 base64 编码的 JSON
func parseVmess(link string) (*Node, error) {
	raw, err := b64Decode(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		return nil, fmt.Errorf("vmess 链接 %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("vmess JSON 解析失败: %v", err)
	}
	// JSON 中的字段可能是字符串或数字，统一转换
	str := func(key string) string {
		if v, ok := m[key]; ok {
			return strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		return ""
	}
	num := func(key string) int {
		n, _ := strconv.Atoi(str(key))
		return n
	}
	server, port := str("add"), num("port")
	if server == "" || port == 0 {
		return nil, fmt.Errorf("vmess 缺少服务器地址或端口")
	}
	name := str("ps")
	if name == "" {
		name = fmt.Sprintf("vmess-%s:%d", server, port)
	}
	cipher := str("scy")
	if cipher == "" {
		cipher = "auto"
	}
	p := map[string]any{
		"name":    name,
		"type":    "vmess",
		"server":  server,
		"port":    port,
		"uuid":    str("id"),
		"alterId": num("aid"),
		"cipher":  cipher,
		"udp":     true,
	}
	if str("tls") == "tls" {
		p["tls"] = true
		if sni := str("sni"); sni != "" {
			p["servername"] = sni
		}
		if fp := str("fp"); fp != "" {
			p["client-fingerprint"] = fp
		}
	}
	switch network := str("net"); network {
	case "ws", "httpupgrade":
		p["network"] = network
		opts := map[string]any{}
		if path := str("path"); path != "" {
			opts["path"] = path
		}
		if host := str("host"); host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		p[network+"-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]any{"grpc-service-name": str("path")}
	case "h2":
		p["network"] = "h2"
		p["h2-opts"] = map[string]any{"path": str("path"), "host": []string{str("host")}}
	case "xhttp", "splithttp":
		return nil, fmt.Errorf("mihomo 内核不支持 xhttp/splithttp 传输协议")
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "vmess",
		Server: server, Remote: port, Proxy: p}, nil
}

// parseTrojan 解析 trojan:// 链接，格式：trojan://password@host:port?参数#备注
func parseTrojan(link string) (*Node, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("链接格式错误: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	if u.Hostname() == "" || port == 0 {
		return nil, fmt.Errorf("缺少服务器地址或端口")
	}
	q := u.Query()
	name := nodeName(u.Fragment, "trojan", u.Hostname(), port)
	password := u.User.Username()
	if pw, ok := u.User.Password(); ok {
		password = password + ":" + pw
	}
	p := map[string]any{
		"name":     name,
		"type":     "trojan",
		"server":   u.Hostname(),
		"port":     port,
		"password": password,
		"udp":      true,
	}
	// trojan 默认就是 TLS，只需补充 sni 等参数
	if sni := q.Get("sni"); sni != "" {
		p["sni"] = sni
	}
	if fp := q.Get("fp"); fp != "" {
		p["client-fingerprint"] = fp
	}
	if q.Get("allowInsecure") == "1" {
		p["skip-cert-verify"] = true
	}
	if err := applyTransport(p, q); err != nil {
		return nil, err
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "trojan",
		Server: u.Hostname(), Remote: port, Proxy: p}, nil
}

// parseSS 解析 ss:// 链接，兼容两种格式：
// SIP002: ss://base64(method:password)@host:port#备注
// 旧格式: ss://base64(method:password@host:port)#备注
func parseSS(link string) (*Node, error) {
	body := strings.TrimPrefix(link, "ss://")
	fragment := ""
	if i := strings.Index(body, "#"); i >= 0 {
		fragment = body[i+1:]
		body = body[:i]
	}
	// 去掉插件等查询参数（simple-obfs/v2ray-plugin 暂不支持）
	if i := strings.Index(body, "?"); i >= 0 {
		q, _ := url.ParseQuery(body[i+1:])
		if q.Get("plugin") != "" {
			return nil, fmt.Errorf("暂不支持带 plugin 插件的 ss 节点")
		}
		body = body[:i]
	}
	var method, password, server string
	var port int
	if i := strings.LastIndex(body, "@"); i >= 0 {
		// SIP002 格式：@ 前是 base64(method:password)，@ 后是 host:port
		userinfo, _ := url.QueryUnescape(body[:i])
		decoded, err := b64Decode(userinfo)
		if err != nil {
			// 部分客户端 userinfo 不做 base64（如 2022-blake3 系列），直接按明文处理
			decoded = []byte(userinfo)
		}
		mp := strings.SplitN(string(decoded), ":", 2)
		if len(mp) != 2 {
			return nil, fmt.Errorf("ss 用户信息格式错误")
		}
		method, password = mp[0], mp[1]
		hostport := body[i+1:]
		j := strings.LastIndex(hostport, ":")
		if j < 0 {
			return nil, fmt.Errorf("ss 缺少端口")
		}
		server = strings.Trim(hostport[:j], "[]")
		port, _ = strconv.Atoi(hostport[j+1:])
	} else {
		// 旧格式：整体 base64
		decoded, err := b64Decode(body)
		if err != nil {
			return nil, fmt.Errorf("ss 链接 %v", err)
		}
		s := string(decoded)
		i := strings.LastIndex(s, "@")
		if i < 0 {
			return nil, fmt.Errorf("ss 链接格式错误")
		}
		mp := strings.SplitN(s[:i], ":", 2)
		if len(mp) != 2 {
			return nil, fmt.Errorf("ss 链接格式错误")
		}
		method, password = mp[0], mp[1]
		hostport := s[i+1:]
		j := strings.LastIndex(hostport, ":")
		if j < 0 {
			return nil, fmt.Errorf("ss 缺少端口")
		}
		server = strings.Trim(hostport[:j], "[]")
		port, _ = strconv.Atoi(hostport[j+1:])
	}
	if server == "" || port == 0 {
		return nil, fmt.Errorf("ss 缺少服务器地址或端口")
	}
	name := nodeName(fragment, "ss", server, port)
	p := map[string]any{
		"name":     name,
		"type":     "ss",
		"server":   server,
		"port":     port,
		"cipher":   method,
		"password": password,
		"udp":      true,
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "ss",
		Server: server, Remote: port, Proxy: p}, nil
}

// parseHysteria2 解析 hysteria2:// 或 hy2:// 链接，
// 格式：hysteria2://auth@host:port?sni=xx&insecure=1&obfs=salamander&obfs-password=xx#备注
func parseHysteria2(link string) (*Node, error) {
	// 统一前缀，方便 url.Parse 处理
	link2 := strings.Replace(link, "hy2://", "hysteria2://", 1)
	u, err := url.Parse(link2)
	if err != nil {
		return nil, fmt.Errorf("链接格式错误: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	if u.Hostname() == "" || port == 0 {
		return nil, fmt.Errorf("缺少服务器地址或端口")
	}
	q := u.Query()
	name := nodeName(u.Fragment, "hysteria2", u.Hostname(), port)
	password := u.User.Username()
	if pw, ok := u.User.Password(); ok {
		password = password + ":" + pw
	}
	p := map[string]any{
		"name":     name,
		"type":     "hysteria2",
		"server":   u.Hostname(),
		"port":     port,
		"password": password,
	}
	if sni := q.Get("sni"); sni != "" {
		p["sni"] = sni
	}
	if q.Get("insecure") == "1" {
		p["skip-cert-verify"] = true
	}
	if obfs := q.Get("obfs"); obfs != "" {
		p["obfs"] = obfs
		p["obfs-password"] = q.Get("obfs-password")
	}
	// 端口跳跃参数（mport/ports）
	if ports := q.Get("mport"); ports != "" {
		p["ports"] = ports
	}
	return &Node{ID: newID(), Name: name, Link: link, Protocol: "hysteria2",
		Server: u.Hostname(), Remote: port, Proxy: p}, nil
}
