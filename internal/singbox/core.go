package singbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The panel installs and updates the sing-box kernel itself, so a fresh host
// does not need an out-of-band one-click script. Release layout follows the
// upstream project: sing-box-<version>-<platform>.tar.gz contains a single
// sing-box binary.
const (
	CoreRepo         = "SagerNet/sing-box"
	CoreAPILatest    = "https://api.github.com/repos/" + CoreRepo + "/releases/latest"
	CoreDownloadBase = "https://github.com/" + CoreRepo + "/releases/download"

	coreMaxBytes    = 320 << 20
	coreVersionTTL  = 10 * time.Minute
	coreInstallKeep = 3
)

// coreMirrors returns the download prefixes tried in order. The empty prefix
// is a direct GitHub download; the rest are public mirrors useful where
// github.com is slow.
func coreMirrors() []string {
	if raw := strings.TrimSpace(os.Getenv("WARDEN_SINGBOX_CORE_MIRRORS")); raw != "" {
		out := make([]string, 0, 4)
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !strings.HasSuffix(part, "/") {
				part += "/"
			}
			out = append(out, part)
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"", "https://gh-proxy.com/", "https://ghfast.top/"}
}

// CoreStatus is the kernel state the panel shows: what is installed, what
// upstream offers and whether an update is available.
type CoreStatus struct {
	Installed       bool     `json:"installed"`
	Version         string   `json:"version"`
	Latest          string   `json:"latest"`
	LatestError     string   `json:"latest_error,omitempty"`
	UpdateAvailable bool     `json:"update_available"`
	CheckedAt       string   `json:"checked_at,omitempty"`
	Platform        string   `json:"platform"`
	Binary          string   `json:"binary"`
	UnitInstalled   bool     `json:"unit_installed"`
	UnitManaged     bool     `json:"unit_managed"`
	ReleaseURL      string   `json:"release_url"`
	Backups         []string `json:"backups"`
	Note            string   `json:"note,omitempty"`
}

type coreRelease struct {
	TagName string `json:"tag_name"`
}

// CorePlatform maps the running binary to a release platform suffix.
func CorePlatform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux-amd64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	case "linux/arm":
		return "linux-armv7", nil
	case "linux/386":
		return "linux-386", nil
	}
	return "", fmt.Errorf("暂不支持在 %s/%s 上安装 sing-box 内核", runtime.GOOS, runtime.GOARCH)
}

// NormalizeCoreVersion accepts 1.14.4, v1.14.4 and latest.
func NormalizeCoreVersion(version string) string {
	version = strings.TrimSpace(strings.ToLower(version))
	version = strings.TrimPrefix(version, "v")
	if version == "" || version == "latest" || version == "stable" {
		return "latest"
	}
	return version
}

func validCoreVersion(version string) bool {
	if version == "" || len(version) > 32 {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

// LatestCore returns the newest upstream tag, cached for a few minutes.
func (m *Manager) LatestCore(ctx context.Context, force bool) (string, error) {
	m.coreMu.Lock()
	if !force && m.coreLatest != "" && time.Since(m.coreChecked) < coreVersionTTL {
		version := m.coreLatest
		m.coreMu.Unlock()
		return version, nil
	}
	m.coreMu.Unlock()

	version, err := fetchLatestCore(ctx)
	m.coreMu.Lock()
	defer m.coreMu.Unlock()
	m.coreChecked = time.Now()
	if err != nil {
		m.coreLatestErr = err.Error()
		if m.coreLatest != "" {
			return m.coreLatest, nil
		}
		return "", err
	}
	m.coreLatest = version
	m.coreLatestErr = ""
	return version, nil
}

func fetchLatestCore(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CoreAPILatest, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "warden-gateway")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := coreHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("查询 sing-box 最新版本失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询 sing-box 最新版本失败：HTTP %d", resp.StatusCode)
	}
	var release coreRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("解析 sing-box 版本失败：%w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if !validCoreVersion(version) {
		return "", fmt.Errorf("sing-box 返回了无法识别的版本：%q", release.TagName)
	}
	return version, nil
}

var coreClient *http.Client

