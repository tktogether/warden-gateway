package diagnostics

import (
	"strings"
	"testing"

	"github.com/tktogether/warden-gateway/internal/config"
)

func singboxConfig(enabled bool) *config.Config {
	cfg := config.Default()
	cfg.SingBox.Enabled = enabled
	return &cfg
}

func itemsByID(items []Item) map[string]Item {
	out := make(map[string]Item, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func TestSingboxItemsSkippedWhenDisabled(t *testing.T) {
	checker := &Checker{}
	items := checker.singboxItems(singboxConfig(false))
	if len(items) != 1 || items[0].Status != StatusSkip {
		t.Fatalf("items = %#v, want a single skip", items)
	}
}

func TestSingboxItemsSkipWhenNotInstalled(t *testing.T) {
	checker := &Checker{SingBox: func() SingBoxState { return SingBoxState{Installed: false} }}
	items := itemsByID(checker.singboxItems(singboxConfig(true)))
	if got := items["singbox-install"].Status; got != StatusSkip {
		t.Fatalf("install status = %q, want skip", got)
	}
	if got := items["singbox-service"].Status; got != StatusSkip {
		t.Fatalf("service status = %q, want skip", got)
	}
	if _, ok := items["singbox-drift"]; ok {
		t.Fatal("drift must not be reported before sing-box is installed")
	}
}

func TestSingboxItemsHealthyWhenPortsMatch(t *testing.T) {
	checker := &Checker{SingBox: func() SingBoxState {
		return SingBoxState{
			Installed: true, Version: "1.14.4", Binary: "/etc/sing-box/bin/sing-box",
			Service: "sing-box.service", State: "active", Running: true,
			NodeCount: 2, EnabledNodes: 2,
			Configured: []int{4430, 21621}, Listening: []int{21621, 4430},
			CheckOK: true, CheckSupported: true,
		}
	}}
	items := itemsByID(checker.singboxItems(singboxConfig(true)))
	if got := items["singbox-service"].Status; got != StatusOK {
		t.Fatalf("service status = %q, want ok", got)
	}
	drift, ok := items["singbox-drift"]
	if !ok {
		t.Fatal("drift item missing")
	}
	if drift.Status != StatusOK {
		t.Fatalf("drift status = %q, want ok", drift.Status)
	}
	if !strings.Contains(drift.Detail, "4430") || !strings.Contains(drift.Detail, "21621") {
		t.Fatalf("drift detail = %q, want both ports listed in order", drift.Detail)
	}
}

func TestSingboxItemsFlagsMissingPortAsFailure(t *testing.T) {
	checker := &Checker{SingBox: func() SingBoxState {
		return SingBoxState{
			Installed: true, Running: true, State: "active",
			NodeCount: 1, EnabledNodes: 1,
			Configured: []int{4430}, Listening: []int{},
			CheckOK: true, CheckSupported: true,
		}
	}}
	items := itemsByID(checker.singboxItems(singboxConfig(true)))
	drift := items["singbox-drift"]
	if drift.Status != StatusFail {
		t.Fatalf("drift status = %q, want fail", drift.Status)
	}
	if !strings.Contains(drift.Detail, "配置了但未监听：4430") {
		t.Fatalf("drift detail = %q", drift.Detail)
	}
}

func TestSingboxItemsWarnsOnPendingRestart(t *testing.T) {
	checker := &Checker{SingBox: func() SingBoxState {
		return SingBoxState{
			Installed: true, Running: true, State: "active",
			NodeCount: 1, EnabledNodes: 1,
			Configured: []int{4430}, Listening: []int{4430},
			Pending: true, ChangedAt: "2026-11-13T09:00:00Z", StartedAt: "2026-11-13T08:00:00Z",
			CheckOK: true, CheckSupported: true,
		}
	}}
	items := itemsByID(checker.singboxItems(singboxConfig(true)))
	drift := items["singbox-drift"]
	if drift.Status != StatusWarn {
		t.Fatalf("drift status = %q, want warn", drift.Status)
	}
	if !strings.Contains(drift.Detail, "2026-11-13T09:00:00Z") {
		t.Fatalf("drift detail = %q", drift.Detail)
	}
	if !strings.Contains(drift.Detail, "重启 sing-box 生效") {
		t.Fatalf("drift detail = %q", drift.Detail)
	}
}

func TestSingboxItemsReportsFailedCheck(t *testing.T) {
	checker := &Checker{SingBox: func() SingBoxState {
		return SingBoxState{
			Installed: true, Running: true, State: "active",
			NodeCount: 1, EnabledNodes: 1,
			Configured: []int{4430}, Listening: []int{4430},
			CheckOK: false, CheckOutput: "decode config: unexpected end of JSON input", CheckSupported: true,
		}
	}}
	items := itemsByID(checker.singboxItems(singboxConfig(true)))
	check := items["singbox-check"]
	if check.Status != StatusFail {
		t.Fatalf("check status = %q, want fail", check.Status)
	}
	if !strings.Contains(check.Detail, "unexpected end of JSON input") {
		t.Fatalf("check detail = %q", check.Detail)
	}
}
