package singbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/logging"
)

const (
	// DefaultRoot is where the sing-box one-click installer keeps its files.
	DefaultRoot    = "/etc/sing-box"
	DefaultService = "sing-box.service"
	// enabledExt and disabledExt mark an inbound file as active or parked.
	enabledExt  = ".json"
	disabledExt = ".json.off"
	// maxConfigBytes bounds a single inbound file edited from the panel.
	maxConfigBytes = 512 << 10
	commandTimeout = 20 * time.Second

	ManagedMarker = "<!-- warden-managed -->"
)

// Options configures a Manager.
type Options struct {
	Root       string
	DataDir    string
	Service    string
	Binary     string
	Host       string
	Log        *logging.Logger
	BackupKeep int
}

// Manager owns the sing-box configuration directory and its systemd unit.
type Manager struct {
	root       string
	confDir    string
	configPath string
	dataDir    string
	service    string
	binary     string
	host       string
	log        *logging.Logger
	keep       int
	mu         sync.Mutex

	coreMu        sync.Mutex
	coreLatest    string
	coreLatestErr string
	coreChecked   time.Time
}

// New builds a manager without touching the filesystem so the panel can start
// on hosts where sing-box is not installed.
func New(opts Options) *Manager {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = DefaultRoot
	}
	service := strings.TrimSpace(opts.Service)
	if service == "" {
		service = DefaultService
	}
	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = "/var/lib/warden-gateway"
	}
	keep := opts.BackupKeep
	if keep <= 0 {
		keep = 20
	}
	log := opts.Log
	if log == nil {
		log = &logging.Logger{}
	}
	return &Manager{
		root:       root,
		confDir:    filepath.Join(root, "conf"),
		configPath: filepath.Join(root, "config.json"),
		dataDir:    dataDir,
		service:    service,
		binary:     strings.TrimSpace(opts.Binary),
		host:       strings.TrimSpace(opts.Host),
		log:        log,
		keep:       keep,
	}
}

// Root is the sing-box installation directory.
func (m *Manager) Root() string { return m.root }

// ConfigPath is the main sing-box configuration file.
func (m *Manager) ConfigPath() string { return m.configPath }

// ConfDir is the directory holding one file per inbound.
func (m *Manager) ConfDir() string { return m.confDir }

// Service is the systemd unit that runs sing-box.
func (m *Manager) Service() string { return m.service }

// Host returns the hostname used in generated share links.
func (m *Manager) Host() string { return m.host }

// SetHost overrides the hostname used in generated share links.
func (m *Manager) SetHost(host string) { m.host = strings.TrimSpace(host) }

func (m *Manager) backupDir() string { return filepath.Join(m.dataDir, "singbox-backups") }

// binaryPath resolves the sing-box executable. The one-click installer keeps a
// 68 byte wrapper in /usr/local/bin, so the real binary is preferred.
func (m *Manager) binaryPath() (string, error) {
	if m.binary != "" {
		if st, err := os.Stat(m.binary); err == nil && !st.IsDir() {
			return m.binary, nil
		}
		return "", fmt.Errorf("sing-box 二进制不存在：%s", m.binary)
	}
	candidates := []string{filepath.Join(m.root, "bin", "sing-box"), "/usr/bin/sing-box", "/usr/local/bin/sing-box"}
	for _, cand := range candidates {
		st, err := os.Stat(cand)
		if err != nil || st.IsDir() {
			continue
		}
		if cand == "/usr/local/bin/sing-box" && st.Mode()&0o111 != 0 {
			// The installer ships a shell wrapper here; skip it when the real
			// binary exists, which the first candidate already covers.
			if _, err := os.Stat(filepath.Join(m.root, "bin", "sing-box")); err == nil {
				continue
			}
		}
		return cand, nil
	}
	if path, err := exec.LookPath("sing-box"); err == nil {
		return path, nil
	}
	return "", errors.New("未找到 sing-box 二进制")
}

// Installed reports whether the sing-box binary is present.
func (m *Manager) Installed() bool {
	_, err := m.binaryPath()
	return err == nil
}

// Version asks the binary for its version.
func (m *Manager) Version() string {
	bin, err := m.binaryPath()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "version ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "version "))
		}
		if strings.HasPrefix(line, "sing-box version ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "sing-box version "))
		}
	}
	return ""
}

// ServiceState returns the ActiveState and SubState of the systemd unit.
func (m *Manager) ServiceState() (state, sub string) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", m.service, "--no-page", "--property=ActiveState,SubState").Output()
	if err != nil {
		return "unknown", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ActiveState":
			state = strings.TrimSpace(value)
		case "SubState":
			sub = strings.TrimSpace(value)
		}
	}
	return state, sub
}

// Running reports whether the systemd unit is active.
func (m *Manager) Running() bool {
	state, _ := m.ServiceState()
	return state == "active"
}

// Restart restarts the sing-box unit, which is separate from the gateway unit,
// so this never interrupts the panel itself.
func (m *Manager) Restart() error {
	if _, err := os.Stat(filepath.Join("/etc/systemd/system", m.service)); err != nil {
		if st, statErr := os.Stat(filepath.Join("/lib/systemd/system", m.service)); statErr != nil || st.IsDir() {
			return fmt.Errorf("未安装 %s，无法重启", m.service)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", m.service).CombinedOutput()
	if err != nil {
		return fmt.Errorf("重启 sing-box 失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restartHook restarts the unit. Tests replace it so node operations can run on
// hosts without systemd.
var restartHook = func(m *Manager) error { return m.Restart() }

// reloadUnit restarts sing-box through the replaceable hook.
func (m *Manager) reloadUnit() error { return restartHook(m) }

// Reload validates the configuration and restarts the unit.
func (m *Manager) Reload() (Check, error) {
	check := m.Check()
	if !check.OK {
		return check, fmt.Errorf("配置校验失败：%s", check.Output)
	}
	if err := m.Restart(); err != nil {
		return check, err
	}
	return check, nil
}
