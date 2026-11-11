package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	Version          = 1
	DefaultPanelPath = "/warden"
	DefaultHTTPAddr  = "127.0.0.1:8080"
	DefaultHTTPSAddr = "127.0.0.1:8443"
	DefaultEntryAddr = "0.0.0.0:443"
	DefaultACMEAddr  = "0.0.0.0:80"
	DefaultDataDir   = "/var/lib/warden-gateway"
	DefaultConfigDir = "/etc/warden-gateway"
)

var hostPattern = regexp.MustCompile("^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$")

type Config struct {
	Version         int          `json:"version"`
	PublicURL       string       `json:"public_url"`
	PanelPath       string       `json:"panel_path"`
	AccessKeySHA256 string       `json:"access_key_sha256"`
	SessionSecret   string       `json:"session_secret"`
	Domain          string       `json:"domain"`
	Email           string       `json:"email,omitempty"`
	Listen          ListenConfig `json:"listen"`
	TLS             TLSConfig    `json:"tls"`
	Docker          DockerConfig `json:"docker"`
	GitHub          GitHubConfig `json:"github"`
	SNI             SNIConfig    `json:"sni"`
	DNS             DNSConfig    `json:"dns"`
	Backup          BackupConfig `json:"backup"`
	Log             LogConfig    `json:"log"`
}

type ListenConfig struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	Entry string `json:"entry"`
	ACME  string `json:"acme"`
}