func coreHTTPClient() *http.Client {
	if coreClient == nil {
		coreClient = &http.Client{Timeout: 0}
	}
	return coreClient
}

// CoreLocalStatus reports the installed kernel without contacting GitHub, so
// payloads that must stay fast can embed it.
func (m *Manager) CoreLocalStatus() CoreStatus {
	platform, platformErr := CorePlatform()
	binary, _ := m.binaryPath()
	status := CoreStatus{
		Version:       m.Version(),
		Binary:        binary,
		Platform:      platform,
		Backups:       m.coreBackups(),
		UnitInstalled: coreUnitInstalled(m.service),
		UnitManaged:   coreUnitManaged(m.service),
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if platformErr != nil {
		status.Platform = runtime.GOOS + "/" + runtime.GOARCH
		status.Note = platformErr.Error()
	}
	status.Installed = status.Version != ""
	return status
}

// CoreStatus reports the installed kernel, the newest release and whether the
// two differ.
func (m *Manager) CoreStatus(ctx context.Context) CoreStatus {
	status := m.CoreLocalStatus()
	platform := status.Platform
	latest, err := m.LatestCore(ctx, false)
	if err != nil {
		status.LatestError = err.Error()
	} else {
		status.Latest = latest
	}
	status.UpdateAvailable = status.Latest != "" && status.Version != "" && status.Latest != status.Version
	if status.Latest != "" && platform != "" {
		status.ReleaseURL = CoreDownloadBase + "/v" + status.Latest + "/sing-box-" + status.Latest + "-" + platform + ".tar.gz"
	}
	return status
}

func (m *Manager) coreBackups() []string {
	pattern := filepath.Join(m.root, "bin", "sing-box.bak-*")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, filepath.Base(path))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// InstallCore downloads the requested release (or the latest one), swaps the
// kernel binary and restarts the service. The previous binary is kept as a
// backup and restored when anything fails.
func (m *Manager) InstallCore(ctx context.Context, version string, logf func(format string, args ...interface{})) (CoreStatus, error) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	version = NormalizeCoreVersion(version)
	if version == "latest" {
		latest, err := m.LatestCore(ctx, true)
		if err != nil {
			return CoreStatus{}, err
		}
		version = latest
	}
	if !validCoreVersion(version) {
		return CoreStatus{}, fmt.Errorf("版本号不合法：%q", version)
	}
	platform, err := CorePlatform()
	if err != nil {
		return CoreStatus{}, err
	}

	workDir := filepath.Join(m.dataDir, "singbox-core")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return CoreStatus{}, fmt.Errorf("创建下载目录失败：%w", err)
	}
	archive := filepath.Join(workDir, "sing-box-"+version+"-"+platform+".tar.gz")
	url := CoreDownloadBase + "/v" + version + "/sing-box-" + version + "-" + platform + ".tar.gz"
	logf("下载 %s", url)
	if err := downloadCore(ctx, url, archive, logf); err != nil {
		return CoreStatus{}, err
	}
	defer os.Remove(archive)

	stageDir := filepath.Join(workDir, "stage-"+version)
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return CoreStatus{}, fmt.Errorf("创建解压目录失败：%w", err)
	}
	defer os.RemoveAll(stageDir)
	staged := filepath.Join(stageDir, "sing-box")
	if err := extractCoreBinary(archive, staged); err != nil {
		return CoreStatus{}, err
	}
	found, err := runCoreVersion(staged)
	if err != nil {
		return CoreStatus{}, err
	}
	if found != version {
		return CoreStatus{}, fmt.Errorf("下载的内核版本是 %s，与期望的 %s 不一致", found, version)
	}

	if err := m.snapshotPreinstall(); err != nil {
		return CoreStatus{}, err
	}

	binDir := filepath.Join(m.root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return CoreStatus{}, fmt.Errorf("创建内核目录失败：%w", err)
	}
	target := filepath.Join(binDir, "sing-box")
	previous := m.Version()
	backup := ""
	if fileExists(target) {
		if previous == "" {
			previous = "unknown"
		}
		backup = filepath.Join(binDir, "sing-box.bak-"+previous)
		if err := copyFile(target, backup, 0o755); err != nil {
			return CoreStatus{}, fmt.Errorf("备份旧内核失败：%w", err)
		}
		logf("旧内核已备份为 %s", filepath.Base(backup))
	}
	if err := installFile(staged, target, 0o755); err != nil {
		return CoreStatus{}, fmt.Errorf("替换内核失败：%w", err)
	}
	restore := func() {
		if backup != "" {
			if err := installFile(backup, target, 0o755); err == nil {
				logf("已恢复旧内核 %s", filepath.Base(backup))
			}
		}
	}

	wroteUnit, err := m.ensureCoreUnit()
	if err != nil {
		restore()
		return CoreStatus{}, err
	}
	if wroteUnit {
		logf("已写入 %s", coreUnitPath(m.service))
		if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
			restore()
			return CoreStatus{}, fmt.Errorf("daemon-reload 失败：%v %s", err, out)
		}
	}
	// 卸载会停用服务；重新安装即使沿用主机已有单元，也要恢复开机自启。
	if out, err := runSystemctl(ctx, "enable", m.service); err != nil {
		logf("启用开机自启失败：%v %s", err, out)
	}
	if out, err := runSystemctl(ctx, "restart", m.service); err != nil {
		restore()
		if out2, startErr := runSystemctl(ctx, "start", m.service); startErr != nil {
			return CoreStatus{}, fmt.Errorf("重启 sing-box 失败：%v %s；尝试启动也失败：%v %s", err, out, startErr, out2)
		}
	}
	m.coreMu.Lock()
	if m.coreLatest == "" {
		m.coreLatest = version
	}
	m.coreLatestErr = ""
	m.coreChecked = time.Now()
	m.coreMu.Unlock()
	m.pruneCoreBackups()

	status := m.CoreStatus(ctx)
	status.Note = "已安装 sing-box " + version
	return status, nil
}

