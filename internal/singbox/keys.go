package singbox

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// NewUUID returns a random RFC 4122 version 4 UUID, the credential VLESS uses.
func NewUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// RandomPassword returns n bytes of entropy encoded as URL-safe base64 without
// padding, which is accepted verbatim by Hysteria2, Trojan and Shadowsocks.
func RandomPassword(n int) (string, error) {
	if n < 8 {
		n = 8
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RandomHex returns n random bytes as lowercase hex, used for Reality short IDs.
func RandomHex(n int) (string, error) {
	if n < 2 {
		n = 2
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// RealityKeypair mirrors `sing-box generate reality-keypair`: a raw 32-byte
// X25519 private scalar and its public key, both URL-safe base64 without
// padding.
func RealityKeypair() (privateKey, publicKey string, err error) {
	priv := make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return "", "", err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv), base64.RawURLEncoding.EncodeToString(pub), nil
}

// SS2022Key returns the base64 key a Shadowsocks 2022 method expects. sing-box
// accepts a plain password for the 2022 methods only when it is exactly the
// method key length, so it is generated per key size.
func SS2022Key(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// EnsureCertificate reuses an existing certificate under <root>/bin and
// otherwise writes a self-signed pair whose SANs cover the host so Hysteria2,
// Trojan and VLESS+TLS clients can connect without a CA issued certificate.
func EnsureCertificate(root, host string) (certPath, keyPath string, err error) {
	binDir := filepath.Join(root, "bin")
	certPath = filepath.Join(binDir, "tls.cer")
	keyPath = filepath.Join(binDir, "tls.key")
	if _, statErr := os.Stat(certPath); statErr == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return certPath, keyPath, nil
		}
	}
	if err = os.MkdirAll(binDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create certificate directory: %w", err)
	}
	certPEM, keyPEM, err := SelfSignedCertificate(host)
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("write certificate: %w", err)
	}
	if err = os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write key: %w", err)
	}
	return certPath, keyPath, nil
}

// SelfSignedCertificate builds a ten-year certificate covering host, localhost
// and every IP literal among the supplied names.
func SelfSignedCertificate(hosts ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "sing-box", Organization: []string{"Warden Gateway"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	seen := map[string]bool{}
	addDNS := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen["dns:"+name] {
			return
		}
		seen["dns:"+name] = true
		template.DNSNames = append(template.DNSNames, name)
	}
	addDNS("localhost")
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if ip := net.ParseIP(host); ip != nil {
			if !seen["ip:"+ip.String()] {
				seen["ip:"+ip.String()] = true
				template.IPAddresses = append(template.IPAddresses, ip)
			}
			continue
		}
		addDNS(host)
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

var _ = ed25519.PublicKeySize
var _ = errors.New
