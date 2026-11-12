package singbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// User is one credential of an inbound. Which fields are set depends on the
// inbound type: VLESS and VMess use UUID, Hysteria2 and Trojan use Password,
// SOCKS uses Username and Password.
type User struct {
	Name     string
	UUID     string
	Password string
	Username string
	Flow     string
}

// TLSInfo is the TLS block that matters to a client: Reality public key, the
// SNI to present and whether a self-signed certificate forces insecure mode.
type TLSInfo struct {
	Enabled     bool
	ServerName  string
	Reality     bool
	PublicKey   string
	ShortID     string
	Fingerprint string
	Insecure    bool
}

// Node is one inbound file under the sing-box configuration directory. A file
// normally carries a single inbound; files with several inbounds keep all of
// them and expose the first one as the primary type.
type Node struct {
	ID        string
	File      string
	Tag       string
	Type      string
	Listen    string
	Port      int
	Enabled   bool
	Inbounds  []map[string]interface{}
	Users     []User
	Transport string
	TLSType   string
	TLS       *TLSInfo
	Error     string
	Modified  time.Time
}

// Check is the outcome of `sing-box check` for the whole configuration.
type Check struct {
	OK       bool
	Output   string
	Checked  time.Time
	Duration time.Duration
}

// Summary is what the panel needs to render the sing-box view.
type Summary struct {
	Installed bool
	Version   string
	Binary    string
	Root      string
	Config    string
	Service   string
	Running   bool
	State     string
	Nodes     []Node
	Check     Check
	Warnings  []string
}

// ShareLink is one client link for one user of one inbound.
type ShareLink struct {
	Label   string `json:"label"`
	URI     string `json:"uri"`
	Inbound int    `json:"inbound"`
	User    int    `json:"user"`
	Error   string `json:"error,omitempty"`
}

// CreateRequest is the payload of a one-click node creation. Only Type is
// required: the name, port and credentials are generated when omitted.
type CreateRequest struct {
	Type   string
	Name   string
	Port   int
	Server string
	Method string
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

func asBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

// parseNode decodes one inbound file. Kept tolerant: a broken file becomes a
// node carrying Error so the panel can show and delete it instead of hiding it.
func parseNode(file string, data []byte, enabled bool) Node {
	id := strings.TrimSuffix(strings.TrimSuffix(file, disabledExt), enabledExt)
	n := Node{
		ID:      id,
		File:    file,
		Enabled: enabled,
		Listen:  "::",
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		n.Error = "配置不是合法 JSON：" + err.Error()
		return n
	}
	for _, raw := range asSlice(doc["inbounds"]) {
		if inb := asMap(raw); inb != nil {
			n.Inbounds = append(n.Inbounds, inb)
		}
	}
	if len(n.Inbounds) == 0 {
		n.Error = "配置里没有 inbounds"
		return n
	}
	first := n.Inbounds[0]
	n.Type = asString(first["type"])
	n.Tag = asString(first["tag"])
	if n.Tag == "" {
		n.Tag = n.ID
	}
	if listen := asString(first["listen"]); listen != "" {
		n.Listen = listen
	}
	n.Port = asInt(first["listen_port"])
	if tls := asMap(first["tls"]); len(tls) > 0 && asBool(tls["enabled"]) {
		info := &TLSInfo{
			Enabled:    true,
			ServerName: asString(tls["server_name"]),
			Insecure:   asBool(tls["insecure"]),
		}
		if reality := asMap(tls["reality"]); len(reality) > 0 && asBool(reality["enabled"]) {
			info.Reality = true
			info.Fingerprint = asString(reality["fingerprint"])
			if ids := asSlice(reality["short_id"]); len(ids) > 0 {
				info.ShortID = asString(ids[0])
			}
			if pub := realityPublicKey(asString(reality["private_key"])); pub != "" {
				info.PublicKey = pub
			}
			if info.ServerName == "" {
				info.ServerName = asString(asMap(reality["handshake"])["server"])
			}
		}
		if info.Fingerprint == "" {
			info.Fingerprint = "chrome"
		}
		n.TLS = info
		n.TLSType = "tls"
		if info.Reality {
			n.TLSType = "reality"
		}
	} else {
		n.TLSType = "none"
	}
	if transport := asMap(first["transport"]); len(transport) > 0 {
		n.Transport = asString(transport["type"])
	}
	n.Users = nodeUsers(n)
	return n
}

// nodeUsers collects credentials across every inbound of the file.
func nodeUsers(n Node) []User {
	var users []User
	for _, inb := range n.Inbounds {
		list := asSlice(inb["users"])
		for _, raw := range list {
			u := asMap(raw)
			users = append(users, User{
				Name:     asString(u["name"]),
				UUID:     asString(u["uuid"]),
				Password: asString(u["password"]),
				Username: asString(u["username"]),
				Flow:     asString(u["flow"]),
			})
		}
		if len(list) == 0 && asString(inb["type"]) == "shadowsocks" {
			password := asString(inb["password"])
			if password == "" && asString(inb["method"]) != "" {
				password = ""
			}
			users = append(users, User{Name: asString(inb["tag"]), Password: password})
		}
	}
	return users
}

// realityPublicKey derives the client public key from the server private key so
// the panel never has to store both.
func realityPublicKey(privateKey string) string {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil || len(raw) != 32 {
		return ""
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(pub)
}

func (n Node) primaryUser() User {
	if len(n.Users) > 0 {
		return n.Users[0]
	}
	return User{}
}

func (n Node) usernameHint() string {
	u := n.primaryUser()
	switch {
	case u.UUID != "":
		return u.UUID
	case u.Username != "":
		return u.Username
	default:
		return u.Password
	}
}

func sortNodes(nodes []Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Port != nodes[j].Port {
			return nodes[i].Port < nodes[j].Port
		}
		return nodes[i].ID < nodes[j].ID
	})
}

var _ = fmt.Sprintf
