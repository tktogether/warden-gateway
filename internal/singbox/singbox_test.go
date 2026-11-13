package singbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	dataDir := t.TempDir()
	m := New(Options{Root: root, DataDir: dataDir})
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o700); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	main := `{"log":{"level":"info"},"outbounds":[{"tag":"direct","type":"direct"}]}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(main), 0o600); err != nil {
		t.Fatalf("write main config: %v", err)
	}
	return m, root
}

func writeConf(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "conf", name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

const hysteriaBody = `{
  "inbounds": [
    {
      "type": "hysteria2",
      "tag": "Hysteria2-12538",
      "listen": "::",
      "listen_port": 12538,
      "users": [{"password": "6c23c651-5d74-4af6-a4f8-67069aa916fe"}],
      "tls": {
        "enabled": true,
        "alpn": ["h3"],
        "key_path": "/etc/sing-box/bin/tls.key",
        "certificate_path": "/etc/sing-box/bin/tls.cer"
      }
    }
  ]
}`

const socksBody = `{
  "inbounds": [
    {
      "type": "socks",
      "tag": "Socks-21621",
      "listen": "::",
      "listen_port": 21621,
      "users": [{"username": "akchan", "password": "tktogether"}]
    }
  ]
}`

func TestParseNodeReadsHysteria2(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	nodes, err := m.Nodes()
	if err != nil {
		t.Fatalf("nodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(nodes))
	}
	node := nodes[0]
	if node.ID != "Hysteria2-12538" || node.Type != "hysteria2" || node.Port != 12538 {
		t.Fatalf("unexpected node: %#v", node)
	}
	if !node.Enabled || node.TLSType != "tls" || node.TLS == nil || node.TLS.Reality {
		t.Fatalf("unexpected tls info: %#v", node.TLS)
	}
	if len(node.Users) != 1 || node.Users[0].Password == "" {
		t.Fatalf("user password not parsed: %#v", node.Users)
	}
}

func TestParseNodeReadsRealityPublicKey(t *testing.T) {
	m, root := newTestManager(t)
	priv, pub, err := RealityKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	body := `{
  "inbounds": [
    {
      "type": "vless",
      "tag": "Reality-4430",
      "listen": "::",
      "listen_port": 4430,
      "users": [{"uuid": "11111111-2222-3333-4444-555555555555", "flow": "xtls-rprx-vision"}],
      "tls": {
        "enabled": true,
        "server_name": "www.microsoft.com",
        "reality": {
          "enabled": true,
          "handshake": {"server": "www.microsoft.com", "server_port": 443},
          "private_key": "` + priv + `",
          "short_id": ["a1b2c3d4"]
        }
      }
    }
  ]
}`
	writeConf(t, root, "Reality-4430.json", body)
	node, err := m.Find("Reality-4430")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if node.TLSType != "reality" || node.TLS == nil || !node.TLS.Reality {
		t.Fatalf("reality not detected: %#v", node)
	}
	if node.TLS.PublicKey != pub {
		t.Fatalf("public key mismatch: %q want %q", node.TLS.PublicKey, pub)
	}
	if node.TLS.ShortID != "a1b2c3d4" || node.Users[0].Flow != "xtls-rprx-vision" {
		t.Fatalf("reality details lost: %#v", node)
	}
}

func TestDisabledNodeKeepsCleanID(t *testing.T) {
	restore := restartHook
	restartHook = func(*Manager) error { return nil }
	defer func() { restartHook = restore }()

	m, root := newTestManager(t)
	writeConf(t, root, "Socks-21621.json.off", socksBody)
	nodes, err := m.Nodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %v %#v", err, nodes)
	}
	if nodes[0].ID != "Socks-21621" || nodes[0].Enabled {
		t.Fatalf("disabled node id: %#v", nodes[0])
	}
	enabled, err := m.SetEnabled("Socks-21621", true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !enabled.Enabled || enabled.File != "Socks-21621.json" {
		t.Fatalf("enable result: %#v", enabled)
	}
	if _, statErr := os.Stat(filepath.Join(root, "conf", "Socks-21621.json.off")); !os.IsNotExist(statErr) {
		t.Fatalf("parked file still present")
	}
	parked, err := m.SetEnabled("Socks-21621", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if parked.Enabled || parked.File != "Socks-21621.json.off" {
		t.Fatalf("disable result: %#v", parked)
	}
}

func TestDeleteArchivesNode(t *testing.T) {
	restore := restartHook
	restartHook = func(*Manager) error { return nil }
	defer func() { restartHook = restore }()

	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	if err := m.Delete("Hysteria2-12538"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "conf", "Hysteria2-12538.json")); !os.IsNotExist(err) {
		t.Fatalf("file not removed")
	}
	backups := m.Backups()
	if len(backups) != 1 || !strings.HasSuffix(backups[0].Name, "Hysteria2-12538.json") {
		t.Fatalf("backup missing: %#v", backups)
	}
	text, err := m.BackupText(backups[0].Name)
	if err != nil || !strings.Contains(text, "hysteria2") {
		t.Fatalf("backup content: %v %q", err, text)
	}
}

func TestSaveConfigRejectsBrokenJSON(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	if _, err := m.SaveConfig("Hysteria2-12538", `{"inbounds": [}`); err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Fatalf("want json error, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "conf", "Hysteria2-12538.json"))
	if err != nil || string(after) != hysteriaBody {
		t.Fatalf("config changed after rejected save")
	}
}

func TestSaveConfigWritesValidJSON(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	next := strings.Replace(hysteriaBody, "\"listen_port\": 12538", "\"listen_port\": 12539", 1)
	check, err := m.SaveConfig("Hysteria2-12538", next)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !check.OK {
		t.Fatalf("check not ok: %#v", check)
	}
	node, err := m.Find("Hysteria2-12538")
	if err != nil || node.Port != 12539 {
		t.Fatalf("port not updated: %#v %v", node, err)
	}
}

func TestCreateRejectsUnknownTypeAndMissingBinary(t *testing.T) {
	m, _ := newTestManager(t)
	if _, _, err := m.Create(CreateRequest{Type: "wireguard"}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("want unsupported type error, got %v", err)
	}
	if _, _, err := m.Create(CreateRequest{Type: "trojan"}); err == nil || !strings.Contains(err.Error(), "未安装") {
		t.Fatalf("want install error, got %v", err)
	}
}

func TestBuildInboundGeneratesCredentials(t *testing.T) {
	m, root := newTestManager(t)
	for _, id := range []string{"hysteria2", "vless-reality", "trojan", "shadowsocks", "socks"} {
		def, ok := nodeType(id)
		if !ok {
			t.Fatalf("missing type %s", id)
		}
		doc, err := m.buildInbound(def, "node-"+id, 12345, "gateway.example.com", "")
		if err != nil {
			t.Fatalf("build %s: %v", id, err)
		}
		inbounds := asSlice(doc["inbounds"])
		if len(inbounds) != 1 {
			t.Fatalf("%s: want one inbound", id)
		}
		inbound := asMap(inbounds[0])
		if asInt(inbound["listen_port"]) != 12345 || asString(inbound["tag"]) != "node-"+id {
			t.Fatalf("%s: bad meta %#v", id, inbound)
		}
		switch id {
		case "hysteria2", "trojan":
			users := asSlice(inbound["users"])
			if len(users) != 1 || len(asString(asMap(users[0])["password"])) < 16 {
				t.Fatalf("%s: weak password %#v", id, users)
			}
			tls := asMap(inbound["tls"])
			if !asBool(tls["enabled"]) {
				t.Fatalf("%s: tls disabled", id)
			}
			keyPath := asString(tls["key_path"])
			if _, statErr := os.Stat(keyPath); statErr != nil {
				t.Fatalf("%s: cert missing: %v", id, statErr)
			}
			if !strings.HasPrefix(keyPath, root) {
				t.Fatalf("%s: cert written outside root: %s", id, keyPath)
			}
		case "vless-reality":
			user := asMap(asSlice(inbound["users"])[0])
			if len(asString(user["uuid"])) != 36 {
				t.Fatalf("bad uuid %#v", user)
			}
			reality := asMap(asMap(inbound["tls"])["reality"])
			if len(asString(reality["private_key"])) != 43 {
				t.Fatalf("bad reality key %#v", reality)
			}
			if ids := asSlice(reality["short_id"]); len(ids) != 1 || len(asString(ids[0])) != 8 {
				t.Fatalf("bad short id %#v", reality)
			}
		case "shadowsocks":
			key := asString(inbound["password"])
			raw, err := base64.StdEncoding.DecodeString(key)
			if err != nil || len(raw) != 16 {
				t.Fatalf("bad ss key %q %v", key, err)
			}
		case "socks":
			user := asMap(asSlice(inbound["users"])[0])
			if asString(user["username"]) != "warden" || len(asString(user["password"])) < 12 {
				t.Fatalf("bad socks user %#v", user)
			}
		}
	}
}

func TestResetRotatesCredentials(t *testing.T) {
	restore := restartHook
	restartHook = func(*Manager) error { return nil }
	defer func() { restartHook = restore }()

	m, root := newTestManager(t)
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	before, err := m.Find("Hysteria2-12538")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	after, err := m.Reset("Hysteria2-12538")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if after.Users[0].Password == before.Users[0].Password {
		t.Fatalf("password not rotated")
	}
	if after.Port != before.Port || after.Type != before.Type {
		t.Fatalf("node identity changed: %#v", after)
	}
}

func TestShareLinksFormats(t *testing.T) {
	m, root := newTestManager(t)
	m.SetHost("gateway.example.com")
	writeConf(t, root, "Hysteria2-12538.json", hysteriaBody)
	writeConf(t, root, "Socks-21621.json", socksBody)
	writeConf(t, root, "SS-18388.json", `{"inbounds":[{"type":"shadowsocks","tag":"SS","listen_port":18388,"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA"}]}`)
	writeConf(t, root, "Trojan-18443.json", `{"inbounds":[{"type":"trojan","tag":"TJ","listen_port":18443,"users":[{"password":"secret-pass"}],"tls":{"enabled":true,"server_name":"gateway.example.com"}}]}`)
	priv, pub, err := RealityKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	reality := `{"inbounds":[{"type":"vless","tag":"RL","listen_port":4430,"users":[{"uuid":"11111111-2222-3333-4444-555555555555","flow":"xtls-rprx-vision"}],"tls":{"enabled":true,"server_name":"www.microsoft.com","reality":{"enabled":true,"handshake":{"server":"www.microsoft.com","server_port":443},"private_key":"` + priv + `","short_id":["a1b2c3d4"]}}}]}`
	writeConf(t, root, "RL-4430.json", reality)

	cases := []struct {
		id     string
		prefix string
		parts  []string
	}{
		{"Hysteria2-12538", "hysteria2://", []string{"@gateway.example.com:12538", "sni=gateway.example.com", "insecure=1"}},
		{"Socks-21621", "socks://", []string{"akchan", "@gateway.example.com:21621"}},
		{"SS-18388", "ss://", []string{"@gateway.example.com:18388"}},
		{"Trojan-18443", "trojan://", []string{"@gateway.example.com:18443", "security=tls"}},
		{"RL-4430", "vless://", []string{"11111111-2222-3333-4444-555555555555@gateway.example.com:4430", "security=reality", "pbk=" + pub, "sid=a1b2c3d4"}},
	}
	for _, tc := range cases {
		_, links, err := m.ShareLinks(tc.id, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		if len(links) != 1 {
			t.Fatalf("%s: want one link, got %d", tc.id, len(links))
		}
		uri := links[0].URI
		if !strings.HasPrefix(uri, tc.prefix) {
			t.Fatalf("%s: unexpected scheme %q", tc.id, uri)
		}
		for _, part := range tc.parts {
			if !strings.Contains(uri, part) {
				t.Fatalf("%s: link %q missing %q", tc.id, uri, part)
			}
		}
	}
}

func TestShareLinksHonoursFallbackHost(t *testing.T) {
	m, root := newTestManager(t)
	writeConf(t, root, "Socks-21621.json", socksBody)
	_, links, err := m.ShareLinks("Socks-21621", "203.0.113.9")
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !strings.Contains(links[0].URI, "203.0.113.9:21621") {
		t.Fatalf("fallback host not used: %q", links[0].URI)
	}
}

func TestQRCodePNG(t *testing.T) {
	png, err := QRCodePNG("socks://warden:pass@127.0.0.1:1080#node", 240)
	if err != nil {
		t.Fatalf("qr: %v", err)
	}
	if len(png) < 100 {
		t.Fatalf("png too small: %d", len(png))
	}
	if !bytes.HasPrefix(png, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("not a png")
	}
	if _, err := QRCodePNG(strings.Repeat("a", maxQRText+1), 240); err == nil {
		t.Fatalf("want error for oversized text")
	}
}

func TestClashAPIAddrFromConfig(t *testing.T) {
	m, root := newTestManager(t)
	main := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9091","secret":"token"}}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(main), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := m.ClashAPIAddr(); got != "127.0.0.1:9091" {
		t.Fatalf("addr = %q", got)
	}
	if got := m.clashSecret(); got != "token" {
		t.Fatalf("secret = %q", got)
	}
	plain := `{"outbounds":[]}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(plain), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := m.ClashAPIAddr(); got != "" {
		t.Fatalf("want empty addr, got %q", got)
	}
}