func (m *Manager) pruneCoreBackups() {
	backups := m.coreBackups()
	for i, name := range backups {
		if i < coreInstallKeep {
			continue
		}
		_ = os.Remove(filepath.Join(m.root, "bin", name))
	}
}

// ensureCoreUnit writes a systemd unit when the host has none for the service.
func (m *Manager) ensureCoreUnit() (bool, error) {
	if coreUnitInstalled(m.service) {
		return false, nil
	}
	path := coreUnitPath(m.service)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("创建 systemd 目录失败：%w", err)
	}
	unit := coreUnitTemplate(m.root, m.configPath)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return false, fmt.Errorf("写入 systemd 单元失败：%w", err)
	}
	return true, nil
}

func coreUnitPath(service string) string {
	name := service
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return filepath.Join("/etc/systemd/system", name)
}

func coreUnitInstalled(service string) bool {
	return findUnitPath(service) != ""
}

// coreUnitManaged reports whether the installed unit was written by this
// project, as opposed to a unit the host already had.
func coreUnitManaged(service string) bool {
	path := findUnitPath(service)
	if path == "" {
		return false
	}
	return unitManagedAt(path)
}

// unitManagedAt reports whether a unit file carries the managed marker.
func unitManagedAt(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "managed by warden-gateway")
}

// coreUnitTemplate renders the unit the panel manages. The marker comment lets
// uninstall know the unit is ours and safe to remove.
func coreUnitTemplate(root, configPath string) string {
	binary := filepath.Join(root, "bin", "sing-box")
	confDir := filepath.Join(root, "conf")
	return "# managed by warden-gateway\n" +
		"[Unit]\n" +
		"Description=sing-box Service (managed by warden-gateway)\n" +
		"After=network.target nss-lookup.target\n" +
		"Wants=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"User=root\n" +
		"NoNewPrivileges=true\n" +
		"ExecStart=" + binary + " run -c " + configPath + " -C " + confDir + "\n" +
		"Restart=on-failure\n" +
		"RestartPreventExitStatus=23\n" +
		"LimitNPROC=10000\n" +
		"LimitNOFILE=1048576\n" +
		"PrivateTmp=true\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

func runSystemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runCoreVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("校验内核失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	for i, field := range fields {
		if field == "version" && i+1 < len(fields) {
			return strings.TrimSpace(fields[i+1]), nil
		}
	}
	return "", fmt.Errorf("无法识别内核版本输出：%s", strings.TrimSpace(string(out)))
}

