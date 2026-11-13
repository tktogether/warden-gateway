// Package diagnostics builds the health report shown by the panel for the
// Warden Gateway installation itself.
package diagnostics

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

// Status values used by every item.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Item is one check result.
type Item struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Section groups the checks of one part.
type Section struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	OK    bool   `json:"ok"`
	Items []Item `json:"items"`
	Hint  string `json:"hint,omitempty"`
}

// Report is the full diagnostics payload.
type Report struct {
	AsOf    string  `json:"as_of"`
	OK      bool    `json:"ok"`
	Project Section `json:"project"`
}

// DNSState is the live listener state supplied by the panel.
type DNSState struct {
	Running  bool
	Listen   string
	Port     string
	Fallback bool
	Error    string
}

// Checker runs the diagnostics.
type Checker struct {
	Config        func() *config.Config
	ConfigPath    string
	DataDir       string
	ServiceName   string
	ServiceStatus func() (string, string)
	Command       func(name string, args ...string) (string, error)
	DNSState      func() DNSState
	SingBox       func() SingBoxState
}

// Run collects the report.
func (c *Checker) Run(_ context.Context) Report {
	report := Report{AsOf: time.Now().UTC().Format(time.RFC3339)}
	report.Project = c.project()
	report.OK = report.Project.OK
	return report
}

func (c *Checker) cfg() *config.Config {
	if c.Config == nil {
		return nil
	}
	return c.Config()
}

func (c *Checker) project() Section {
	section := Section{Key: "project", Title: "项目自身", Items: []Item{}}
	cfg := c.cfg()
	if cfg == nil {
		section.Items = append(section.Items, Item{ID: "config", Label: "配置文件", Status: StatusFail, Detail: "无法读取配置"})
		section.OK = false
		return section
	}
	section.Items = append(section.Items, Item{ID: "config", Label: "配置文件", Status: StatusOK, Detail: cfg.PanelURL()})
	if err := cfg.Validate(); err != nil {
		section.Items = append(section.Items, Item{ID: "validate", Label: "配置校验", Status: StatusFail, Detail: err.Error()})
	} else {
		section.Items = append(section.Items, Item{ID: "validate", Label: "配置校验", Status: StatusOK, Detail: "通过"})
	}
	if c.ServiceStatus != nil {
		active, detail := c.ServiceStatus()
		status := StatusFail
		if active == "active" {
			status = StatusOK
		}
		section.Items = append(section.Items, Item{ID: "service", Label: "服务状态", Status: status, Detail: strings.TrimSpace(active + " " + detail)})
	}
	if cfg.Listen.HTTPS != "" {
		section.Items = append(section.Items, listenItem("listen-panel", "面板监听", cfg.Listen.HTTPS))
	}
	if cfg.TLS.Mode == "auto" && cfg.Listen.ACME != "" {
		section.Items = append(section.Items, listenItem("listen-acme", "ACME 监听", cfg.Listen.ACME))
	} else {
		section.Items = append(section.Items, Item{ID: "tls", Label: "TLS 模式", Status: StatusOK, Detail: cfg.TLS.Mode})
	}
	if cfg.SNI.Enabled && cfg.Listen.Entry != "" {
		section.Items = append(section.Items, listenItem("sni", "SNI 入口", cfg.Listen.Entry))
	} else if cfg.SNI.Enabled {
		section.Items = append(section.Items, Item{ID: "sni", Label: "SNI 入口", Status: StatusFail, Detail: "未配置监听地址"})
	} else {
		section.Items = append(section.Items, Item{ID: "sni", Label: "SNI 入口", Status: StatusSkip, Detail: "未启用"})
	}
	if cfg.Docker.Enabled && cfg.Listen.HTTP != "" {
		section.Items = append(section.Items, listenItem("docker", "Docker 镜像代理", cfg.Listen.HTTP))
	} else if cfg.Docker.Enabled {
		section.Items = append(section.Items, Item{ID: "docker", Label: "Docker 镜像代理", Status: StatusFail, Detail: "缺少本地 HTTP 监听"})
	} else {
		section.Items = append(section.Items, Item{ID: "docker", Label: "Docker 镜像代理", Status: StatusSkip, Detail: "未启用"})
	}
	if cfg.GitHub.Enabled {
		section.Items = append(section.Items, Item{ID: "github", Label: "GitHub 下载代理", Status: StatusOK, Detail: fmt.Sprintf("允许 %d 个域名", len(cfg.GitHub.AllowHosts))})
	} else {
		section.Items = append(section.Items, Item{ID: "github", Label: "GitHub 下载代理", Status: StatusSkip, Detail: "未启用"})
	}
	section.Items = append(section.Items, c.dnsItem(cfg))
	section.Items = append(section.Items, c.singboxItems(cfg)...)
	if c.DataDir != "" {
		if _, err := os.Stat(c.DataDir); err != nil {
			section.Items = append(section.Items, Item{ID: "data", Label: "数据目录", Status: StatusFail, Detail: err.Error()})
		} else {
			section.Items = append(section.Items, Item{ID: "data", Label: "数据目录", Status: StatusOK, Detail: c.DataDir})
		}
	}
	section.OK = sectionHealthy(section)
	return section
}

func (c *Checker) dnsItem(cfg *config.Config) Item {
	if !cfg.DNS.Enabled {
		return Item{ID: "dns", Label: "DNS 分流", Status: StatusSkip, Detail: "未启用"}
	}
	state := DNSState{}
	if c.DNSState != nil {
		state = c.DNSState()
	}
	if state.Error != "" {
		return Item{ID: "dns", Label: "DNS 分流", Status: StatusFail, Detail: state.Error}
	}
	addr := cfg.DNS.Listen
	if state.Listen != "" {
		addr = state.Listen
	}
	item := listenItem("dns", "DNS 分流", addr)
	if item.Status == StatusOK && state.Fallback {
		item.Status = StatusWarn
		item.Detail += "，53 端口被占用已回退"
	}
	if item.Status == StatusOK && cfg.DNS.GatewayIP == "" {
		item.Status = StatusWarn
		item.Detail += "，未配置分流地址"
	}
	return item
}

func listenItem(id, label, addr string) Item {
	if err := dialHost(addr); err != nil {
		return Item{ID: id, Label: label, Status: StatusFail, Detail: addr + " " + err.Error()}
	}
	return Item{ID: id, Label: label, Status: StatusOK, Detail: addr}
}

func sectionHealthy(section Section) bool {
	for _, item := range section.Items {
		if item.Status == StatusFail {
			return false
		}
	}
	return true
}

func dialHost(addr string) error {
	return dialContext(context.Background(), addr)
}

func dialContext(ctx context.Context, addr string) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}
