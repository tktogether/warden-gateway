// Package cn applies and restores China mainland network settings on the host
// running Warden Gateway: APT mirror, BBR, Docker registry mirror, Git replace
// rules, /etc/hosts entries and the client resolver state.
package cn

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultBackupRoot stores configuration snapshots before every change.
	DefaultBackupRoot = "/var/backups/warden-gateway-cn"

	hostsBegin = "# >>> warden-gateway cn >>>"
	hostsEnd   = "# <<< warden-gateway cn <<<"

	pathHosts        = "/etc/hosts"
	pathAPTList      = "/etc/apt/sources.list"
	pathAPTDir       = "/etc/apt/sources.list.d"
	pathBBR          = "/etc/sysctl.d/99-warden-cn-bbr.conf"
	pathDocker       = "/etc/docker/daemon.json"
	pathGitConfig    = "/etc/gitconfig"
	pathResolv       = "/etc/resolv.conf"
	pathResolvedConf = "/etc/systemd/resolved.conf.d/warden-cn.conf"
	pathTCPCC        = "/proc/sys/net/ipv4/tcp_congestion_control"
	pathQDisc        = "/proc/sys/net/core/default_qdisc"

	maxBackups = 10
)

// Options selects what to apply. Disabled items are not touched.
type Options struct {
	APTMirror    string   `json:"apt_mirror"`
	DockerMirror string   `json:"docker_mirror"`
	GitMirror    string   `json:"git_mirror"`
	GatewayIP    string   `json:"gateway_ip"`
	Domains      []string `json:"domains"`

	// Mode selects the redirect method: hosts, dns, both or none. When set
	// it overrides Hosts and DNS; empty keeps the flags as given.
	Mode string `json:"mode,omitempty"`

	Hosts     bool `json:"hosts"`
	DNS       bool `json:"dns"`
	APT       bool `json:"apt"`
	BBR       bool `json:"bbr"`
	Docker    bool `json:"docker"`
	Git       bool `json:"git"`
	APTUpdate bool `json:"apt_update"`
	NoBackup  bool `json:"no_backup"`
}

// Manager applies settings. Root prefixes filesystem paths and is empty on a
// real host. Exec runs system commands and is replaceable in tests.
type Manager struct {
	Root       string
	BackupRoot string
	Exec       func(name string, args ...string) (string, error)
}

// FileRecord records one file inside a snapshot.
type FileRecord struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Size    int64  `json:"size"`
}

// Manifest is stored next to every snapshot.
type Manifest struct {
	ID        string       `json:"id"`
	Created   string       `json:"created"`
	CreatedTS int64        `json:"created_ts"`
	Files     []FileRecord `json:"files"`
}

// BackupInfo describes one snapshot.
type BackupInfo struct {
	ID        string `json:"id"`
	Created   string `json:"created"`
	CreatedTS int64  `json:"created_ts"`
	Files     int    `json:"files"`
}

