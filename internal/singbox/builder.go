package singbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// NodeType describes one inbound kind the panel can create in one click.
type NodeType struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Hint        string `json:"hint"`
	Transport   string `json:"transport"`
	Security    string `json:"security"`
	NeedsTLS    bool   `json:"needs_tls"`
	DefaultPort int    `json:"default_port"`
}

// NodeTypes is the catalogue shown in the creation wizard.
func NodeTypes() []NodeType {
	return []NodeType{
		{ID: "hysteria2", Label: "Hysteria2", Hint: "QUIC 传输，弱网和跨运营商表现最好，客户端需支持 Hysteria2。", Transport: "QUIC", Security: "TLS（自签证书）", NeedsTLS: true, DefaultPort: 12538},
		{ID: "vless-reality", Label: "VLESS + Reality", Hint: "借用真实网站握手，不需要证书，抗封锁能力最强。", Transport: "TCP", Security: "Reality", DefaultPort: 4430},
		{ID: "trojan", Label: "Trojan", Hint: "标准 TLS 伪装，客户端兼容性最好。", Transport: "TCP", Security: "TLS（自签证书）", NeedsTLS: true, DefaultPort: 18443},
		{ID: "shadowsocks", Label: "Shadowsocks 2022", Hint: "轻量 AEAD 代理，CPU 占用低，适合低配机器。", Transport: "TCP + UDP", Security: "AEAD-2022", DefaultPort: 18388},
		{ID: "socks", Label: "SOCKS5", Hint: "不加密的代理，仅建议用于本机或内网，不要直接暴露公网。", Transport: "TCP", Security: "用户名密码", DefaultPort: 21080},
	}
}

// nodeType looks up one type by ID.
func nodeType(id string) (NodeType, bool) {
	for _, item := range NodeTypes() {
		if item.ID == id {
			return item, true
		}
	}
	return NodeType{}, false
}

// DefaultPortFor returns the suggested port for a type.
func DefaultPortFor(id string) int {
	if t, ok := nodeType(id); ok {
		return t.DefaultPort
	}
	return 0
}

// NodeKind is the sing-box inbound type, which differs from the panel type for
// the Reality variant.
func NodeKind(id string) string {
	if id == "vless-reality" {
		return "vless"
	}
	return id
}

// NormalizeName keeps a node name usable as a file name and share label.
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-", "..", "-", ".", "-")
	name = replacer.Replace(name)
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if r == '-' && len(out) > 0 && out[len(out)-1] == '-' {
			continue
		}
		out = append(out, r)
	}
	cleaned := strings.Trim(strings.TrimSpace(string(out)), "-")
	if len([]rune(cleaned)) > 32 {
		cleaned = strings.Trim(string([]rune(cleaned)[:32]), "-")
	}
	return cleaned
}

// portFree reports whether nothing listens on the port right now.
func portFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	for _, addr := range []string{fmt.Sprintf("0.0.0.0:%d", port), fmt.Sprintf("[::]:%d", port)} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// pickPort returns the first free port at or above the preference.
func (m *Manager) pickPort(preferred int, used map[int]bool) (int, error) {
	start := preferred
	if start <= 0 || start > 65000 {
		start = 20000
	}
	for port := start; port < 65000; port++ {
		if used[port] || !portFree(port) {
			continue
		}
		return port, nil
	}
	return 0, errors.New("没有可用的端口，请手动指定")
}

