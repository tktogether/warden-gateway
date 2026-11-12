package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fileEntry is one inbound file on disk before parsing.
type fileEntry struct {
	name     string
	enabled  bool
	path     string
	modified time.Time
}

// inboundFiles lists every inbound file, active or parked, sorted by name.
func (m *Manager) inboundFiles() ([]fileEntry, error) {
	entries, err := os.ReadDir(m.confDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 失败：%w", m.confDir, err)
	}
	var files []fileEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		var enabled bool
		switch {
		case strings.HasSuffix(name, disabledExt):
			enabled = false
		case strings.HasSuffix(name, enabledExt):
			enabled = true
		default:
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		files = append(files, fileEntry{
			name:     name,
			enabled:  enabled,
			path:     filepath.Join(m.confDir, name),
			modified: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// Find returns one node by ID, where the ID is the file name without .json.
func (m *Manager) Find(id string) (Node, error) {
	id = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(id, disabledExt), enabledExt))
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return Node{}, errors.New("节点标识不合法")
	}
	for _, file := range []string{id + enabledExt, id + disabledExt} {
		path := filepath.Join(m.confDir, file)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, statErr := os.Stat(path)
		node := parseNode(file, raw, strings.HasSuffix(file, enabledExt))
		if statErr == nil {
			node.Modified = info.ModTime()
		}
		return node, nil
	}
	return Node{}, fmt.Errorf("未找到节点 %s", id)
}

// Nodes parses every inbound file.
func (m *Manager) Nodes() ([]Node, error) {
	files, err := m.inboundFiles()
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(files))
	for _, file := range files {
		raw, readErr := os.ReadFile(file.path)
		if readErr != nil {
			out = append(out, Node{ID: strings.TrimSuffix(file.name, enabledExt), File: file.name, Enabled: file.enabled, Error: readErr.Error(), Modified: file.modified})
			continue
		}
		node := parseNode(file.name, raw, file.enabled)
		node.Modified = file.modified
		out = append(out, node)
	}
	sortNodes(out)
	return out, nil
}

// Summary gathers everything the panel shows about sing-box.
func (m *Manager) Summary() Summary {
	state, sub := m.ServiceState()
	summary := Summary{
		Installed: m.Installed(),
		Version:   m.Version(),
		Root:      m.root,
		Config:    m.configPath,
		Service:   m.service,
		State:     state,
	}
	if bin, err := m.binaryPath(); err == nil {
		summary.Binary = bin
	}
	summary.Running = state == "active"
	if sub != "" && state != "active" {
		summary.State = state + " (" + sub + ")"
	}
	if nodes, err := m.Nodes(); err == nil {
		summary.Nodes = nodes
	} else {
		summary.Warnings = append(summary.Warnings, err.Error())
	}
	if summary.Installed {
		summary.Check = m.Check()
		if !summary.Check.OK {
			summary.Warnings = append(summary.Warnings, "配置校验未通过："+summary.Check.Output)
		}
	}
	for _, node := range summary.Nodes {
		if node.Error != "" {
			summary.Warnings = append(summary.Warnings, node.File+"："+node.Error)
		}
	}
	return summary
}

// Check runs `sing-box check` against the whole configuration.
func (m *Manager) Check() Check {
	check := Check{Checked: time.Now()}
	bin, err := m.binaryPath()
	if err != nil {
		check.Output = err.Error()
		return check
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	args := []string{"check", "-c", m.configPath}
	if st, statErr := os.Stat(m.confDir); statErr == nil && st.IsDir() {
		args = append(args, "-C", m.confDir)
	}
	started := time.Now()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	check.Duration = time.Since(started)
	check.Output = strings.TrimSpace(string(out))
	check.OK = err == nil
	if err != nil && check.Output == "" {
		check.Output = err.Error()
	}
	return check
}

// checkSoft validates the whole configuration with the sing-box binary. When the
// binary cannot be resolved the panel keeps working: the caller has already
// validated the JSON, and a host without sing-box has no unit to break.
func (m *Manager) checkSoft() Check {
	check := m.Check()
	if !check.OK && !m.Installed() {
		check.OK = true
		check.Output = "未找到 sing-box 可执行文件，已跳过配置校验"
	}
	return check
}

// ConfigText returns the raw JSON of one inbound file.
func (m *Manager) ConfigText(id string) (string, error) {
	node, err := m.Find(id)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(m.confDir, node.File))
	if err != nil {
		return "", fmt.Errorf("读取配置失败：%w", err)
	}
	if len(raw) > maxConfigBytes {
		return "", fmt.Errorf("配置文件超过 %d KB，无法在面板编辑", maxConfigBytes/1024)
	}
	return string(raw), nil
}

// SaveConfig validates and writes one inbound file, restoring the previous
// content when `sing-box check` rejects the new one.
func (m *Manager) SaveConfig(id, text string) (Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, err := m.Find(id)
	if err != nil {
		return Check{}, err
	}
	if len(text) > maxConfigBytes {
		return Check{}, fmt.Errorf("配置超过 %d KB", maxConfigBytes/1024)
	}
	var probe map[string]interface{}
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return Check{}, fmt.Errorf("配置不是合法 JSON：%v", err)
	}
	path := filepath.Join(m.confDir, node.File)
	previous, readErr := os.ReadFile(path)
	if readErr != nil {
		return Check{}, fmt.Errorf("读取原配置失败：%w", readErr)
	}
	if err := writeAtomic(path, []byte(text), 0o600); err != nil {
		return Check{}, err
	}
	check := m.checkSoft()
	if !check.OK {
		if restoreErr := writeAtomic(path, previous, 0o600); restoreErr != nil {
			m.log.Errorf("singbox: restore config after failed check: %v", restoreErr)
		}
		return check, fmt.Errorf("配置校验失败，已恢复原配置：%s", check.Output)
	}
	return check, nil
}

// writeAtomic writes a file through a temporary sibling and a rename.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
