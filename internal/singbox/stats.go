package singbox

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultClashController is the loopback address the panel asks sing-box to
// expose its Clash API on. Keeping it on 127.0.0.1 means the API that carries
// connection metadata is never reachable from outside the host.
const DefaultClashController = "127.0.0.1:19090"

// StatsStatus reports whether the main configuration exposes the Clash API the
// panel reads live connections and traffic from.
type StatsStatus struct {
	Enabled    bool   `json:"enabled"`
	Controller string `json:"controller,omitempty"`
	Secret     bool   `json:"secret"`
}

// StatsStatus inspects the main configuration without changing it.
func (m *Manager) StatsStatus() StatsStatus {
	doc, _, err := m.mainConfigDoc()
	if err != nil {
		return StatsStatus{}
	}
	clash := asMap(asMap(doc["experimental"])["clash_api"])
	controller := strings.TrimSpace(asString(clash["external_controller"]))
	if controller == "" {
		return StatsStatus{}
	}
	return StatsStatus{
		Enabled:    true,
		Controller: controller,
		Secret:     strings.TrimSpace(asString(clash["secret"])) != "",
	}
}

// EnableStats writes experimental.clash_api into the main configuration,
// validates it and restarts sing-box so the live connection view works without
// the operator editing files. The previous bytes are restored when validation
// or the restart fails, which keeps the service on its known-good config.
func (m *Manager) EnableStats(controller string) (StatsStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	controller = strings.TrimSpace(controller)
	if controller == "" {
		controller = DefaultClashController
	}
	host, port, err := net.SplitHostPort(controller)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return StatsStatus{}, fmt.Errorf("Clash API 监听地址不合法：%s", controller)
	}
	doc, previous, err := m.mainConfigDoc()
	if err != nil {
		return StatsStatus{}, err
	}
	experimental := asMap(doc["experimental"])
	if experimental == nil {
		experimental = map[string]interface{}{}
	}
	clash := asMap(experimental["clash_api"])
	if clash == nil {
		clash = map[string]interface{}{}
	}
	clash["external_controller"] = controller
	secret := strings.TrimSpace(asString(clash["secret"]))
	if secret == "" {
		generated, genErr := RandomHex(16)
		if genErr != nil {
			return StatsStatus{}, fmt.Errorf("生成 Clash API 密钥失败：%w", genErr)
		}
		secret = generated
	}
	clash["secret"] = secret
	experimental["clash_api"] = clash
	doc["experimental"] = experimental
	if _, err := m.replaceMainConfig(doc, previous); err != nil {
		return StatsStatus{}, err
	}
	if err := m.reloadUnit(); err != nil {
		m.restoreMainConfig(previous)
		return StatsStatus{}, err
	}
	return m.StatsStatus(), nil
}

// DisableStats removes the Clash API block again and restarts sing-box.
func (m *Manager) DisableStats() (StatsStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, previous, err := m.mainConfigDoc()
	if err != nil {
		return StatsStatus{}, err
	}
	experimental := asMap(doc["experimental"])
	if experimental == nil || asMap(experimental["clash_api"]) == nil {
		return StatsStatus{}, nil
	}
	delete(experimental, "clash_api")
	if len(experimental) == 0 {
		delete(doc, "experimental")
	} else {
		doc["experimental"] = experimental
	}
	if _, err := m.replaceMainConfig(doc, previous); err != nil {
		return StatsStatus{}, err
	}
	if err := m.reloadUnit(); err != nil {
		m.restoreMainConfig(previous)
		return StatsStatus{}, err
	}
	return m.StatsStatus(), nil
}

// mainConfigDoc reads and decodes the main configuration together with the raw
// bytes needed to undo a change.
func (m *Manager) mainConfigDoc() (map[string]interface{}, []byte, error) {
	raw, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("读取主配置失败：%w", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("主配置不是合法 JSON：%w", err)
	}
	return doc, raw, nil
}

func (m *Manager) configBackupDir() string {
	return filepath.Join(m.dataDir, "singbox-config-backups")
}

// replaceMainConfig archives the previous file, writes the merged document and
// validates it, restoring the previous bytes when sing-box rejects the result.
func (m *Manager) replaceMainConfig(doc map[string]interface{}, previous []byte) (Check, error) {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(m.configPath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(m.configBackupDir(), 0o700); err != nil {
		return Check{}, fmt.Errorf("创建备份目录失败：%w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backup := filepath.Join(m.configBackupDir(), stamp+"-config.json")
	if err := os.WriteFile(backup, previous, 0o600); err != nil {
		return Check{}, fmt.Errorf("备份主配置失败：%w", err)
	}
	m.pruneConfigBackups()
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Check{}, fmt.Errorf("编码主配置失败：%w", err)
	}
	body = append(body, byte(10))
	if err := writeAtomic(m.configPath, body, mode); err != nil {
		return Check{}, err
	}
	check := m.checkSoft()
	if !check.OK {
		m.restoreMainConfig(previous)
		return check, fmt.Errorf("配置校验失败，已恢复原配置：%s", check.Output)
	}
	return check, nil
}

// restoreMainConfig puts the previous main configuration back and restarts the
// unit so the running process matches the file again.
func (m *Manager) restoreMainConfig(previous []byte) {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(m.configPath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(m.configPath, previous, mode); err != nil {
		m.log.Errorf("singbox: restore main config: %v", err)
		return
	}
	if err := m.reloadUnit(); err != nil {
		m.log.Errorf("singbox: restart after main config restore: %v", err)
	}
}

// pruneConfigBackups keeps the newest ten archived main configurations.
func (m *Manager) pruneConfigBackups() {
	entries, err := os.ReadDir(m.configBackupDir())
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) <= 10 {
		return
	}
	sort.Strings(names)
	for _, stale := range names[:len(names)-10] {
		_ = os.Remove(filepath.Join(m.configBackupDir(), stale))
	}
}