type TLSConfig struct {
	Mode     string `json:"mode"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type DockerConfig struct {
	Enabled         bool     `json:"enabled"`
	DefaultRegistry string   `json:"default_registry"`
	ReferrersStub   bool     `json:"referrers_stub"`
	AllowCIDRs      []string `json:"allow_cidrs,omitempty"`
}

type GitHubConfig struct {
	Enabled        bool     `json:"enabled"`
	AllowHosts     []string `json:"allow_hosts"`
	MaxBodyBytes   int64    `json:"max_body_bytes"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

type SNIConfig struct {
	Enabled     bool     `json:"enabled"`
	PanelDomain string   `json:"panel_domain"`
	Passthrough []string `json:"passthrough"`
}

type DNSConfig struct {
	Enabled   bool     `json:"enabled"`
	Listen    string   `json:"listen"`
	GatewayIP string   `json:"gateway_ip"`
	Domains   []string `json:"domains"`
	Upstream  string   `json:"upstream"`
}

type BackupConfig struct {
	Dir    string `json:"dir"`
	Retain int    `json:"retain"`
}

type LogConfig struct {
	File  string `json:"file,omitempty"`
	Level string `json:"level"`
}

func Default() Config {
	return Config{
		Version:   Version,
		PanelPath: DefaultPanelPath,
		Listen: ListenConfig{
			HTTP:  DefaultHTTPAddr,
			HTTPS: DefaultHTTPSAddr,
			Entry: DefaultEntryAddr,
			ACME:  DefaultACMEAddr,
		},
		TLS: TLSConfig{
			Mode:     "auto",
			CacheDir: filepath.Join(DefaultDataDir, "acme"),
		},
		Docker: DockerConfig{
			Enabled:         true,
			DefaultRegistry: "docker.io",
			ReferrersStub:   true,
		},
		GitHub: GitHubConfig{
			Enabled:        true,
			AllowHosts:     DefaultGitHubHosts(),
			MaxBodyBytes:   256 << 20,
			TimeoutSeconds: 300,
		},
		SNI: SNIConfig{
			Enabled:     true,
			Passthrough: DefaultPassthroughHosts(),
		},
		DNS: DNSConfig{
			Enabled:  false,
			Listen:   "0.0.0.0:53",
			Upstream: "1.1.1.1:53",
		},
		Backup: BackupConfig{
			Dir:    filepath.Join(DefaultDataDir, "backups"),
			Retain: 10,
		},
		Log: LogConfig{Level: "info"},
	}
}

func DefaultGitHubHosts() []string {
	return []string{
		"github.com",
		"api.github.com",
		"codeload.github.com",
		"raw.githubusercontent.com",
		"objects.githubusercontent.com",
		"gist.githubusercontent.com",
		"avatars.githubusercontent.com",
		"user-images.githubusercontent.com",
		"github-releases.githubusercontent.com",
		"release-assets.githubusercontent.com",
		"*.github.com",
		"*.githubusercontent.com",
	}
}

func DefaultPassthroughHosts() []string {
	return []string{
		"github.com",
		"api.github.com",
		"codeload.github.com",
		"*.github.com",
		"*.githubusercontent.com",
		"ghcr.io",
		"*.ghcr.io",
		"registry-1.docker.io",
		"auth.docker.io",
		"*.docker.io",
		"*.docker.com",
		"*.cloudfront.net",
		"*.cloudflare.docker.com",
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Normalize() {
	if c.Version == 0 {
		c.Version = Version
	}
	c.PublicURL = strings.TrimSpace(c.PublicURL)
	c.Domain = strings.ToLower(strings.TrimSpace(c.Domain))
	c.Email = strings.TrimSpace(c.Email)
	c.PanelPath = normalizePath(c.PanelPath)
	if c.PanelPath == "" {
		c.PanelPath = DefaultPanelPath
	}
	c.Listen.HTTP = strings.TrimSpace(c.Listen.HTTP)
	c.Listen.HTTPS = strings.TrimSpace(c.Listen.HTTPS)
	c.Listen.Entry = strings.TrimSpace(c.Listen.Entry)
	c.Listen.ACME = strings.TrimSpace(c.Listen.ACME)
	c.TLS.Mode = strings.ToLower(strings.TrimSpace(c.TLS.Mode))
	c.TLS.CertFile = strings.TrimSpace(c.TLS.CertFile)
	c.TLS.KeyFile = strings.TrimSpace(c.TLS.KeyFile)
	c.TLS.CacheDir = strings.TrimSpace(c.TLS.CacheDir)
	c.Docker.DefaultRegistry = strings.TrimSpace(c.Docker.DefaultRegistry)
	c.SNI.PanelDomain = strings.ToLower(strings.TrimSpace(c.SNI.PanelDomain))
	c.DNS.Listen = strings.TrimSpace(c.DNS.Listen)
	c.DNS.GatewayIP = strings.TrimSpace(c.DNS.GatewayIP)
	c.DNS.Upstream = strings.TrimSpace(c.DNS.Upstream)
	c.Backup.Dir = strings.TrimSpace(c.Backup.Dir)
	c.Log.File = strings.TrimSpace(c.Log.File)
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	if c.Docker.DefaultRegistry == "" {
		c.Docker.DefaultRegistry = "docker.io"
	}
	if c.TLS.CacheDir == "" {
		c.TLS.CacheDir = filepath.Join(DefaultDataDir, "acme")
	}
	if c.Backup.Dir == "" {
		c.Backup.Dir = filepath.Join(DefaultDataDir, "backups")
	}
	if c.Backup.Retain <= 0 {
		c.Backup.Retain = 10
	}
	if c.GitHub.MaxBodyBytes <= 0 {
		c.GitHub.MaxBodyBytes = 256 << 20
	}
	if c.GitHub.TimeoutSeconds <= 0 {
		c.GitHub.TimeoutSeconds = 300
	}
	if len(c.GitHub.AllowHosts) == 0 {
		c.GitHub.AllowHosts = DefaultGitHubHosts()
	}
	if len(c.SNI.Passthrough) == 0 {
		c.SNI.Passthrough = DefaultPassthroughHosts()
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Domain == "" && c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err == nil {
			host := strings.ToLower(u.Hostname())
			if host != "" && net.ParseIP(host) == nil {
				c.Domain = host
			}
		}
	}
	if c.SNI.PanelDomain == "" {
		c.SNI.PanelDomain = c.Domain
	}
	if c.TLS.Mode == "" {
		if c.Domain != "" && net.ParseIP(c.Domain) == nil {
			c.TLS.Mode = "auto"
		} else {
			c.TLS.Mode = "self"
		}
	}
}

func (c *Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return errors.New("public_url is not a valid URL")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("public_url scheme must be http or https")
		}
		if u.User != nil {
			return errors.New("public_url must not contain user info")
		}
		if p := strings.TrimSuffix(u.EscapedPath(), "/"); p != "" && p != c.PanelPath {
			return errors.New("public_url path must be empty or equal to panel_path")
		}
	}
	if c.PanelPath == "" || !strings.HasPrefix(c.PanelPath, "/") {
		return errors.New("panel_path must start with /")
	}
	if strings.Contains(c.PanelPath, "..") || strings.ContainsAny(c.PanelPath, " \t") {
		return errors.New("panel_path contains invalid characters")
	}
	if c.AccessKeySHA256 != "" && !isHex64(c.AccessKeySHA256) {
		return errors.New("access_key_sha256 must be a 64 character hex string")
	}
	if c.SessionSecret != "" && len(c.SessionSecret) < 32 {
		return errors.New("session_secret must be at least 32 characters")
	}
	if c.Domain != "" {
		if net.ParseIP(c.Domain) == nil && !validHostname(c.Domain) {
			return errors.New("domain is not a valid hostname")
		}
	}
	if c.Email != "" && (!strings.Contains(c.Email, "@") || strings.ContainsAny(c.Email, " \t\r\n")) {
		return errors.New("email is not valid")
	}
	if err := validateListen("listen.http", c.Listen.HTTP, false); err != nil {
		return err
	}
	if err := validateListen("listen.https", c.Listen.HTTPS, false); err != nil {
		return err
	}
	if c.SNI.Enabled {
		if err := validateListen("listen.entry", c.Listen.Entry, true); err != nil {
			return err
		}
	}
	if c.TLS.Mode == "auto" {
		if err := validateListen("listen.acme", c.Listen.ACME, true); err != nil {
			return err
		}
	}
	switch c.TLS.Mode {
	case "auto":
		if c.Domain == "" || net.ParseIP(c.Domain) != nil {
			return errors.New("tls.mode auto requires a DNS domain")
		}
		if c.TLS.CacheDir == "" {
			return errors.New("tls.cache_dir is required for auto mode")
		}
	case "files":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return errors.New("tls.mode files requires cert_file and key_file")
		}
		if _, err := os.Stat(c.TLS.CertFile); err != nil {
			return fmt.Errorf("tls.cert_file: %w", err)
		}
		if _, err := os.Stat(c.TLS.KeyFile); err != nil {
			return fmt.Errorf("tls.key_file: %w", err)
		}
	case "self":
	default:
		return errors.New("tls.mode must be auto, files or self")
	}
	if c.Docker.Enabled && c.Docker.DefaultRegistry == "" {
		return errors.New("docker.default_registry is required")
	}
	for _, raw := range c.Docker.AllowCIDRs {
		if _, _, err := net.ParseCIDR(raw); err != nil {
			return fmt.Errorf("docker.allow_cidrs: %w", err)
		}
	}
	if c.GitHub.Enabled {
		if len(c.GitHub.AllowHosts) == 0 {
			return errors.New("github.allow_hosts must not be empty")
		}
		if c.GitHub.MaxBodyBytes <= 0 {
			return errors.New("github.max_body_bytes must be positive")
		}
		if c.GitHub.TimeoutSeconds <= 0 || c.GitHub.TimeoutSeconds > 3600 {
			return errors.New("github.timeout_seconds must be between 1 and 3600")
		}
		for _, h := range c.GitHub.AllowHosts {
			if !validHostPattern(h) {
				return fmt.Errorf("github.allow_hosts: invalid host pattern %q", h)
			}
		}
	}
	if c.SNI.Enabled {
		if c.SNI.PanelDomain == "" {
			return errors.New("sni.panel_domain is required when sni is enabled")
		}
		if net.ParseIP(c.SNI.PanelDomain) == nil && !validHostname(c.SNI.PanelDomain) {
			return errors.New("sni.panel_domain is not a valid hostname")
		}
		if len(c.SNI.Passthrough) == 0 {
			return errors.New("sni.passthrough must not be empty")
		}
		for _, h := range c.SNI.Passthrough {
			if !validHostPattern(h) {
				return fmt.Errorf("sni.passthrough: invalid host pattern %q", h)
			}
		}
	}
	if c.DNS.Enabled {
		if err := validateListen("dns.listen", c.DNS.Listen, true); err != nil {
			return err
		}
		if net.ParseIP(c.DNS.GatewayIP) == nil {
			return errors.New("dns.gateway_ip must be an IP address")
		}
		if len(c.DNS.Domains) == 0 {
			return errors.New("dns.domains must not be empty")
		}
		for _, d := range c.DNS.Domains {
			if net.ParseIP(d) == nil && !validHostname(d) {
				return fmt.Errorf("dns.domains: invalid domain %q", d)
			}
		}
		if err := validateListen("dns.upstream", c.DNS.Upstream, false); err != nil {
			return err
		}
	}
	if c.Backup.Dir == "" {
		return errors.New("backup.dir is required")
	}
	if c.Backup.Retain <= 0 || c.Backup.Retain > 1000 {
		return errors.New("backup.retain must be between 1 and 1000")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log.level must be debug, info, warn or error")
	}
	return nil
}

