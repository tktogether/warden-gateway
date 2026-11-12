package singbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup is one archived inbound file kept before a destructive action.
type Backup struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Delete archives then removes one inbound file, restoring it when the
// remaining configuration no longer validates.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, err := m.Find(id)
	if err != nil {
		return err
	}
	path := filepath.Join(m.confDir, node.File)
	previous, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置失败：%w", err)
	}
	if _, err := m.backupFile(node.File, previous); err != nil {
		m.log.Errorf("singbox: backup before delete failed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除配置失败：%w", err)
	}
	check := m.checkSoft()
	if !check.OK {
		if restoreErr := writeAtomic(path, previous, 0o600); restoreErr != nil {
			m.log.Errorf("singbox: restore after failed delete: %v", restoreErr)
		}
		return fmt.Errorf("删除后配置校验失败，已恢复：%s", check.Output)
	}
	return m.reloadUnit()
}

// SetEnabled activates or parks one node by renaming its file, which is how the
// one-click layout expects nodes to be switched off.
func (m *Manager) SetEnabled(id string, enabled bool) (Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, err := m.Find(id)
	if err != nil {
		if enabled {
			return Node{}, err
		}
		return Node{}, err
	}
	if node.Enabled == enabled {
		return node, nil
	}
	source := filepath.Join(m.confDir, node.File)
	target := filepath.Join(m.confDir, node.ID+enabledExt)
	if !enabled {
		target = filepath.Join(m.confDir, node.ID+disabledExt)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		return Node{}, fmt.Errorf("目标文件 %s 已存在", filepath.Base(target))
	}
	if err := os.Rename(source, target); err != nil {
		return Node{}, fmt.Errorf("切换节点状态失败：%w", err)
	}
	check := m.checkSoft()
	if !check.OK {
		if restoreErr := os.Rename(target, source); restoreErr != nil {
			m.log.Errorf("singbox: rename back after failed check: %v", restoreErr)
		}
		return Node{}, fmt.Errorf("切换后配置校验失败，已恢复：%s", check.Output)
	}
	if err := m.reloadUnit(); err != nil {
		return Node{}, err
	}
	return m.Find(node.ID)
}

// Backups lists the archived inbound files, newest first.
func (m *Manager) Backups() []Backup {
	entries, err := os.ReadDir(m.backupDir())
	if err != nil {
		return nil
	}
	out := make([]Backup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		out = append(out, Backup{Name: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	if len(out) > m.keep {
		for _, stale := range out[m.keep:] {
			_ = os.Remove(filepath.Join(m.backupDir(), stale.Name))
		}
		out = out[:m.keep]
	}
	return out
}

// BackupText returns the archived JSON of one backup file.
func (m *Manager) BackupText(name string) (string, error) {
	if !ValidateName(strings.TrimSuffix(name, ".json")) {
		return "", errors.New("备份名称不合法")
	}
	raw, err := os.ReadFile(filepath.Join(m.backupDir(), name))
	if err != nil {
		return "", fmt.Errorf("读取备份失败：%w", err)
	}
	return string(raw), nil
}

// backupFile stores one inbound file under the data directory with a timestamp
// prefix so a terminal restore is a simple copy back.
func (m *Manager) backupFile(name string, data []byte) (string, error) {
	dir := m.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败：%w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	target := filepath.Join(dir, stamp+"-"+name)
	if err := writeAtomic(target, data, 0o600); err != nil {
		return "", err
	}
	return target, nil
}

// ValidateName reports whether a node ID is safe to use as a file name.
func ValidateName(name string) bool {
	if name == "" || len(name) > 64 || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if r == '/' || r == 92 || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// MarshalNodeDoc re-renders an inbound document with two-space indentation so
// the editor and the writer agree on formatting.
func MarshalNodeDoc(text string) (string, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}