// Create writes a new inbound file, validates it and restarts sing-box.
func (m *Manager) Create(req CreateRequest) (Node, Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	typeDef, ok := nodeType(strings.TrimSpace(req.Type))
	if !ok {
		return Node{}, Check{}, fmt.Errorf("不支持的节点类型：%s", req.Type)
	}
	if !m.Installed() {
		return Node{}, Check{}, errors.New("本机未安装 sing-box，请先在终端安装 sing-box")
	}
	if st, err := os.Stat(m.configPath); err != nil || st.IsDir() {
		return Node{}, Check{}, fmt.Errorf("未找到 sing-box 主配置 %s", m.configPath)
	}
	if err := os.MkdirAll(m.confDir, 0o700); err != nil {
		return Node{}, Check{}, fmt.Errorf("创建配置目录失败：%w", err)
	}
	existing, err := m.inboundFiles()
	if err != nil {
		return Node{}, Check{}, err
	}
	used := map[int]bool{}
	names := map[string]bool{}
	for _, file := range existing {
		id := strings.TrimSuffix(strings.TrimSuffix(file.name, disabledExt), enabledExt)
		names[id] = true
		if raw, readErr := os.ReadFile(file.path); readErr == nil {
			if node := parseNode(file.name, raw, file.enabled); node.Port > 0 {
				used[node.Port] = true
			}
		}
	}
	port := req.Port
	if port == 0 {
		port, err = m.pickPort(typeDef.DefaultPort, used)
		if err != nil {
			return Node{}, Check{}, err
		}
	} else {
		if port < 1 || port > 65535 {
			return Node{}, Check{}, errors.New("端口必须在 1 到 65535 之间")
		}
		if used[port] {
			return Node{}, Check{}, fmt.Errorf("端口 %d 已被其他节点占用", port)
		}
		if !portFree(port) {
			return Node{}, Check{}, fmt.Errorf("端口 %d 已被其他程序占用", port)
		}
	}
	name := NormalizeName(req.Name)
	if name == "" {
		name = fmt.Sprintf("%s-%d", typeDef.ID, port)
	}
	base := name
	for i := 2; names[base]; i++ {
		base = fmt.Sprintf("%s-%d", name, i)
	}
	name = base
	server := strings.TrimSpace(req.Server)
	if server == "" {
		server = m.host
	}
	doc, err := m.buildInbound(typeDef, name, port, server, req.Method)
	if err != nil {
		return Node{}, Check{}, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Node{}, Check{}, err
	}
	raw = append(raw, 10)
	path := filepath.Join(m.confDir, name+enabledExt)
	if _, statErr := os.Stat(path); statErr == nil {
		return Node{}, Check{}, fmt.Errorf("节点 %s 已存在", name)
	}
	if err := writeAtomic(path, raw, 0o600); err != nil {
		return Node{}, Check{}, err
	}
	check := m.checkSoft()
	if !check.OK {
		_ = os.Remove(path)
		return Node{}, check, fmt.Errorf("配置校验失败，未创建节点：%s", check.Output)
	}
	node, err := m.Find(name)
	if err != nil {
		return Node{}, check, err
	}
	if err := m.reloadUnit(); err != nil {
		return node, check, err
	}
	return node, check, nil
}

// buildInbound renders the JSON document for one node type.
func (m *Manager) buildInbound(t NodeType, name string, port int, server, method string) (map[string]interface{}, error) {
	inbound := map[string]interface{}{
		"type":        NodeKind(t.ID),
		"tag":         name,
		"listen":      "::",
		"listen_port": port,
	}
	switch t.ID {
	case "hysteria2":
		password, err := RandomPassword(16)
		if err != nil {
			return nil, err
		}
		certPath, keyPath, err := EnsureCertificate(m.root, server)
		if err != nil {
			return nil, err
		}
		inbound["users"] = []interface{}{map[string]interface{}{"password": password}}
		inbound["tls"] = map[string]interface{}{
			"enabled":          true,
			"alpn":             []interface{}{"h3"},
			"certificate_path": certPath,
			"key_path":         keyPath,
		}
	case "vless-reality":
		uuid, err := NewUUID()
		if err != nil {
			return nil, err
		}
		priv, _, err := RealityKeypair()
		if err != nil {
			return nil, err
		}
		shortID, err := RandomHex(4)
		if err != nil {
			return nil, err
		}
		inbound["users"] = []interface{}{map[string]interface{}{"uuid": uuid, "flow": "xtls-rprx-vision"}}
		inbound["tls"] = map[string]interface{}{
			"enabled":     true,
			"server_name": "www.microsoft.com",
			"reality": map[string]interface{}{
				"enabled":     true,
				"handshake":   map[string]interface{}{"server": "www.microsoft.com", "server_port": 443},
				"private_key": priv,
				"short_id":    []interface{}{shortID},
			},
		}
	case "trojan":
		password, err := RandomPassword(16)
		if err != nil {
			return nil, err
		}
		certPath, keyPath, err := EnsureCertificate(m.root, server)
		if err != nil {
			return nil, err
		}
		inbound["users"] = []interface{}{map[string]interface{}{"password": password}}
		inbound["tls"] = map[string]interface{}{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		}
	case "shadowsocks":
		methodName := strings.TrimSpace(method)
		if methodName == "" {
			methodName = "2022-blake3-aes-128-gcm"
		}
		keyLen := 0
		switch methodName {
		case "2022-blake3-aes-128-gcm", "aes-128-gcm", "chacha20-ietf-poly1305":
			keyLen = 16
		case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "aes-256-gcm":
			keyLen = 32
		default:
			return nil, fmt.Errorf("不支持的 Shadowsocks 方法：%s", methodName)
		}
		key, err := SS2022Key(keyLen)
		if err != nil {
			return nil, err
		}
		inbound["method"] = methodName
		inbound["password"] = key
	case "socks":
		password, err := RandomPassword(12)
		if err != nil {
			return nil, err
		}
		inbound["users"] = []interface{}{map[string]interface{}{"username": "warden", "password": password}}
	default:
		return nil, fmt.Errorf("不支持的节点类型：%s", t.ID)
	}
	return map[string]interface{}{"inbounds": []interface{}{inbound}}, nil
}

