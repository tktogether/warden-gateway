package cn

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var aptURLRe = regexp.MustCompile("https?://[A-Za-z0-9._:-]+")

func (m *Manager) writeHosts(ip string, domains []string) error {
	path := m.fs(pathHosts)
	original, _ := os.ReadFile(path)
	content := stripBlock(string(original), hostsBegin, hostsEnd)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	block := hostsBegin + "\n" + ip + " " + strings.Join(domains, " ") + "\n" + hostsEnd + "\n"
	return writeAtomic(path, []byte(content+block), 0o644)
}

// writeDNS points the client resolver at the gateway. When resolv.conf is a
// symlink (systemd-resolved) a drop-in is written instead of the target file.
func (m *Manager) writeDNS(ip string) error {
	path := m.fs(pathResolv)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		distros := []string{"/etc/systemd/resolved.conf.d", "/usr/lib/systemd/resolved.conf.d", "/run/systemd/resolved.conf.d"}
		for _, dir := range distros {
			fsDir := m.fs(dir)
			if info, err := os.Stat(fsDir); err != nil || !info.IsDir() {
				continue
			}
			conf := "[Resolve]\nDNS=" + ip + "\nFallbackDNS=\n"
			if err := writeAtomic(filepath.Join(fsDir, "warden-cn.conf"), []byte(conf), 0o644); err != nil {
				return err
			}
			if out, err := m.run("systemctl", "restart", "systemd-resolved"); err != nil {
				return fmt.Errorf("重启 systemd-resolved 失败：%s", trimOut(out, err))
			}
			return nil
		}
		return fmt.Errorf("%s 是符号链接，且未找到 systemd-resolved 配置目录", pathResolv)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s 失败：%w", pathResolv, err)
	}
	content := stripBlock(string(original), hostsBegin, hostsEnd)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	// The managed entry goes first so the local resolver prefers the gateway.
	block := hostsBegin + "\nnameserver " + ip + "\n" + hostsEnd + "\n"
	return writeAtomic(path, []byte(block+content), 0o644)
}

func stripBlock(content, begin, end string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == begin {
			inBlock = true
			continue
		}
		if trimmed == end {
			inBlock = false
			continue
		}
		if inBlock {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (m *Manager) writeAPTMirror(mirror string) error {
	target, err := normalizeMirror(mirror)
	if err != nil {
		return err
	}
	changed := 0
	for _, path := range m.aptFiles() {
		fsPath := m.fs(path)
		data, err := os.ReadFile(fsPath)
		if err != nil {
			continue
		}
		next := aptURLRe.ReplaceAllString(string(data), target)
		if next == string(data) {
			continue
		}
		if err := writeAtomic(fsPath, []byte(next), 0o644); err != nil {
			return err
		}
		changed++
	}
	if changed == 0 {
		return fmt.Errorf("未找到可替换的 APT 源地址")
	}
	return nil
}

func normalizeMirror(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("未指定镜像地址")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("镜像地址无效：%s", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("镜像地址仅支持 http 或 https：%s", raw)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func (m *Manager) applyBBR() error {
	if _, err := m.run("modprobe", "tcp_bbr"); err != nil {
		// The module can be built in; verify the running value below.
		_ = err
	}
	content := "# warden-gateway cn\nnet.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n"
	if err := writeAtomic(m.fs(pathBBR), []byte(content), 0o644); err != nil {
		return err
	}
	if out, err := m.run("sysctl", "--system"); err != nil {
		return fmt.Errorf("应用内核参数失败：%s", trimOut(out, err))
	}
	cc, _ := m.readFile(pathTCPCC)
	if strings.TrimSpace(cc) != "bbr" {
		return fmt.Errorf("内核未启用 bbr，当前为 %q", strings.TrimSpace(cc))
	}
	return nil
}

func (m *Manager) applyDockerMirror(mirror string) error {
	if mirror == "" {
		return fmt.Errorf("未指定 Docker 镜像地址")
	}
	target, err := normalizeMirror(mirror)
	if err != nil {
		return err
	}
	path := m.fs(pathDocker)
	doc := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s 不是合法 JSON：%w", pathDocker, err)
		}
	}
	mirrors, err := json.Marshal([]string{target})
	if err != nil {
		return err
	}
	doc["registry-mirrors"] = mirrors
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, append(data, byte(10)), 0o600)
}

func (m *Manager) applyGitMirror(mirror string) error {
	if mirror == "" {
		return fmt.Errorf("未指定 Git 镜像地址")
	}
	target, err := normalizeMirror(mirror)
	if err != nil {
		return err
	}
	path := m.fs(pathGitConfig)
	original, _ := os.ReadFile(path)
	if m.Root == "" {
		for _, section := range gitMirrorSections(string(original)) {
			if _, err := m.run("git", "config", "--system", "--remove-section", "url."+section); err != nil {
				_ = err
			}
		}
		original, _ = os.ReadFile(path)
	}
	content := stripBlock(string(original), gitBlockBegin, gitBlockEnd)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	lines := []string{gitBlockBegin, "[url \"" + target + "/https://github.com/\"]"}
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		lines = append(lines, "\tinsteadOf = "+prefix)
	}
	lines = append(lines, gitBlockEnd, "")
	return writeAtomic(path, []byte(content+strings.Join(lines, "\n")), 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp-warden-cn"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		// A bind-mounted target cannot be replaced by rename. Writing the
		// existing inode in place keeps the mount intact.
		if writeErr := os.WriteFile(path, data, mode); writeErr != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	return nil
}

// gitMirrorSections lists url sections that already point at GitHub through a
// mirror, so a new mirror replaces them instead of stacking another rewrite.
func gitMirrorSections(content string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[url \"") || !strings.HasSuffix(line, "]") {
			continue
		}
		section := strings.TrimSuffix(strings.TrimPrefix(line, "[url \""), "\"]")
		if !strings.HasSuffix(section, "/https://github.com/") || seen[section] {
			continue
		}
		seen[section] = true
		out = append(out, section)
	}
	return out
}
