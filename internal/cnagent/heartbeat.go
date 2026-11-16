package cnagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ProtocolVersion is the heartbeat wire protocol this agent speaks. The
// gateway accepts the current and the previous version (spec §9.6); the
// agent upgrades in lockstep with the gateway, so it always sends exactly
// this version.
const ProtocolVersion = 1

// heartbeatPath is the control-plane endpoint the agent reports to.
const heartbeatPath = "/api/agent/heartbeat"

// Report is the heartbeat request body. The top-level protocol field is
// stamped by SendHeartbeat; the remaining fields are filled by the agent run
// loop (probe round, DNS state, sampled metrics, workload discovery). Field
// names on the wire are snake_case and shared with the gateway side.
type Report struct {
	Protocol      int            `json:"protocol"`
	MachineID     string         `json:"machine_id"`
	Hostname      string         `json:"hostname"`
	AgentVersion  string         `json:"agent_version"`
	BootTime      string         `json:"boot_time"`              // RFC3339 UTC
	UptimeSeconds int64          `json:"uptime_seconds"`         // seconds since boot
	Probe         Probe          `json:"probe"`                  // last gateway probe round
	DNS           DNS            `json:"dns"`                    // local stub resolver state
	DomainsHash   string         `json:"domains_hash"`           // hash of the locally applied split-domain list
	System        System         `json:"system"`                 // disk/memory/load sample
	Workloads     []Workload     `json:"workloads"`              // discovered docker containers
	LastCommand   *CommandResult `json:"last_command,omitempty"` // result of the command executed in the previous cycle
}

// Probe mirrors ProbeResult on the wire. CheckedAt is an RFC3339 UTC string.
type Probe struct {
	Result    string `json:"result"`
	CheckedAt string `json:"checked_at"`
	Detail    string `json:"detail,omitempty"`
}

// DNS reports the local stub resolver state so the panel can see whether the
// CN machine is serving the split at the expected address or has degraded to
// plain upstream forwarding.
type DNS struct {
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Degraded bool   `json:"degraded"`
}

// System carries the sampled disk/memory/load metrics (filled by
// sampleSystem in sysmetrics.go); percentages are 0-100.
type System struct {
	DiskUsedPct float64 `json:"disk_used_pct"`
	MemUsedPct  float64 `json:"mem_used_pct"`
	Load1       float64 `json:"load1"`
}

// Workload is one discovered docker container. It is part of the wire
// protocol, which is why it is defined here; DiscoverWorkloads (workload.go)
// returns []Workload.
type Workload struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Started string `json:"started"`
	Coolify bool   `json:"coolify"`
}

// Command is one queued instruction from the gateway.
type Command struct {
	ID     string            `json:"id"`
	Action string            `json:"action"`
	Params map[string]string `json:"params,omitempty"`
}

// CommandResult is the agent's acknowledgement of an executed command; the
// run loop puts it into Report.LastCommand so the gateway sees the outcome
// with the next heartbeat.
type CommandResult struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// DomainPayload is the split-domain list the gateway pushes down. A nil
// Domains in the reply means the agent's DomainsHash already matches.
type DomainPayload struct {
	Hash    string   `json:"hash"`
	Domains []string `json:"domains"`
}

// HeartbeatReply is the gateway's response to a heartbeat.
type HeartbeatReply struct {
	Commands      []Command      `json:"commands"`
	Domains       *DomainPayload `json:"domains"`
	TargetVersion string         `json:"target_version"`
}

// StatusError is the structured error for a rejected heartbeat. Callers use
// errors.As to get it and tell HTTP rejections (401 revoked, 429 rate
// limited, 5xx gateway trouble) apart from transport failures.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("heartbeat: gateway returned status %d", e.Code)
}

// SendHeartbeat posts report to the gateway heartbeat endpoint and returns
// the parsed reply. It stamps ProtocolVersion on a copy of the report, sends
// the token as a Bearer Authorization header, and treats any non-2xx status
// as a *StatusError carrying the status code. A nil httpClient means
// http.DefaultClient.
func SendHeartbeat(ctx context.Context, cfg *Config, report *Report, httpClient *http.Client) (*HeartbeatReply, error) {
	if report == nil {
		return nil, errors.New("heartbeat: report is nil")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// Stamp the wire protocol version on a copy so the caller's struct is
	// never mutated and the field is always exactly ProtocolVersion.
	msg := *report
	msg.Protocol = ProtocolVersion
	body, err := json.Marshal(&msg)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: encode report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.Origin, "/")+heartbeatPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("heartbeat: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: post report: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Code: resp.StatusCode}
	}

	var reply HeartbeatReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("heartbeat: parse reply: %w", err)
	}
	return &reply, nil
}
