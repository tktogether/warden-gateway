// Package diagnostics builds the two-part health report shown by the panel:
// one part for the Warden Gateway installation itself, one part for the China
// mainland settings that cn-setup applied to this host.
package diagnostics

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/cn"
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
	CN      Section `json:"cn"`
}

// Checker runs the diagnostics.
type Checker struct {
	Config        func() *config.Config
	ConfigPath    string
	DataDir       string
	ServiceName   string
	ServiceStatus func() (string, string)
	Command       func(name string, args ...string) (string, error)
}

// Run collects both sections.
func (c *Checker) Run(ctx context.Context) Report {
	report := Report{AsOf: time.Now().UTC().Format(time.RFC3339)}
	report.Project = c.project()
	report.CN = c.cnSection(ctx)
	report.OK = report.Project.OK && report.CN.OK
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
	if cfg.DNS.Enabled {
		item := listenItem("dns", "DNS 分流", cfg.DNS.Listen)
		if item.Status == StatusOK && cfg.DNS.GatewayIP == "" {
			item.Status = StatusWarn
			item.Detail += "，未配置分流地址"
		}
		section.Items = append(section.Items, item)
	} else {
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 分流", Status: StatusSkip, Detail: "未启用"})
	}
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

func listenItem(id, label, addr string) Item {
	if err := dialHost(addr); err != nil {
		return Item{ID: id, Label: label, Status: StatusFail, Detail: addr + " " + err.Error()}
	}
	return Item{ID: id, Label: label, Status: StatusOK, Detail: addr}
}

func (c *Checker) cnSection(ctx context.Context) Section {
	section := Section{Key: "cn", Title: "国内机配置", Items: []Item{}}
	status, err := cn.New().Status()
	if err != nil {
		section.Items = append(section.Items, Item{ID: "status", Label: "读取状态", Status: StatusFail, Detail: err.Error()})
		section.OK = false
		return section
	}
	if len(status.Backups) == 0 {
		section.Items = append(section.Items, Item{ID: "backup", Label: "配置备份", Status: StatusSkip, Detail: "尚未执行国内机设置"})
	} else {
		section.Items = append(section.Items, Item{ID: "backup", Label: "配置备份", Status: StatusOK, Detail: fmt.Sprintf("%d 个，最新 %s", len(status.Backups), status.LatestBackup)})
	}
	gateway := ""
	if status.Hosts.Managed {
		gateway = status.Hosts.IP
	}
	cfg := c.cfg()
	if gateway == "" && cfg != nil && cfg.DNS.Enabled {
		gateway = cfg.DNS.GatewayIP
	}
	if status.Hosts.Managed {
		item := Item{ID: "hosts", Label: "hosts 条目", Status: StatusOK, Detail: fmt.Sprintf("%s，%d 个域名", status.Hosts.IP, len(status.Hosts.Domains))}
		if !resolvesTo(status.Hosts.Domains, status.Hosts.IP) {
			item.Status = StatusWarn
			item.Detail += "，解析未全部指向网关"
		}
		section.Items = append(section.Items, item)
	} else {
		section.Items = append(section.Items, Item{ID: "hosts", Label: "hosts 条目", Status: StatusSkip, Detail: "未设置"})
	}
	nameservers := strings.Join(status.DNS.Nameservers, ", ")
	switch {
	case len(status.DNS.Nameservers) == 0:
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 指向", Status: StatusSkip, Detail: "未找到 nameserver"})
	case gateway != "" && status.DNS.Managed == gateway:
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 指向", Status: StatusOK, Detail: nameservers})
	case gateway != "" && status.DNS.GatewayOnly && status.DNS.Nameservers[0] == gateway:
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 指向", Status: StatusOK, Detail: nameservers})
	case gateway != "":
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 指向", Status: StatusWarn, Detail: nameservers + "，未指向网关 " + gateway})
	default:
		section.Items = append(section.Items, Item{ID: "dns", Label: "DNS 指向", Status: StatusSkip, Detail: nameservers + "，未使用网关分流"})
	}
	if status.Docker.Installed && len(status.Docker.Mirrors) > 0 {
		section.Items = append(section.Items, Item{ID: "docker", Label: "Docker 镜像", Status: StatusOK, Detail: strings.Join(status.Docker.Mirrors, ", ")})
	} else if status.Docker.Installed {
		section.Items = append(section.Items, Item{ID: "docker", Label: "Docker 镜像", Status: StatusSkip, Detail: "未配置加速地址"})
	} else {
		section.Items = append(section.Items, Item{ID: "docker", Label: "Docker 镜像", Status: StatusSkip, Detail: "未安装 Docker"})
	}
	if len(status.Git.Mirrors) > 0 {
		section.Items = append(section.Items, Item{ID: "git", Label: "Git 镜像", Status: StatusOK, Detail: strings.Join(status.Git.Mirrors, ", ")})
	} else {
		section.Items = append(section.Items, Item{ID: "git", Label: "Git 镜像", Status: StatusSkip, Detail: "未设置"})
	}
	if status.APT.Mirrored {
		section.Items = append(section.Items, Item{ID: "apt", Label: "APT 源", Status: StatusOK, Detail: strings.Join(status.APT.Hosts, ", ")})
	} else if len(status.APT.Hosts) > 0 {
		section.Items = append(section.Items, Item{ID: "apt", Label: "APT 源", Status: StatusWarn, Detail: strings.Join(status.APT.Hosts, ", ") + "，未使用国内镜像"})
	} else {
		section.Items = append(section.Items, Item{ID: "apt", Label: "APT 源", Status: StatusSkip, Detail: "未找到源地址"})
	}
	if status.BBR.Congestion == "bbr" {
		section.Items = append(section.Items, Item{ID: "bbr", Label: "拥塞控制", Status: StatusOK, Detail: "bbr / " + status.BBR.QDisc})
	} else if status.BBR.Congestion != "" {
		section.Items = append(section.Items, Item{ID: "bbr", Label: "拥塞控制", Status: StatusWarn, Detail: status.BBR.Congestion + " / " + status.BBR.QDisc})
	} else {
		section.Items = append(section.Items, Item{ID: "bbr", Label: "拥塞控制", Status: StatusSkip, Detail: "无法读取"})
	}
	if gateway != "" {
		item := Item{ID: "gateway", Label: "网关连通性", Status: StatusOK, Detail: gateway}
		if err := dialContext(ctx, net.JoinHostPort(gateway, "443")); err != nil {
			item.Status = StatusWarn
			item.Detail = gateway + ":443 " + err.Error()
		}
		section.Items = append(section.Items, item)
	}
	section.OK = sectionHealthy(section)
	if !section.OK && len(status.Backups) == 0 {
		section.Hint = "使用 bootstrap.sh cn-setup 应用国内机设置"
	}
	return section
}

func sectionHealthy(section Section) bool {
	for _, item := range section.Items {
		if item.Status == StatusFail {
			return false
		}
	}
	return true
}

// resolvesTo reports whether every probed domain resolves to ip. Only a few
// representative domains are probed so one slow resolver cannot stall the
// diagnostics request.
func resolvesTo(domains []string, ip string) bool {
	if len(domains) == 0 || ip == "" {
		return false
	}
	limit := len(domains)
	if limit > 3 {
		limit = 3
	}
	for _, domain := range domains[:limit] {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		addrs, err := net.DefaultResolver.LookupHost(ctx, domain)
		cancel()
		if err != nil {
			return false
		}
		found := false
		for _, addr := range addrs {
			if addr == ip {
				found = true
				break
			}
		}
		if !found {
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
