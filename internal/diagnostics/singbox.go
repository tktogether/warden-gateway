package diagnostics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tktogether/warden-gateway/internal/config"
)

// SingBoxState is the live sing-box information supplied by the panel.
type SingBoxState struct {
	Installed      bool
	Version        string
	Binary         string
	Service        string
	Running        bool
	State          string
	NodeCount      int
	EnabledNodes   int
	Configured     []int
	Listening      []int
	CheckOK        bool
	CheckOutput    string
	CheckSupported bool
	Error          string
}

// singboxItems reports the node service the panel manages. A host without
// sing-box is not a fault: the feature is optional, so everything is skipped
// instead of failing the report.
func (c *Checker) singboxItems(cfg *config.Config) []Item {
	if !cfg.SingBox.Enabled {
		return []Item{{ID: "singbox", Label: "sing-box 节点", Status: StatusSkip, Detail: "未启用"}}
	}
	state := SingBoxState{}
	if c.SingBox != nil {
		state = c.SingBox()
	}
	items := make([]Item, 0, 5)
	if state.Error != "" {
		items = append(items, Item{ID: "singbox", Label: "sing-box 节点", Status: StatusWarn, Detail: state.Error})
	}
	if !state.Installed {
		items = append(items, Item{
			ID: "singbox-install", Label: "sing-box 内核", Status: StatusSkip,
			Detail: "未安装，可在面板“节点”页一键安装",
		})
		items = append(items, Item{ID: "singbox-service", Label: "sing-box 服务", Status: StatusSkip, Detail: "未安装"})
		return items
	}
	detail := state.Version
	if state.Binary != "" {
		detail = joinNonEmpty(detail, state.Binary)
	}
	items = append(items, Item{ID: "singbox-install", Label: "sing-box 内核", Status: StatusOK, Detail: detail})

	serviceDetail := joinNonEmpty(state.Service, state.State)
	serviceStatus := StatusOK
	if !state.Running {
		serviceStatus = StatusFail
		if serviceDetail == "" {
			serviceDetail = "未运行"
		}
	}
	items = append(items, Item{ID: "singbox-service", Label: "sing-box 服务", Status: serviceStatus, Detail: serviceDetail})

	nodeDetail := fmt.Sprintf("%d 个节点，%d 个启用", state.NodeCount, state.EnabledNodes)
	nodeStatus := StatusOK
	if state.NodeCount == 0 {
		nodeStatus = StatusWarn
		nodeDetail = "还没有节点"
	}
	items = append(items, Item{ID: "singbox-nodes", Label: "sing-box 节点", Status: nodeStatus, Detail: nodeDetail})

	if state.CheckSupported {
		checkItem := Item{ID: "singbox-check", Label: "节点配置校验", Status: StatusOK, Detail: "通过"}
		if !state.CheckOK {
			checkItem.Status = StatusFail
			checkItem.Detail = strings.TrimSpace(state.CheckOutput)
			if checkItem.Detail == "" {
				checkItem.Detail = "sing-box check 失败"
			}
		}
		items = append(items, checkItem)
	}
	items = append(items, driftItem(state))
	return items
}

// driftItem compares configured and listening ports so a configuration that the
// running process has not picked up is visible before it surprises an operator.
func driftItem(state SingBoxState) Item {
	item := Item{ID: "singbox-drift", Label: "配置与进程一致性"}
	configured := uniquePorts(state.Configured)
	listening := uniquePorts(state.Listening)
	if len(configured) == 0 && len(listening) == 0 {
		item.Status = StatusSkip
		item.Detail = "没有可比较的端口"
		return item
	}
	missing, extra := missingExtra(configured, listening)
	if len(missing) == 0 && len(extra) == 0 {
		item.Status = StatusOK
		item.Detail = "监听端口与配置一致：" + joinPorts(listening)
		return item
	}
	parts := make([]string, 0, 2)
	if len(missing) > 0 {
		parts = append(parts, "配置了但未监听："+joinPorts(missing)+"（重启 sing-box 生效）")
	}
	if len(extra) > 0 {
		parts = append(parts, "仍在监听但配置已移除："+joinPorts(extra))
	}
	if len(missing) > 0 {
		item.Status = StatusFail
	} else {
		item.Status = StatusWarn
	}
	item.Detail = strings.Join(parts, "；")
	return item
}

func missingExtra(configured, listening []int) (missing, extra []int) {
	have := make(map[int]bool, len(listening))
	for _, port := range listening {
		have[port] = true
	}
	want := make(map[int]bool, len(configured))
	for _, port := range configured {
		want[port] = true
		if !have[port] {
			missing = append(missing, port)
		}
	}
	for _, port := range listening {
		if !want[port] {
			extra = append(extra, port)
		}
	}
	sort.Ints(missing)
	sort.Ints(extra)
	return missing, extra
}

func uniquePorts(ports []int) []int {
	seen := make(map[int]bool, len(ports))
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

func joinPorts(ports []int) string {
	if len(ports) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, "、")
}

func joinNonEmpty(values ...string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, " · ")
}