// Reset regenerates the credential of one node in place and restarts sing-box.
func (m *Manager) Reset(id string) (Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, err := m.Find(id)
	if err != nil {
		return Node{}, err
	}
	path := filepath.Join(m.confDir, node.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Node{}, fmt.Errorf("读取配置失败：%w", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Node{}, fmt.Errorf("配置不是合法 JSON：%v", err)
	}
	changed := false
	for _, entry := range asSlice(doc["inbounds"]) {
		inbound := asMap(entry)
		if inbound == nil {
			continue
		}
		switch asString(inbound["type"]) {
		case "vless":
			uuid, genErr := NewUUID()
			if genErr != nil {
				return Node{}, genErr
			}
			flow := ""
			if users := asSlice(inbound["users"]); len(users) > 0 {
				if u := asMap(users[0]); u != nil {
					flow = asString(u["flow"])
				}
			}
			user := map[string]interface{}{"uuid": uuid}
			if flow != "" {
				user["flow"] = flow
			}
			inbound["users"] = []interface{}{user}
			if tls := asMap(inbound["tls"]); tls != nil {
				if reality := asMap(tls["reality"]); reality != nil && asBool(reality["enabled"]) {
					priv, _, keyErr := RealityKeypair()
					if keyErr != nil {
						return Node{}, keyErr
					}
					shortID, idErr := RandomHex(4)
					if idErr != nil {
						return Node{}, idErr
					}
					reality["private_key"] = priv
					reality["short_id"] = []interface{}{shortID}
				}
			}
			changed = true
		case "hysteria2", "trojan":
			password, genErr := RandomPassword(16)
			if genErr != nil {
				return Node{}, genErr
			}
			inbound["users"] = []interface{}{map[string]interface{}{"password": password}}
			changed = true
		case "shadowsocks":
			methodName := asString(inbound["method"])
			keyLen := 16
			if strings.Contains(methodName, "256") || strings.Contains(methodName, "chacha20-poly1305") {
				keyLen = 32
			}
			key, genErr := SS2022Key(keyLen)
			if genErr != nil {
				return Node{}, genErr
			}
			inbound["password"] = key
			changed = true
		case "socks":
			password, genErr := RandomPassword(12)
			if genErr != nil {
				return Node{}, genErr
			}
			username := "warden"
			if users := asSlice(inbound["users"]); len(users) > 0 {
				if u := asMap(users[0]); u != nil && asString(u["username"]) != "" {
					username = asString(u["username"])
				}
			}
			inbound["users"] = []interface{}{map[string]interface{}{"username": username, "password": password}}
			changed = true
		}
	}
	if !changed {
		return Node{}, fmt.Errorf("节点类型 %s 暂不支持重置密钥", node.Type)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Node{}, err
	}
	out = append(out, 10)
	if err := writeAtomic(path, out, 0o600); err != nil {
		return Node{}, err
	}
	check := m.checkSoft()
	if !check.OK {
		if restoreErr := writeAtomic(path, raw, 0o600); restoreErr != nil {
			m.log.Errorf("singbox: restore after failed reset: %v", restoreErr)
		}
		return Node{}, fmt.Errorf("重置后配置校验失败，已恢复：%s", check.Output)
	}
	if err := m.reloadUnit(); err != nil {
		return Node{}, err
	}
	return m.Find(node.ID)
}
