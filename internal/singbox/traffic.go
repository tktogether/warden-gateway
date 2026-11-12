package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// clashSnapshot is the subset of the Clash API the panel reads.
type clashSnapshot struct {
	DownloadTotal int64 `json:"downloadTotal"`
	UploadTotal   int64 `json:"uploadTotal"`
	Connections   []struct {
		ID       string `json:"id"`
		Metadata struct {
			Network         string `json:"network"`
			Type            string `json:"type"`
			SourceIP        string `json:"sourceIP"`
			SourcePort      string `json:"sourcePort"`
			DestinationIP   string `json:"destinationIP"`
			DestinationPort string `json:"destinationPort"`
			Host            string `json:"host"`
			ProcessPath     string `json:"processPath"`
		} `json:"metadata"`
		Upload   int64    `json:"upload"`
		Download int64    `json:"download"`
		Rule     string   `json:"rule"`
		Chains   []string `json:"chains"`
		Start    string   `json:"start"`
	} `json:"connections"`
}

// Connections returns the live connections reported by the Clash API.
func (m *Manager) Connections() ([]Connection, Traffic, error) {
	addr := m.ClashAPIAddr()
	if addr == "" {
		return nil, Traffic{Available: false, Error: "未配置 Clash API，无法读取连接信息"}, nil
	}
	if !clashReachable(addr) {
		return nil, Traffic{Available: false, Error: "Clash API 未监听 " + addr}, nil
	}
	body, err := m.clashGet(addr, "/connections")
	if err != nil {
		return nil, Traffic{Available: false, Error: err.Error()}, nil
	}
	var snapshot clashSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil, Traffic{Available: false, Error: "解析 Clash API 响应失败：" + err.Error()}, nil
	}
	traffic := Traffic{
		UploadTotal:   snapshot.UploadTotal,
		DownloadTotal: snapshot.DownloadTotal,
		Connections:   len(snapshot.Connections),
		Available:     true,
		At:            time.Now().Unix(),
	}
	connections := make([]Connection, 0, len(snapshot.Connections))
	for _, item := range snapshot.Connections {
		meta := item.Metadata
		source := joinHostPort(meta.SourceIP, meta.SourcePort)
		target := joinHostPort(meta.DestinationIP, meta.DestinationPort)
		if meta.Host != "" {
			if meta.DestinationPort != "" {
				target = net.JoinHostPort(meta.Host, meta.DestinationPort)
			} else {
				target = meta.Host
			}
		}
		inbound := ""
		outbound := ""
		if len(item.Chains) > 0 {
			outbound = item.Chains[0]
		}
		if len(item.Chains) > 1 {
			inbound = item.Chains[len(item.Chains)-1]
		}
		connections = append(connections, Connection{
			ID:        item.ID,
			Network:   meta.Network,
			Type:      meta.Type,
			Source:    source,
			Target:    target,
			Inbound:   inbound,
			Outbound:  outbound,
			Rule:      item.Rule,
			Process:   meta.ProcessPath,
			StartedAt: item.Start,
			Upload:    item.Upload,
			Download:  item.Download,
		})
	}
	return connections, traffic, nil
}

func joinHostPort(host, port string) string {
	if host == "" {
		return ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// clashReachable checks the Clash API socket before issuing an HTTP request, so
// a disabled API does not cost a dial timeout on every page load.
func clashReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (m *Manager) clashGet(addr, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "http://" + addr + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if secret := m.clashSecret(); secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Clash API 失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Clash API 返回 %d", resp.StatusCode)
	}
	return body, nil
}

// clashSecret reads the optional Clash API secret from the main config.
func (m *Manager) clashSecret() string {
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
	return asString(asMap(experimental["clash_api"])["secret"])
}

var _ = strings.TrimSpace
