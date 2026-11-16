package cnagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const (
	// stateFreshness is the maximum age (by the state file's updated_at) the
	// self-check accepts before the agent counts as stale — the run loop
	// touches state.json once per heartbeat cycle (about every 10s), so a
	// file older than 5 minutes means the agent stopped writing.
	stateFreshness = 5 * time.Minute

	// selfCheckDNSTimeout bounds the probe DNS query (2s, ruling 3): the
	// check runs inside heartbeat command handling and must never stall a
	// cycle.
	selfCheckDNSTimeout = 2 * time.Second

	// fallbackCheckDomain is queried when the state file carries no domain
	// list: a name the agent's stub resolver must be able to answer.
	fallbackCheckDomain = "github.com"
)

// checkEntry is one named result line inside the SelfCheck report.
type checkEntry struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// stateSnapshot is the subset of the state file the self-check consumes. It
// is decoded loosely so the check keeps working when the state format gains
// fields; the current agentState writer emits no domains list (only the count
// and hash), so the DNS check falls back to fallbackCheckDomain unless a
// writer includes the list.
type stateSnapshot struct {
	UpdatedAt   string   `json:"updated_at"`
	ProbeResult string   `json:"probe_result"`
	ProbeDetail string   `json:"probe_detail,omitempty"`
	Domains     []string `json:"domains,omitempty"`
}

// SelfCheck runs the local health checks that the agent's self-check
// heartbeat command and warden-cn cn-check (Task 14) report on:
//
//   - state: statePath exists and is fresh (updated_at < 5 minutes old);
//   - probe: the recorded gateway probe verdict is OK;
//   - dns: a real A query (miekg/dns, UDP, 2s timeout) against the configured
//     DNS listen — DefaultDNSListen when cfg.DNSListen is empty — for the
//     first domain in the state file's list (github.com when there is none)
//     comes back with an answer;
//   - config_mode: agent.json (default /etc/warden-gateway-cn/agent.json) is
//     mode 0600, because the file carries the machine token (skipped on
//     Windows and when the file does not exist).
//
// It returns {"ok": bool, "checks": []checkEntry, "as_of": RFC3339}; ok is
// true only when every check passed. Callers that run on the agent itself
// (checkFn) hand in the agent's actually bound DNS listen via cfg.DNSListen.
func SelfCheck(ctx context.Context, cfg *Config, statePath string) map[string]interface{} {
	if cfg == nil {
		cfg = &Config{}
	}
	st, stateEntry, probeEntry := checkStateFile(statePath)
	checks := []checkEntry{
		stateEntry,
		probeEntry,
		checkDNS(ctx, cfg, firstDomain(st)),
		checkConfigMode(),
	}
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	return map[string]interface{}{
		"ok":     ok,
		"checks": checks,
		"as_of":  time.Now().UTC().Format(time.RFC3339),
	}
}

// checkStateFile reads and judges the state file. It returns the parsed
// snapshot plus the state (freshness) and probe entries; a missing or
// unparseable file fails both.
func checkStateFile(statePath string) (stateSnapshot, checkEntry, checkEntry) {
	var st stateSnapshot

	data, err := os.ReadFile(statePath)
	if err != nil {
		return st,
			checkEntry{Name: "state", OK: false, Detail: fmt.Sprintf("read %s: %v", statePath, err)},
			checkEntry{Name: "probe", OK: false, Detail: "no state file, no probe verdict"}
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st,
			checkEntry{Name: "state", OK: false, Detail: fmt.Sprintf("parse %s: %v", statePath, err)},
			checkEntry{Name: "probe", OK: false, Detail: "unparseable state, no probe verdict"}
	}
	ts, err := time.Parse(time.RFC3339, st.UpdatedAt)
	if err != nil {
		return st,
			checkEntry{Name: "state", OK: false, Detail: fmt.Sprintf("updated_at %q in %s is not RFC3339", st.UpdatedAt, statePath)},
			checkEntry{Name: "probe", OK: false, Detail: "state has no usable probe timestamp"}
	}

	age := time.Since(ts) // a slightly future updated_at (clock skew) counts as fresh
	fresh := age < stateFreshness
	detail := fmt.Sprintf("state file %s updated_at %s (age %s)", statePath, st.UpdatedAt, age.Round(time.Second))
	if !fresh {
		detail = fmt.Sprintf("state file %s is stale: updated_at %s is %s old (limit %s)",
			statePath, st.UpdatedAt, age.Round(time.Second), stateFreshness)
	}

	probeDetail := "no probe result recorded"
	if st.ProbeResult != "" {
		probeDetail = "probe " + st.ProbeResult
		if st.ProbeDetail != "" {
			probeDetail += ": " + st.ProbeDetail
		}
	}
	return st,
		checkEntry{Name: "state", OK: fresh, Detail: detail},
		checkEntry{Name: "probe", OK: st.ProbeResult == ProbeOK, Detail: probeDetail}
}

// firstDomain returns the first usable split-domain from the state snapshot,
// falling back to a well-known public name when the list is empty.
func firstDomain(st stateSnapshot) string {
	for _, d := range st.Domains {
		if d = strings.TrimSpace(d); d != "" {
			return d
		}
	}
	return fallbackCheckDomain
}

// checkDNS sends one real A query to the configured DNS listen. A UDP write
// alone proves nothing (dials always succeed), so the check only passes when
// the server answers with NOERROR and at least one record.
func checkDNS(ctx context.Context, cfg *Config, name string) checkEntry {
	listen := strings.TrimSpace(cfg.DNSListen)
	if listen == "" {
		listen = DefaultDNSListen
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	cl := &dns.Client{Net: "udp", Timeout: selfCheckDNSTimeout}
	resp, _, err := cl.ExchangeContext(ctx, m, listen)
	if err != nil {
		return checkEntry{Name: "dns", OK: false, Detail: fmt.Sprintf("query %s via udp %s: %v", name, listen, err)}
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) == 0 {
		return checkEntry{Name: "dns", OK: false, Detail: fmt.Sprintf(
			"query %s via udp %s: rcode=%s answers=%d", name, listen, dns.RcodeToString[resp.Rcode], len(resp.Answer))}
	}
	return checkEntry{Name: "dns", OK: true, Detail: fmt.Sprintf(
		"query %s via udp %s answered with %d record(s)", name, listen, len(resp.Answer))}
}

// checkConfigMode verifies agent.json is 0600 (the token must not be
// world-readable). It runs only where POSIX file modes exist and only when
// the file is present; a missing file is not a leak, so the check passes as
// skipped.
func checkConfigMode() checkEntry {
	if runtime.GOOS == "windows" {
		return checkEntry{Name: "config_mode", OK: true, Detail: "skipped: no POSIX file modes on " + runtime.GOOS}
	}
	fi, err := os.Stat(defaultConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return checkEntry{Name: "config_mode", OK: true, Detail: defaultConfigPath + " not found (skipped)"}
		}
		return checkEntry{Name: "config_mode", OK: false, Detail: fmt.Sprintf("stat %s: %v", defaultConfigPath, err)}
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return checkEntry{Name: "config_mode", OK: false, Detail: fmt.Sprintf(
			"%s mode is %04o, want 0600 (the file carries the machine token)", defaultConfigPath, perm)}
	}
	return checkEntry{Name: "config_mode", OK: true, Detail: fmt.Sprintf("%s mode 0600", defaultConfigPath)}
}
