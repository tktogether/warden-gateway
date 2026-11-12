package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeNameStripsConfigSuffix(t *testing.T) {
	cases := map[string]string{
		"Hysteria2-12538.json":     "Hysteria2-12538",
		"Hysteria2-12538.json.off": "Hysteria2-12538",
		"plain":                    "plain",
	}
	for input, want := range cases {
		node := Node{ID: "id", Tag: input}
		if got := node.Name(); got != want {
			t.Fatalf("Name(%q) = %q, want %q", input, got, want)
		}
	}
	if got := (Node{ID: "only-id"}).Name(); got != "only-id" {
		t.Fatalf("fallback name = %q", got)
	}
}

func TestEnableStatsWritesClashAPI(t *testing.T) {
	m, root := newTestManager(t)
	oldHook := restartHook
	restartHook = func(*Manager) error { return nil }
	defer func() { restartHook = oldHook }()

	status, err := m.EnableStats("")
	if err != nil {
		t.Fatalf("enable stats: %v", err)
	}
	if !status.Enabled || status.Controller != DefaultClashController || !status.Secret {
		t.Fatalf("unexpected status: %#v", status)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	clash := asMap(asMap(doc["experimental"])["clash_api"])
	if asString(clash["external_controller"]) != DefaultClashController {
		t.Fatalf("controller not written: %s", string(raw))
	}
	if asString(clash["secret"]) == "" {
		t.Fatalf("secret not generated: %s", string(raw))
	}
	if got := m.ClashAPIAddr(); got != DefaultClashController {
		t.Fatalf("ClashAPIAddr = %q", got)
	}
	// The rest of the main configuration must survive the rewrite.
	if asMap(doc["log"]) == nil || len(asSlice(doc["outbounds"])) == 0 {
		t.Fatalf("existing config lost: %s", string(raw))
	}

	status, err = m.DisableStats()
	if err != nil {
		t.Fatalf("disable stats: %v", err)
	}
	if status.Enabled {
		t.Fatalf("still enabled after disable: %#v", status)
	}
	raw, err = os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "clash_api") {
		t.Fatalf("clash_api still present: %s", string(raw))
	}
	if asMap(asMap(mustDoc(t, raw))["log"]) == nil {
		t.Fatalf("log block lost on disable: %s", string(raw))
	}
}

func TestEnableStatsRejectsBadController(t *testing.T) {
	m, root := newTestManager(t)
	oldHook := restartHook
	restartHook = func(*Manager) error { return nil }
	defer func() { restartHook = oldHook }()
	before, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if _, err := m.EnableStats("not-an-address"); err == nil {
		t.Fatalf("bad controller accepted")
	}
	after, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("config changed on rejected controller")
	}
}

func TestStatsStatusReportsExistingBlock(t *testing.T) {
	m, root := newTestManager(t)
	main := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:19090","secret":"token"}}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(main), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	status := m.StatsStatus()
	if !status.Enabled || status.Controller != "127.0.0.1:19090" || !status.Secret {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func mustDoc(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}