func downloadCore(ctx context.Context, url, dest string, logf func(string, ...interface{})) error {
	var lastErr error
	for _, mirror := range coreMirrors() {
		attempt := mirror + url
		label := attempt
		if mirror != "" {
			label = mirror + "（镜像）"
		}
		logf("尝试 %s", label)
		if err := downloadFileOnce(ctx, attempt, dest); err != nil {
			lastErr = err
			logf("下载失败：%v", err)
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的下载地址")
	}
	return fmt.Errorf("下载 sing-box 内核失败：%w", lastErr)
}

func downloadFileOnce(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "warden-gateway")
	resp, err := coreHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dest + ".part"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, coreMaxBytes+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if written > coreMaxBytes {
		os.Remove(tmp)
		return fmt.Errorf("安装包超过 %d MB", coreMaxBytes>>20)
	}
	if written == 0 {
		os.Remove(tmp)
		return errors.New("安装包为空")
	}
	return os.Rename(tmp, dest)
}

func extractCoreBinary(archive, dest string) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("打开安装包失败：%w", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("安装包不是有效的 tar.gz：%w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取安装包失败：%w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(header.Name) != "sing-box" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("写入内核失败：%w", err)
		}
		if _, err := io.Copy(out, io.LimitReader(reader, coreMaxBytes)); err != nil {
			out.Close()
			os.Remove(dest)
			return fmt.Errorf("解压内核失败：%w", err)
		}
		if err := out.Close(); err != nil {
			os.Remove(dest)
			return err
		}
		if err := os.Chmod(dest, 0o755); err != nil {
			return err
		}
		return nil
	}
	return errors.New("安装包里没有 sing-box 可执行文件")
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func installFile(src, dest string, mode os.FileMode) error {
	if err := copyFile(src, dest, mode); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// RestartCore restarts the kernel service.
func (m *Manager) RestartCore(ctx context.Context) error {
	if !coreUnitInstalled(m.service) {
		return fmt.Errorf("未安装 %s，无法重启", m.service)
	}
	if out, err := runSystemctl(ctx, "restart", m.service); err != nil {
		return fmt.Errorf("重启 sing-box 失败：%v %s", err, out)
	}
	return nil
}

// preinstallState records what the host looked like before the panel first
// installed a kernel, so the change can be undone exactly.
type preinstallState struct {
	At             string `json:"at"`
	UnitPath       string `json:"unit_path,omitempty"`
	UnitExisted    bool   `json:"unit_existed"`
	UnitSaved      string `json:"unit_saved,omitempty"`
	BinaryPath     string `json:"binary_path"`
	BinaryExisted  bool   `json:"binary_existed"`
	BinarySaved    string `json:"binary_saved,omitempty"`
	ServiceActive  bool   `json:"service_active"`
	ServiceEnabled bool   `json:"service_enabled"`
	RolledBackAt   string `json:"rolled_back_at,omitempty"`
}

func (m *Manager) preinstallDir() string {
	return filepath.Join(m.dataDir, "singbox-core", "preinstall")
}

func (m *Manager) preinstallFile() string {
	return filepath.Join(m.preinstallDir(), "state.json")
}

func (m *Manager) loadPreinstall() (preinstallState, error) {
	raw, err := os.ReadFile(m.preinstallFile())
	if err != nil {
		return preinstallState{}, err
	}
	var st preinstallState
	if err := json.Unmarshal(raw, &st); err != nil {
		return preinstallState{}, err
	}
	return st, nil
}

func (m *Manager) savePreinstall(st preinstallState) error {
	if err := os.MkdirAll(m.preinstallDir(), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeAtomic(m.preinstallFile(), raw, 0o600)
}

// snapshotPreinstall captures the pre-install state once. Later installs keep
// the original snapshot so rollback still returns to the very first state.
func (m *Manager) snapshotPreinstall() error {
	if _, err := os.Stat(m.preinstallFile()); err == nil {
		return nil
	}
	dir := m.preinstallDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(m.root, "bin", "sing-box")
	unitPath := findUnitPath(m.service)
	st := preinstallState{
		At:             time.Now().UTC().Format(time.RFC3339),
		UnitPath:       unitPath,
		UnitExisted:    unitPath != "",
		BinaryPath:     target,
		BinaryExisted:  fileExists(target),
		ServiceActive:  m.serviceFlag("is-active"),
		ServiceEnabled: m.serviceFlag("is-enabled"),
	}
	if st.UnitExisted {
		st.UnitSaved = filepath.Join(dir, "unit.service")
		if err := copyFile(st.UnitPath, st.UnitSaved, 0o644); err != nil {
			return fmt.Errorf("备份原服务单元失败：%w", err)
		}
	}
	if st.BinaryExisted {
		st.BinarySaved = filepath.Join(dir, "sing-box")
		if err := copyFile(target, st.BinarySaved, 0o755); err != nil {
			return fmt.Errorf("备份原内核失败：%w", err)
		}
	}
	return m.savePreinstall(st)
}

func findUnitPath(service string) string {
	name := service
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	for _, dir := range []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system"} {
		path := filepath.Join(dir, name)
		if fileExists(path) {
			return path
		}
	}
	return ""
}

func (m *Manager) serviceFlag(flag string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", flag, "--quiet", m.service).Run() == nil
}

// RollbackPrevious restores the newest kernel backup.
func (m *Manager) RollbackPrevious(ctx context.Context) (CoreStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	backups := m.coreBackups()
	if len(backups) == 0 {
		return CoreStatus{}, errors.New("没有可回滚的内核备份")
	}
	name := backups[0]
	backup := filepath.Join(m.root, "bin", name)
	version := strings.TrimPrefix(name, "sing-box.bak-")
	target := filepath.Join(m.root, "bin", "sing-box")
	current := m.Version()
	if current != "" {
		keep := filepath.Join(m.root, "bin", "sing-box.bak-"+current)
		if !fileExists(keep) {
			if err := copyFile(target, keep, 0o755); err != nil {
				return CoreStatus{}, fmt.Errorf("保存当前内核失败：%w", err)
			}
		}
	}
	if err := installFile(backup, target, 0o755); err != nil {
		return CoreStatus{}, fmt.Errorf("回滚内核失败：%w", err)
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return CoreStatus{}, fmt.Errorf("清理回滚备份失败：%w", err)
	}
	if !coreUnitInstalled(m.service) {
		if _, err := m.ensureCoreUnit(); err != nil {
			return CoreStatus{}, err
		}
		if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
			return CoreStatus{}, fmt.Errorf("daemon-reload 失败：%v %s", err, out)
		}
	}
	if out, err := runSystemctl(ctx, "restart", m.service); err != nil {
		return CoreStatus{}, fmt.Errorf("回滚后重启失败：%v %s", err, out)
	}
	status := m.CoreStatus(ctx)
	status.Note = "已回滚到 sing-box " + version
	return status, nil
}