// Step is one applied or restored item.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is returned by Apply and Restore.
type Report struct {
	AsOf     string   `json:"as_of"`
	BackupID string   `json:"backup_id,omitempty"`
	Steps    []Step   `json:"steps"`
	Reloaded []string `json:"reloaded,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// APTStatus summarizes the APT mirror state.
type APTStatus struct {
	Hosts    []string `json:"hosts"`
	Mirrored bool     `json:"mirrored"`
}

// BBRStatus summarizes congestion control state.
type BBRStatus struct {
	Congestion string `json:"congestion"`
	QDisc      string `json:"qdisc"`
	Configured bool   `json:"configured"`
}

// DockerStatus summarizes the registry mirror state.
type DockerStatus struct {
	Installed bool     `json:"installed"`
	Mirrors   []string `json:"mirrors"`
}

// GitStatus summarizes Git replace rules.
type GitStatus struct {
	Mirrors []string `json:"mirrors"`
}

// HostsStatus summarizes managed hosts entries.
type HostsStatus struct {
	Managed bool     `json:"managed"`
	IP      string   `json:"ip"`
	Domains []string `json:"domains"`
}

// DNSStatus summarizes the resolver state.
type DNSStatus struct {
	Nameservers []string `json:"nameservers"`
	Managed     string   `json:"managed,omitempty"`
	GatewayOnly bool     `json:"gateway_only"`
	// SystemdResolved reports a drop-in written for systemd-resolved.
	SystemdResolved bool   `json:"systemd_resolved"`
	ResolvedDNS     string `json:"resolved_dns,omitempty"`
}

// Status is the read-only view used by the panel and by diagnostics.
type Status struct {
	AsOf         string       `json:"as_of"`
	BackupRoot   string       `json:"backup_root"`
	Backups      []BackupInfo `json:"backups"`
	LatestBackup string       `json:"latest_backup"`
	APT          APTStatus    `json:"apt"`
	BBR          BBRStatus    `json:"bbr"`
	Docker       DockerStatus `json:"docker"`
	Git          GitStatus    `json:"git"`
	Hosts        HostsStatus  `json:"hosts"`
	DNS          DNSStatus    `json:"dns"`
}

// Mode reports which redirect method is configured on this machine.
func (st Status) Mode() string {
	hosts := st.Hosts.Managed
	dns := st.DNS.Managed != "" || (st.DNS.SystemdResolved && st.DNS.ResolvedDNS != "")
	switch {
	case hosts && dns:
		return "both"
	case hosts:
		return "hosts"
	case dns:
		return "dns"
	default:
		return "none"
	}
}

// New returns a Manager bound to the local filesystem.
func New() *Manager {
	return &Manager{BackupRoot: DefaultBackupRoot}
}

func (m *Manager) fs(path string) string {
	if m.Root != "" && m.Root != "/" {
		return filepath.Join(m.Root, path)
	}
	return path
}

func (m *Manager) backupRoot() string {
	if m.BackupRoot != "" {
		return m.BackupRoot
	}
	return DefaultBackupRoot
}

// DetectIP returns the configured gateway address when set, otherwise the first
// non-loopback nameserver address.
func (m *Manager) DetectIP() string {
	data, ok := m.readFile(pathResolv)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil || ip.IsLoopback() {
			continue
		}
		return fields[1]
	}
	return ""
}

func (m *Manager) readFile(path string) (string, bool) {
	data, err := os.ReadFile(m.fs(path))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Status reads the current state without modifying anything.
func (m *Manager) Status() (Status, error) {
	st := Status{AsOf: time.Now().UTC().Format(time.RFC3339), BackupRoot: m.backupRoot()}
	backups, err := m.List()
	if err != nil {
		return st, err
	}
	st.Backups = backups
	if len(backups) > 0 {
		st.LatestBackup = backups[0].ID
	}
	st.APT = m.aptStatus()
	st.BBR = m.bbrStatus()
	st.Docker = m.dockerStatus()
	st.Git = m.gitStatus()
	st.Hosts = m.hostsStatus()
	st.DNS = m.dnsStatus()
	return st, nil
}

var urlHostRe = regexp.MustCompile("https?://([^/\\s]+)")

func (m *Manager) aptStatus() APTStatus {
	st := APTStatus{Hosts: []string{}}
	seen := map[string]bool{}
	files := []string{pathAPTList}
	entries, err := os.ReadDir(m.fs(pathAPTDir))
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".list") && !strings.HasSuffix(name, ".sources") {
				continue
			}
			files = append(files, filepath.Join(pathAPTDir, name))
		}
	}
	for _, f := range files {
		data, ok := m.readFile(f)
		if !ok {
			continue
		}
		for _, match := range urlHostRe.FindAllStringSubmatch(data, -1) {
			host := match[1]
			if seen[host] {
				continue
			}
			seen[host] = true
			st.Hosts = append(st.Hosts, host)
		}
	}
	sort.Strings(st.Hosts)
	for _, host := range st.Hosts {
		if strings.Contains(host, "aliyun") || strings.Contains(host, "tuna") || strings.Contains(host, "ustc") {
			st.Mirrored = true
		}
	}
	return st
}

func (m *Manager) bbrStatus() BBRStatus {
	st := BBRStatus{}
	if data, ok := m.readFile(pathTCPCC); ok {
		st.Congestion = strings.TrimSpace(data)
	}
	if data, ok := m.readFile(pathQDisc); ok {
		st.QDisc = strings.TrimSpace(data)
	}
	if _, ok := m.readFile(pathBBR); ok {
		st.Configured = true
	}
	return st
}

func (m *Manager) dockerStatus() DockerStatus {
	st := DockerStatus{Mirrors: []string{}}
	data, ok := m.readFile(pathDocker)
	if ok {
		st.Installed = true
		var raw struct {
			Mirrors []string `json:"registry-mirrors"`
		}
		if err := json.Unmarshal([]byte(data), &raw); err == nil {
			st.Mirrors = append(st.Mirrors, raw.Mirrors...)
		}
		return st
	}
	if _, err := os.Stat(m.fs("/var/run/docker.sock")); err == nil {
		st.Installed = true
		return st
	}
	if _, err := os.Stat(m.fs("/usr/bin/docker")); err == nil {
		st.Installed = true
	}
	return st
}

func (m *Manager) gitStatus() GitStatus {
	st := GitStatus{Mirrors: []string{}}
	data, ok := m.readFile(pathGitConfig)
	if !ok {
		return st
	}
	seen := map[string]bool{}
	add := func(section string) {
		section = strings.TrimSpace(section)
		section = strings.TrimPrefix(section, "url.")
		if !strings.HasSuffix(section, "/https://github.com/") {
			return
		}
		mirror := strings.TrimSuffix(section, "/https://github.com/")
		if mirror == "" || seen[mirror] {
			return
		}
		seen[mirror] = true
		st.Mirrors = append(st.Mirrors, mirror)
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[url \"") && strings.HasSuffix(line, "]") && strings.HasSuffix(line, "\"]"):
			add(strings.TrimSuffix(strings.TrimPrefix(line, "[url \""), "\"]"))
		case strings.HasPrefix(line, "[url.") && strings.HasSuffix(line, "]"):
			add(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
		}
	}
	return st
}

func (m *Manager) hostsStatus() HostsStatus {
	st := HostsStatus{Domains: []string{}}
	data, ok := m.readFile(pathHosts)
	if !ok {
		return st
	}
	inBlock := false
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == hostsBegin {
			inBlock = true
			st.Managed = true
			continue
		}
		if trimmed == hostsEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		if st.IP == "" {
			st.IP = fields[0]
		}
		st.Domains = append(st.Domains, fields[1:]...)
	}
	return st
}

func (m *Manager) dnsStatus() DNSStatus {
	st := DNSStatus{Nameservers: []string{}}
	if dropIn, ok := m.readFile(pathResolvedConf); ok {
		st.SystemdResolved = true
		for _, raw := range strings.Split(dropIn, "\n") {
			line := strings.TrimSpace(raw)
			if !strings.HasPrefix(line, "DNS=") {
				continue
			}
			if fields := strings.Fields(strings.TrimPrefix(line, "DNS=")); len(fields) > 0 {
				st.ResolvedDNS = strings.TrimSuffix(fields[0], ",")
			}
		}
	}
	data, ok := m.readFile(pathResolv)
	if !ok {
		if st.Managed == "" {
			st.Managed = st.ResolvedDNS
		}
		return st
	}
	managed := false
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == hostsBegin || line == hostsEnd {
			managed = line == hostsBegin
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			st.Nameservers = append(st.Nameservers, fields[1])
			if managed {
				st.Managed = fields[1]
			}
		}
	}
	st.GatewayOnly = len(st.Nameservers) == 1
	if st.Managed == "" {
		st.Managed = st.ResolvedDNS
	}
	return st
}

// List returns snapshots ordered newest first.
func (m *Manager) List() ([]BackupInfo, error) {
	root := m.backupRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, err
	}
	out := make([]BackupInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest, err := m.loadManifest(e.Name())
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{ID: manifest.ID, Created: manifest.Created, CreatedTS: manifest.CreatedTS, Files: len(manifest.Files)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedTS > out[j].CreatedTS })
	return out, nil
}

func (m *Manager) loadManifest(id string) (Manifest, error) {
	var manifest Manifest
	path, err := safeJoin(m.backupRoot(), id, "manifest.json")
	if err != nil {
		return manifest, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// Resolve maps an identifier to a snapshot id; empty or latest picks the newest.
func (m *Manager) Resolve(id string) (string, error) {
	if id != "" && id != "latest" {
		if _, err := m.loadManifest(id); err != nil {
			return "", fmt.Errorf("backup %s not found", id)
		}
		return id, nil
	}
	list, err := m.List()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no backup found in %s", m.backupRoot())
	}
	return list[0].ID, nil
}

func safeJoin(root, name, rest string) (string, error) {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("invalid backup id %q", name)
	}
	return filepath.Join(root, name, rest), nil
}