func TestNormalizeNameAndValidateName(t *testing.T) {
	if got := NormalizeName("  手机 / 家里  "); got != "手机-家里" {
		t.Fatalf("normalize = %q", got)
	}
	long := strings.Repeat("a", 40)
	if got := NormalizeName(long); len([]rune(got)) != 32 {
		t.Fatalf("normalize length = %d", len([]rune(got)))
	}
	for _, bad := range []string{"", "a/b", "a\b", "..", "a\x01b", strings.Repeat("a", 65)} {
		if ValidateName(bad) {
			t.Fatalf("ValidateName(%q) = true", bad)
		}
	}
	if !ValidateName("Hysteria2-12538") {
		t.Fatalf("valid name rejected")
	}
}

func TestPickPortSkipsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	m, _ := newTestManager(t)
	got, err := m.pickPort(busy, map[int]bool{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got == busy {
		t.Fatalf("pickPort returned busy port %d", busy)
	}
	used := map[int]bool{busy + 1: true}
	next, err := m.pickPort(busy, used)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if next == busy || next == busy+1 {
		t.Fatalf("pickPort ignored used set: %d", next)
	}
}

func TestNodeTypesDefaultPortsUnique(t *testing.T) {
	seen := map[int]string{}
	for _, def := range NodeTypes() {
		if def.DefaultPort == 0 || def.ID == "" || def.Label == "" {
			t.Fatalf("incomplete type %#v", def)
		}
		if other, ok := seen[def.DefaultPort]; ok {
			t.Fatalf("%s and %s share port %d", def.ID, other, def.DefaultPort)
		}
		seen[def.DefaultPort] = def.ID
	}
}

func TestMarshalNodeDocRoundsTrips(t *testing.T) {
	out, err := MarshalNodeDoc(`{"inbounds":[{"type":"socks"}]}`)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output has no trailing newline")
	}
}

func TestLooksLikeShellScriptDetectsWrapper(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec /etc/sing-box/bin/sing-box \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if !looksLikeShellScript(wrapper) {
		t.Fatalf("shell wrapper not detected")
	}
	binary := filepath.Join(dir, "real")
	if err := os.WriteFile(binary, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if looksLikeShellScript(binary) {
		t.Fatalf("real binary misdetected as shell script")
	}
	if looksLikeShellScript(filepath.Join(dir, "missing")) {
		t.Fatalf("missing file misdetected as shell script")
	}
}
