package cn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	gitBlockBegin = "# >>> warden-gateway cn git >>>"
	gitBlockEnd   = "# <<< warden-gateway cn git <<<"
)

var commandRunner = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (m *Manager) run(name string, args ...string) (string, error) {
	runner := m.Exec
	if runner == nil {
		runner = commandRunner
	}
	return runner(name, args...)
}

// DefaultDomains returns the hosts and DNS entries used when none are given.
func DefaultDomains() []string {
	return []string{
		"github.com",
		"api.github.com",
		"codeload.github.com",
		"raw.githubusercontent.com",
		"objects.githubusercontent.com",
		"gist.githubusercontent.com",
		"ghcr.io",
		"registry-1.docker.io",
		"auth.docker.io",
		"production.cloudfront.docker.com",
	}
}

// Apply writes the selected settings and reports every step.
func (m *Manager) Apply(opts Options) (Report, error) {
	report := Report{AsOf: time.Now().UTC().Format(time.RFC3339)}
	if opts.GatewayIP != "" && net.ParseIP(opts.GatewayIP) == nil {
		return report, fmt.Errorf("网关地址无效：%s", opts.GatewayIP)
	}
	domains := normalizeDomains(opts.Domains)
	if len(domains) == 0 {
		domains = DefaultDomains()
	}
	if (opts.Hosts || opts.DNS) && opts.GatewayIP == "" {
		return report, fmt.Errorf("未指定网关地址，无法写入 hosts 或 DNS 设置")
	}
	if !opts.NoBackup {
		id, err := m.snapshot(m.pathsFor(opts))
		if err != nil {
			return report, fmt.Errorf("创建备份失败：%w", err)
		}
		report.BackupID = id
	}
	if opts.Hosts {
		report.Steps = append(report.Steps, stepFrom("hosts", m.writeHosts(opts.GatewayIP, domains)))
	}
	if opts.DNS {
		report.Steps = append(report.Steps, stepFrom("dns", m.writeDNS(opts.GatewayIP)))
	}
	if opts.APT {
		mirror := strings.TrimSpace(opts.APTMirror)
		if mirror == "" {
			report.Steps = append(report.Steps, Step{Name: "apt", OK: false, Detail: "未指定 APT 镜像地址"})
		} else if err := m.writeAPTMirror(mirror); err != nil {
			report.Steps = append(report.Steps, stepFrom("apt", err))
		} else {
			report.Steps = append(report.Steps, Step{Name: "apt", OK: true, Detail: "APT 源已替换为 " + mirror})
		}
	}
	if opts.BBR {
		report.Steps = append(report.Steps, stepFrom("bbr", m.applyBBR()))
	}
	if opts.Docker {
		report.Steps = append(report.Steps, stepFrom("docker", m.applyDockerMirror(strings.TrimSpace(opts.DockerMirror))))
		if out, err := m.run("systemctl", "reload", "docker"); err != nil {
			report.Warnings = append(report.Warnings, "docker 未重载："+trimOut(out, err))
		} else {
			report.Reloaded = append(report.Reloaded, "docker")
		}
	}
	if opts.Git {
		report.Steps = append(report.Steps, stepFrom("git", m.applyGitMirror(strings.TrimSpace(opts.GitMirror))))
	}
	if opts.APTUpdate && opts.APT {
		if out, err := m.run("apt-get", "update"); err != nil {
			report.Warnings = append(report.Warnings, "apt-get update 失败："+trimOut(out, err))
		} else {
			report.Reloaded = append(report.Reloaded, "apt")
		}
	}
	return report, nil
}

func stepFrom(name string, err error) Step {
	if err != nil {
		return Step{Name: name, OK: false, Detail: err.Error()}
	}
	return Step{Name: name, OK: true}
}

func trimOut(out string, err error) string {
	text := strings.TrimSpace(out)
	if text == "" && err != nil {
		text = err.Error()
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

func normalizeDomains(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		for _, part := range strings.Split(item, ",") {
			host := strings.ToLower(strings.TrimSpace(part))
			if host == "" || seen[host] {
				continue
			}
			seen[host] = true
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) pathsFor(opts Options) []string {
	paths := make([]string, 0, 12)
	if opts.Hosts || opts.DNS {
		paths = append(paths, pathHosts)
	}
	if opts.DNS {
		paths = append(paths, pathResolv, pathResolvedConf)
	}
	if opts.APT {
		paths = append(paths, m.aptFiles()...)
	}
	if opts.BBR {
		paths = append(paths, pathBBR)
	}
	if opts.Docker {
		paths = append(paths, pathDocker)
	}
	if opts.Git {
		paths = append(paths, pathGitConfig)
	}
	return paths
}

func (m *Manager) aptFiles() []string {
	files := []string{pathAPTList}
	entries, err := os.ReadDir(m.fs(pathAPTDir))
	if err != nil {
		return files
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".list") && !strings.HasSuffix(name, ".sources") {
			continue
		}
		files = append(files, filepath.Join(pathAPTDir, name))
	}
	sort.Strings(files)
	return files
}

func (m *Manager) snapshot(paths []string) (string, error) {
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	root := m.backupRoot()
	id := "cn-" + stamp
	dir := filepath.Join(root, id)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("cn-%s-%d", stamp, i)
		dir = filepath.Join(root, id)
	}
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return "", err
	}
	manifest := Manifest{ID: id, Created: now.Format(time.RFC3339), CreatedTS: now.Unix()}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		record := FileRecord{Path: path}
		data, err := os.ReadFile(m.fs(path))
		if err != nil {
			manifest.Files = append(manifest.Files, record)
			continue
		}
		record.Existed = true
		record.Size = int64(len(data))
		stored := filepath.Join(dir, "files", encodePath(path))
		if err := os.WriteFile(stored, data, 0o600); err != nil {
			return "", err
		}
		manifest.Files = append(manifest.Files, record)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		return "", err
	}
	_ = m.prune()
	return id, nil
}

func encodePath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

func (m *Manager) prune() error {
	list, err := m.List()
	if err != nil || len(list) <= maxBackups {
		return err
	}
	for _, item := range list[maxBackups:] {
		if _, err := safeJoin(m.backupRoot(), item.ID, "manifest.json"); err != nil {
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.backupRoot(), item.ID))
	}
	return nil
}

// Restore puts back every file recorded in a snapshot.
func (m *Manager) Restore(id string) (Report, error) {
	report := Report{AsOf: time.Now().UTC().Format(time.RFC3339)}
	resolved, err := m.Resolve(id)
	if err != nil {
		return report, err
	}
	manifest, err := m.loadManifest(resolved)
	if err != nil {
		return report, err
	}
	report.BackupID = resolved
	base, err := safeJoin(m.backupRoot(), resolved, "files")
	if err != nil {
		return report, err
	}
	for _, record := range manifest.Files {
		target := m.fs(record.Path)
		if !record.Existed {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				report.Steps = append(report.Steps, stepFrom(record.Path, err))
				continue
			}
			report.Steps = append(report.Steps, Step{Name: record.Path, OK: true, Detail: "已删除"})
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, encodePath(record.Path)))
		if err != nil {
			report.Steps = append(report.Steps, stepFrom(record.Path, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			report.Steps = append(report.Steps, stepFrom(record.Path, err))
			continue
		}
		if err := writeAtomic(target, data, 0o644); err != nil {
			report.Steps = append(report.Steps, stepFrom(record.Path, err))
			continue
		}
		report.Steps = append(report.Steps, Step{Name: record.Path, OK: true, Detail: "已恢复"})
	}
	return report, nil
}