// RollbackPreinstall returns the host to the state captured before the first
// panel-managed install: original kernel and unit restored, or removed when
// they did not exist.
func (m *Manager) RollbackPreinstall(ctx context.Context) (CoreStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.loadPreinstall()
	if err != nil {
		return CoreStatus{}, errors.New("没有安装前快照，无法回滚到安装前")
	}
	if _, err := runSystemctl(ctx, "stop", m.service); err != nil {
		m.log.Warnf("singbox: stop before preinstall rollback: %v", err)
	}
	if _, err := runSystemctl(ctx, "disable", m.service); err != nil {
		m.log.Warnf("singbox: disable before preinstall rollback: %v", err)
	}
	target := filepath.Join(m.root, "bin", "sing-box")
	if st.BinaryExisted && st.BinarySaved != "" && fileExists(st.BinarySaved) {
		if err := installFile(st.BinarySaved, target, 0o755); err != nil {
			return CoreStatus{}, fmt.Errorf("恢复原内核失败：%w", err)
		}
	} else if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return CoreStatus{}, fmt.Errorf("移除内核失败：%w", err)
	}
	unitPath := coreUnitPath(m.service)
	if st.UnitExisted && st.UnitSaved != "" && fileExists(st.UnitSaved) {
		if err := installFile(st.UnitSaved, st.UnitPath, 0o644); err != nil {
			return CoreStatus{}, fmt.Errorf("恢复原服务单元失败：%w", err)
		}
	} else if fileExists(unitPath) {
		raw, readErr := os.ReadFile(unitPath)
		if readErr == nil && strings.Contains(string(raw), "managed by warden-gateway") {
			if err := os.Remove(unitPath); err != nil {
				return CoreStatus{}, fmt.Errorf("移除服务单元失败：%w", err)
			}
		}
	}
	if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
		m.log.Warnf("singbox: daemon-reload after preinstall rollback: %v %s", err, out)
	}
	if st.ServiceEnabled {
		if _, err := runSystemctl(ctx, "enable", m.service); err != nil {
			m.log.Warnf("singbox: enable after preinstall rollback: %v", err)
		}
	}
	if st.ServiceActive {
		if out, err := runSystemctl(ctx, "start", m.service); err != nil {
			return CoreStatus{}, fmt.Errorf("恢复原服务状态失败：%v %s", err, out)
		}
	}
	st.RolledBackAt = time.Now().UTC().Format(time.RFC3339)
	if err := m.savePreinstall(st); err != nil {
		m.log.Warnf("singbox: save preinstall state: %v", err)
	}
	status := m.CoreStatus(ctx)
	status.Note = "已回滚到安装前状态"
	return status, nil
}

