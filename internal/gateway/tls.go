package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
	"golang.org/x/crypto/acme/autocert"
)

type TLSManager struct {
	Mode        string
	Config      *tls.Config
	HTTPHandler http.Handler
	Domain      string
	manager     *autocert.Manager
	leaf        *x509.Certificate
}

func NewTLSManager(cfg *config.Config) (*TLSManager, error) {
	switch cfg.TLS.Mode {
	case "files":
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load certificate: %w", err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, err
		}
		cert.Leaf = leaf
		return &TLSManager{
			Mode:   "files",
			Domain: cfg.Domain,
			Config: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			leaf:   leaf,
			HTTPHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target := "https://" + r.Host + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			}),
		}, nil
	case "auto":
		if cfg.Domain == "" {
			return nil, errors.New("auto TLS requires a domain")
		}
		if err := os.MkdirAll(cfg.TLS.CacheDir, 0o700); err != nil {
			return nil, err
		}
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			Email:  cfg.Email,
			Cache:  autocert.DirCache(cfg.TLS.CacheDir),
			HostPolicy: func(_ context.Context, host string) error {
				if strings.EqualFold(host, cfg.Domain) {
					return nil
				}
				return fmt.Errorf("host %q is not configured", host)
			},
		}
		tlsCfg := m.TLSConfig()
		tlsCfg.MinVersion = tls.VersionTLS12
		return &TLSManager{
			Mode:        "auto",
			Domain:      cfg.Domain,
			Config:      tlsCfg,
			HTTPHandler: m.HTTPHandler(nil),
			manager:     m,
		}, nil
	case "self":
		cert, leaf, err := selfSigned(cfg)
		if err != nil {
			return nil, err
		}
		return &TLSManager{
			Mode:   "self",
			Domain: cfg.Domain,
			Config: &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			},
			leaf: leaf,
			HTTPHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target := "https://" + r.Host + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			}),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported TLS mode %q", cfg.TLS.Mode)
	}
}

func (t *TLSManager) Leaf() *x509.Certificate { return t.leaf }

func (t *TLSManager) ACMEDirectory() string {
	if t.manager == nil {
		return ""
	}
	return fmt.Sprintf("%T", t.manager.Cache)
}

func selfSigned(cfg *config.Config) (tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Warden Gateway"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	host := cfg.PublicHost()
	if host != "" {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	if cfg.Domain != "" && net.ParseIP(cfg.Domain) == nil {
		tmpl.DNSNames = append(tmpl.DNSNames, cfg.Domain)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	cert.Leaf = leaf
	if cfg.TLS.CacheDir != "" {
		_ = os.MkdirAll(cfg.TLS.CacheDir, 0o700)
		_ = os.WriteFile(filepath.Join(cfg.TLS.CacheDir, "self.crt"), certPEM, 0o644)
		_ = os.WriteFile(filepath.Join(cfg.TLS.CacheDir, "self.key"), keyPEM, 0o600)
	}
	return cert, leaf, nil
}

func CertificateSummary(addr, serverName string) (map[string]interface{}, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("no peer certificate")
	}
	leaf := state.PeerCertificates[0]
	now := time.Now()
	days := int(leaf.NotAfter.Sub(now).Hours() / 24)
	out := map[string]interface{}{
		"ok":             now.Before(leaf.NotAfter) && now.After(leaf.NotBefore),
		"subject":        leaf.Subject.String(),
		"issuer":         leaf.Issuer.String(),
		"not_before":     leaf.NotBefore.UTC().Format(time.RFC3339),
		"not_after":      leaf.NotAfter.UTC().Format(time.RFC3339),
		"expires_ts":     leaf.NotAfter.Unix(),
		"days_remaining": days,
		"dns_names":      leaf.DNSNames,
		"serial":         leaf.SerialNumber.String(),
	}
	return out, nil
}
