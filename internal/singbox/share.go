package singbox

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// shareHost is the address a client dials. It prefers the configured public
// host and falls back to the address of the connection that asked.
func (m *Manager) shareHost(fallback string) string {
	if host := strings.TrimSpace(m.host); host != "" {
		return host
	}
	if host := strings.TrimSpace(fallback); host != "" {
		return host
	}
	return "127.0.0.1"
}

// ShareLinks renders one client link per user of one node.
func (m *Manager) ShareLinks(id, fallbackHost string) (Node, []ShareLink, error) {
	node, err := m.Find(id)
	if err != nil {
		return Node{}, nil, err
	}
	host := m.shareHost(fallbackHost)
	var links []ShareLink
	for inboundIndex, inbound := range node.Inbounds {
		users := asSlice(inbound["users"])
		kind := asString(inbound["type"])
		if len(users) == 0 && kind == "shadowsocks" {
			users = []interface{}{map[string]interface{}{"password": asString(inbound["password"])}}
		}
		for userIndex, raw := range users {
			user := asMap(raw)
			label := asString(user["name"])
			if label == "" {
				label = node.Name()
				if len(users) > 1 {
					label = fmt.Sprintf("%s-%d", node.Name(), userIndex+1)
				}
			}
			link := ShareLink{Label: label, Inbound: inboundIndex, User: userIndex}
			uri, genErr := shareURI(node, inbound, user, host)
			if genErr != nil {
				link.Error = genErr.Error()
			} else {
				link.URI = uri
			}
			links = append(links, link)
		}
		if len(users) == 0 {
			link := ShareLink{Label: node.Name(), Inbound: inboundIndex, Error: "该节点没有可生成链接的用户"}
			links = append(links, link)
		}
	}
	if len(links) == 0 {
		return node, nil, errors.New("该节点没有可生成链接的用户")
	}
	return node, links, nil
}

// shareURI renders the client URI of one user of one inbound.
func shareURI(node Node, inbound, user map[string]interface{}, host string) (string, error) {
	kind := asString(inbound["type"])
	port := asInt(inbound["listen_port"])
	if port == 0 {
		port = node.Port
	}
	if port == 0 {
		return "", errors.New("节点端口为空")
	}
	if host == "" {
		return "", errors.New("未配置对外地址")
	}
	name := shareLabel(node, user)
	server := net.JoinHostPort(host, strconv.Itoa(port))
	switch kind {
	case "hysteria2":
		password := asString(user["password"])
		if password == "" {
			return "", errors.New("节点缺少密码")
		}
		values := url.Values{}
		values.Set("sni", host)
		values.Set("insecure", "1")
		return fmt.Sprintf("hysteria2://%s@%s/?%s#%s", url.User(password).String(), server, values.Encode(), url.PathEscape(name)), nil
	case "vless":
		uuid := asString(user["uuid"])
		if uuid == "" {
			return "", errors.New("节点缺少 UUID")
		}
		values := url.Values{}
		values.Set("type", "tcp")
		values.Set("encryption", "none")
		tls := asMap(inbound["tls"])
		security := "none"
		if tls != nil && asBool(tls["enabled"]) {
			security = "tls"
			if reality := asMap(tls["reality"]); reality != nil && asBool(reality["enabled"]) {
				security = "reality"
				if pub := realityPublicKey(asString(reality["private_key"])); pub != "" {
					values.Set("pbk", pub)
				}
				if ids := asSlice(reality["short_id"]); len(ids) > 0 {
					values.Set("sid", asString(ids[0]))
				}
				if hs := asMap(reality["handshake"]); hs != nil {
					if sni := asString(hs["server"]); sni != "" {
						values.Set("sni", sni)
					}
				}
				values.Set("fp", "chrome")
			} else if sni := asString(tls["server_name"]); sni != "" {
				values.Set("sni", sni)
				values.Set("allowInsecure", "1")
			}
		}
		values.Set("security", security)
		if flow := asString(user["flow"]); flow != "" {
			values.Set("flow", flow)
		}
		return fmt.Sprintf("vless://%s@%s?%s#%s", uuid, server, values.Encode(), url.PathEscape(name)), nil
	case "trojan":
		password := asString(user["password"])
		if password == "" {
			return "", errors.New("节点缺少密码")
		}
		values := url.Values{}
		if tls := asMap(inbound["tls"]); tls != nil {
			if sni := asString(tls["server_name"]); sni != "" {
				values.Set("sni", sni)
			}
			values.Set("security", "tls")
			values.Set("allowInsecure", "1")
		}
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.QueryEscape(password), server, values.Encode(), url.PathEscape(name)), nil
	case "shadowsocks":
		method := asString(inbound["method"])
		password := asString(inbound["password"])
		if password == "" {
			password = asString(user["password"])
		}
		if method == "" || password == "" {
			return "", errors.New("节点缺少加密方法或密码")
		}
		credentials := base64URLRaw(method + ":" + password)
		return fmt.Sprintf("ss://%s@%s#%s", credentials, server, url.PathEscape(name)), nil
	case "socks":
		username := asString(user["username"])
		password := asString(user["password"])
		if username == "" {
			return "", errors.New("节点缺少用户名")
		}
		return fmt.Sprintf("socks://%s:%s@%s#%s", url.QueryEscape(username), url.QueryEscape(password), server, url.PathEscape(name)), nil
	default:
		return "", fmt.Errorf("暂不支持为 %s 生成分享链接", kind)
	}
}

// shareLabel builds the fragment shown in clients.
func shareLabel(node Node, user map[string]interface{}) string {
	base := node.Name()
	if label := asString(user["name"]); label != "" {
		return base + "-" + label
	}
	return base
}

// base64URLRaw encodes without padding, which Shadowsocks URIs require in the
// userinfo section.
func base64URLRaw(text string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}

// Connection is one live connection reported by sing-box for the panel.
type Connection struct {
	ID        string `json:"id"`
	Network   string `json:"network"`
	Type      string `json:"type"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Inbound   string `json:"inbound"`
	Outbound  string `json:"outbound"`
	Rule      string `json:"rule"`
	Process   string `json:"process"`
	StartedAt string `json:"started_at"`
	Upload    int64  `json:"upload"`
	Download  int64  `json:"download"`
}

// Traffic is the sing-box traffic counter.
type Traffic struct {
	UploadTotal   int64  `json:"upload_total"`
	DownloadTotal int64  `json:"download_total"`
	UpRate        int64  `json:"up_rate"`
	DownRate      int64  `json:"down_rate"`
	Connections   int    `json:"connections"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Available     bool   `json:"available"`
	Error         string `json:"error,omitempty"`
	At            int64  `json:"at"`
}

// ClashAPIAddr is the local address of the experimental Clash API.
func (m *Manager) ClashAPIAddr() string {
	raw, err := os.ReadFile(m.configPath)
	if err != nil {
		return ""
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	experimental := asMap(doc["experimental"])
	if experimental == nil {
		return ""
	}
	clash := asMap(experimental["clash_api"])
	if clash == nil {
		return ""
	}
	addr := asString(clash["external_controller"])
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	if !strings.Contains(addr, ":") {
		addr += ":9090"
	}
	return addr
}