// UninstallCore stops the service, archives the managed kernel and removes the
// unit the panel wrote. Node configuration files are left untouched.
func (m *Manager) UninstallCore(ctx context.Context) (CoreStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !coreUnitInstalled(m.service) {
		return CoreStatus{}, errors.New("未安装 sing-box 服务单元")
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	archiveDir := filepath.Join(m.dataDir, "singbox-core", "uninstall-"+stamp)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return CoreStatus{}, fmt.Errorf("创建归档目录失败：%w", err)
	}
	target := filepath.Join(m.root, "bin", "sing-box")
	archived := []string{}
	if fileExists(target) {
		dest := filepath.Join(archiveDir, "sing-box")
		if err := copyFile(target, dest, 0o755); err != nil {
			return CoreStatus{}, fmt.Errorf("归档内核失败：%w", err)
		}
		archived = append(archived, "sing-box")
	}
	unitPath := findUnitPath(m.service)
	if unitPath == "" {
		unitPath = coreUnitPath(m.service)
	}
	unitManaged := unitManagedAt(unitPath)
	if unitManaged {
		dest := filepath.Join(archiveDir, filepath.Base(unitPath))
		if err := copyFile(unitPath, dest, 0o644); err != nil {
			return CoreStatus{}, fmt.Errorf("归档服务单元失败：%w", err)
		}
		archived = append(archived, filepath.Base(unitPath))
	}
	manifest := map[string]interface{}{
		"at":      time.Now().UTC().Format(time.RFC3339),
		"version": m.Version(),
		"binary":  target,
		"unit":    unitPath,
		"files":   archived,
	}
	if raw, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(archiveDir, "manifest.json"), append(raw, '\n'), 0o600)
	}
	if out, err := runSystemctl(ctx, "stop", m.service); err != nil {
		return CoreStatus{}, fmt.Errorf("停止服务失败：%v %s", err, out)
	}
	if out, err := runSystemctl(ctx, "disable", m.service); err != nil {
		m.log.Warnf("singbox: disable on uninstall: %v %s", err, out)
	}
	if unitManaged {
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return CoreStatus{}, fmt.Errorf("移除服务单元失败：%w", err)
		}
		if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
			m.log.Warnf("singbox: daemon-reload on uninstall: %v %s", err, out)
		}
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return CoreStatus{}, fmt.Errorf("移除内核失败：%w", err)
	}
	m.coreMu.Lock()
	m.coreLatest = ""
	m.coreLatestErr = ""
	m.coreMu.Unlock()
	status := m.CoreStatus(ctx)
	status.Note = "已卸载 sing-box 内核，归档在 " + archiveDir
	return status, nil
}
