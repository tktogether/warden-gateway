// Package cnagent implements the agent side of the CN split: on-disk
// configuration, machine identity, and the pieces the warden-cn-agent daemon
// needs to talk to the gateway control plane.
package cnagent

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultUpstream         = "223.5.5.5:53"
	DefaultDNSListen        = "198.18.0.1:53"
	DefaultRepo             = "tktogether/warden-gateway"
	DefaultHeartbeatSeconds = 10
)

var tokenPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Config is the agent configuration stored as agent.json (mode 0600) on the
// CN machine.
type Config struct {
	Origin           string `json:"origin"`
	Token            string `json:"token"`
	GatewayIP        string `json:"gateway_ip"`
	Upstream         string `json:"upstream"`
	DNSListen        string `json:"dns_listen"`
	Repo             string `json:"repo"`
	MachineID        string `json:"machine_id"`
	Hostname         string `json:"hostname"`
	HeartbeatSeconds int    `json:"heartbeat_seconds"`
	// InsecureTLS disables certificate verification for the gateway. It is an
	// explicit install-time opt-in written by bootstrap.sh for self-signed or
	// IP-only panels (tls.mode=self), where there is no verifiable name. ACME
	// deployments leave it false and keep full verification.
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

// DefaultConfig returns the config used before provisioning fills in the
// gateway-issued fields. DNSListen defaults to the gateway IP inside the
// 198.18.0.0/15 range; tests may override it.
func DefaultConfig() Config {
	return Config{
		Upstream:         DefaultUpstream,
		DNSListen:        DefaultDNSListen,
		Repo:             DefaultRepo,
		HeartbeatSeconds: DefaultHeartbeatSeconds,
	}
}

// Load reads and parses the config at path. A missing file is an error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the config as indented JSON with mode 0600, via a temp file and
// rename so readers never see a half-written file.
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFile0600(path, data)
}

// Validate requires the gateway-issued fields and a full-length hex token.
// DNSListen is optional: empty means "use DefaultDNSListen". Upstream is
// optional too (empty means "use DefaultUpstream"), but when set it must be
// ip:port with an IPv4 host part — bootstrap writes that host verbatim into
// resolv.conf nameserver lines and the DNS proxy dials it directly, so a
// hostname or malformed pair must fail validation instead of breaking the
// machine's resolver at runtime.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Origin) == "" {
		return errors.New("origin is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token is required")
	}
	if strings.TrimSpace(c.GatewayIP) == "" {
		return errors.New("gateway_ip is required")
	}
	if !tokenPattern.MatchString(c.Token) {
		return errors.New("token must be a 64 character hex string")
	}
	upstream := strings.TrimSpace(c.Upstream)
	if upstream == "" {
		upstream = DefaultUpstream
	}
	host, port, err := net.SplitHostPort(upstream)
	if err != nil {
		return fmt.Errorf("upstream must be ip:port, got %q", c.Upstream)
	}
	if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
		return fmt.Errorf("upstream host must be an IPv4 address, got %q", host)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("upstream port must be numeric 1-65535, got %q", port)
	}
	return nil
}

// HTTPClient returns the HTTP client used for gateway control-plane calls.
// Verification is on by default; when InsecureTLS is set the client skips
// certificate verification. That setting exists only for gateways behind a
// self-signed certificate or an IP-only panel, where the certificate cannot be
// validated; bootstrap.sh writes it explicitly (--insecure-tls or the
// self-signed auto-detection) so ACME deployments keep verification.
func (c *Config) HTTPClient(timeout time.Duration) *http.Client {
	if c != nil && c.InsecureTLS {
		// #nosec G402 -- deliberate operator opt-in for a self-signed/IP-only
		// gateway; the alternative is that such gateways cannot be managed at
		// all. TLS is still used, only the certificate chain check is skipped.
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402
			},
		}
	}
	return &http.Client{Timeout: timeout}
}

// NewMachineID returns a stable machine identity: the contents of
// /etc/machine-id when present, otherwise 16 random bytes as hex.
func NewMachineID() string {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error as of Go 1.24
	return hex.EncodeToString(b)
}

// writeFile0600 writes data to path atomically with mode 0600.
func writeFile0600(path string, data []byte) error {
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
	if err := tmp.Chmod(0o600); err != nil {
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