func (c *Config) Save(path string) error {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeFileAtomic(path, raw, 0o600)
}

func (c *Config) PublicHost() string {
	if c.PublicURL == "" {
		return ""
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (c *Config) PublicScheme() string {
	if c.PublicURL == "" {
		return "http"
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme == "" {
		return "http"
	}
	return u.Scheme
}

func (c *Config) BaseURL() string {
	if c.PublicURL == "" {
		return ""
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host, "/")
}

func (c *Config) PanelURL() string {
	base := c.BaseURL()
	if base == "" {
		return c.PanelPath
	}
	return base + c.PanelPath
}

func (c *Config) TLSEnabled() bool {
	return c.Listen.HTTPS != "" || c.Listen.Entry != ""
}

func (c *Config) EntryEnabled() bool {
	return c.Listen.Entry != "" && c.SNI.Enabled
}

func (c *Config) ACMEEnabled() bool {
	return c.Listen.ACME != "" && c.TLS.Mode == "auto"
}

func GenerateAccessKey() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	return key, HashAccessKey(key), nil
}

func GenerateSessionSecret() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashAccessKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func ValidateAccessKey(key string) error {
	if len(key) < 10 {
		return errors.New("access key must be at least 10 characters")
	}
	if len(key) > 256 {
		return errors.New("access key must be at most 256 characters")
	}
	for _, r := range key {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return errors.New("access key must not contain whitespace")
		}
	}
	return nil
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimSuffix(p, "/")
	return p
}

func validateListen(name, addr string, allowEmptyHost bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if host == "" && !allowEmptyHost {
		return fmt.Errorf("%s: host must not be empty", name)
	}
	if host != "" && net.ParseIP(host) == nil && !validHostname(host) {
		return fmt.Errorf("%s: invalid host", name)
	}
	if port == "" {
		return fmt.Errorf("%s: port is required", name)
	}
	n, err := net.LookupPort("tcp", port)
	if err != nil || n <= 0 || n > 65535 {
		return fmt.Errorf("%s: invalid port", name)
	}
	return nil
}

func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 || strings.Contains(h, "..") {
		return false
	}
	return hostPattern.MatchString(h)
}

func validHostPattern(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" {
		return false
	}
	if strings.HasPrefix(h, "*.") {
		return validHostname(strings.TrimPrefix(h, "*."))
	}
	return validHostname(h) || net.ParseIP(h) != nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
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
	return os.Rename(tmpName, path)
}

func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
