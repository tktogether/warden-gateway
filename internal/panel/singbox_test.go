package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/singbox"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

const testSocksNode = `{"inbounds":[{"type":"socks","tag":"Socks-21621","listen":"::","listen_port":21621,"users":[{"username":"akchan","password":"tktogether"}]}]}`

func newSingboxPanel(t *testing.T) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "sing-box")
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "Socks-21621.json"), []byte(testSocksNode), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.PublicURL = "http://127.0.0.1:18080/warden"
	cfg.Domain = ""
	cfg.TLS.Mode = "self"
	cfg.Listen.HTTP = "127.0.0.1:18080"
	cfg.Listen.HTTPS = "127.0.0.1:18443"
	cfg.Listen.Entry = ""
	cfg.Listen.ACME = ""
	cfg.SNI.Enabled = false
	cfg.SessionSecret = strings.Repeat("s", 48)
	key := "0123456789"
	cfg.AccessKeySHA256 = config.HashAccessKey(key)
	cfg.Backup.Dir = filepath.Join(dir, "backups")
	cfg.SingBox.Enabled = false
	cfg.SingBox.Root = root
	cfg.SingBox.Service = "sing-box.service"
	cfg.SingBox.Host = "gateway.example.com"
	cfg.Normalize()
	cfgPath := filepath.Join(dir, "config.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	// config.Validate 要求 singbox.root 为绝对路径，Windows 临时目录不满足，
	// 保存后再打开，面板只使用内存中的配置。
	cfg.SingBox.Enabled = true
	dataDir := filepath.Join(dir, "data")
	mgr, err := auth.NewManager(cfg.SessionSecret, cfg.AccessKeySHA256, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sb := singbox.New(singbox.Options{Root: root, DataDir: dataDir, Service: "sing-box.service", Host: "gateway.example.com"})
	srv, err := New(Options{
		Config: &cfg, ConfigPath: cfgPath, DataDir: dataDir,
		ServiceName: "warden-gateway.service", Auth: mgr,
		Backups: backup.New(cfg.Backup.Dir, cfgPath, dataDir, filepath.Join(dir, "svc"), 3),
		Stats:   telemetry.NewStore(), Started: time.Now(), SingBox: sb,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, key, root
}

func TestSingboxEndpointExposesNodes(t *testing.T) {
	srv, key, _ := newSingboxPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/singbox", nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var payload struct {
		Installed bool   `json:"installed"`
		Host      string `json:"host"`
		Nodes     []struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Port  int    `json:"port"`
			Links []struct {
				URI  string `json:"uri"`
				User int    `json:"user"`
			} `json:"links"`
		} `json:"nodes"`
		Types []struct {
			ID          string `json:"id"`
			DefaultPort int    `json:"default_port"`
		} `json:"types"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Installed {
		t.Fatalf("installed should be false in test env")
	}
	if payload.Host != "gateway.example.com" {
		t.Fatalf("host = %q", payload.Host)
	}
	if len(payload.Nodes) != 1 || payload.Nodes[0].ID != "Socks-21621" || payload.Nodes[0].Port != 21621 {
		t.Fatalf("nodes = %#v", payload.Nodes)
	}
	links := payload.Nodes[0].Links
	if len(links) != 1 || !strings.HasPrefix(links[0].URI, "socks://") || !strings.Contains(links[0].URI, "gateway.example.com:21621") {
		t.Fatalf("links = %#v", links)
	}
	if len(payload.Types) != 5 {
		t.Fatalf("types = %#v", payload.Types)
	}
}

func TestSingboxConfigReadAndSave(t *testing.T) {
	srv, key, root := newSingboxPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/singbox/config?id=Socks-21621", nil, cookie, csrf)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "listen_port") {
		t.Fatalf("config read %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "POST", "/warden/api/singbox/config", map[string]string{"id": "Socks-21621", "text": "{broken"}, cookie, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("broken json status %d: %s", rec.Code, rec.Body)
	}
	if after, err := os.ReadFile(filepath.Join(root, "conf", "Socks-21621.json")); err != nil || string(after) != testSocksNode {
		t.Fatalf("config changed after rejected save")
	}
	next := strings.Replace(testSocksNode, "21621", "21622", 1)
	rec = do(t, srv, "POST", "/warden/api/singbox/config", map[string]string{"id": "Socks-21621", "text": next}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status %d: %s", rec.Code, rec.Body)
	}
	if _, err := srv.singbox.Find("Socks-21621"); err != nil {
		t.Fatalf("find after save: %v", err)
	}
}

func TestSingboxQREndpoint(t *testing.T) {
	srv, key, _ := newSingboxPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/singbox/qr?id=Socks-21621&user=akchan&size=240", nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("content type %q", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("body is not a png")
	}
}

func TestSingboxRejectsBadInput(t *testing.T) {
	srv, key, _ := newSingboxPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "POST", "/warden/api/singbox/nodes", map[string]interface{}{"type": "wireguard"}, cookie, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "POST", "/warden/api/singbox/action", map[string]string{"id": "Socks-21621", "action": "explode"}, cookie, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("action status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, "GET", "/warden/api/singbox/config?id=..%2Fetc%2Fpasswd", nil, cookie, csrf)
	if rec.Code == http.StatusOK {
		t.Fatalf("path traversal accepted: %s", rec.Body)
	}
}

func TestSingboxPayloadNamesAndStats(t *testing.T) {
	srv, key, root := newSingboxPanel(t)
	legacy := `{"inbounds":[{"type":"hysteria2","tag":"Hysteria2-12538.json","listen_port":12538,"users":[{"password":"6c23c651-5d74-4af6-a4f8-67069aa916fe"}],"tls":{"enabled":true,"server_name":"gateway.example.com"}}]}`
	if err := os.WriteFile(filepath.Join(root, "conf", "Hysteria2-12538.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/singbox", nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var payload struct {
		Nodes []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Links []struct {
				Label string `json:"label"`
			} `json:"links"`
		} `json:"nodes"`
		Stats struct {
			Enabled    bool   `json:"enabled"`
			Controller string `json:"controller"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, node := range payload.Nodes {
		if node.ID != "Hysteria2-12538" {
			continue
		}
		found = true
		if node.Name != "Hysteria2-12538" {
			t.Fatalf("name = %q", node.Name)
		}
		if len(node.Links) != 1 || node.Links[0].Label != "Hysteria2-12538" {
			t.Fatalf("links = %#v", node.Links)
		}
	}
	if !found {
		t.Fatalf("legacy node missing: %#v", payload.Nodes)
	}
	if payload.Stats.Enabled || payload.Stats.Controller != "" {
		t.Fatalf("stats = %#v", payload.Stats)
	}
}

func TestSingboxStatsEnableKeepsConfigOnFailure(t *testing.T) {
	srv, key, root := newSingboxPanel(t)
	cookie, csrf := login(t, srv, key)
	before, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, "POST", "/warden/api/singbox/action", map[string]string{"action": "stats-enable"}, cookie, csrf)
	// 测试机没有 systemd，开启会在重启阶段失败，此时主配置必须回到原样。
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	after, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config not restored: %s", string(after))
	}
}
func TestSingboxDisabledReturns503(t *testing.T) {
	srv, key := newPanel(t)
	cookie, csrf := login(t, srv, key)
	rec := do(t, srv, "GET", "/warden/api/singbox", nil, cookie, csrf)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}
