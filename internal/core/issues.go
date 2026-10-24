package core

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

func (s Severity) rank() int {
	switch s {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}

type Issue struct {
	SwitchID   string   `json:"switchId"`
	SwitchName string   `json:"switchName"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
}

// evaluate turns one switch's status and last snapshot into issues.
func evaluate(sw store.Switch, st *Status, snap *aruba.Snapshot, set store.Settings) []Issue {
	name := displayName(sw, snap)
	var out []Issue
	add := func(sev Severity, cat, title, detail string) {
		out = append(out, Issue{SwitchID: sw.ID, SwitchName: name, Severity: sev, Category: cat, Title: title, Detail: detail})
	}

	if st.Checked && !st.Online {
		d := "No answer on TCP " + fmt.Sprint(sw.Port)
		if !st.Since.IsZero() {
			d += " since " + st.Since.Format("Jan 2 15:04")
		}
		if st.PingError != "" {
			d += " (" + st.PingError + ")"
		}
		add(Critical, "Reachability", "Switch is offline", d)
	}
	if st.HostKeyChanged {
		add(Critical, "SSH", "SSH host key changed", "The switch presented a different host key than before. If the switch was replaced or reset, trust the new key from the switch page.")
	}
	if st.AuthFailed {
		add(Critical, "SSH", "SSH login failed", st.CollectError)
	} else if st.CollectError != "" && !st.HostKeyChanged && st.Online {
		add(Warning, "SSH", "Data collection failed", st.CollectError)
	}
	if snap == nil || snap.CollectedAt.IsZero() {
		return out
	}

	// spanning tree
	if snap.STP.Known && !snap.STP.Enabled {
		add(Critical, "Spanning tree", "Spanning tree is disabled", "A single looped cable can take this segment down.")
	}
	if snap.STP.Enabled {
		if st.TopologyDelta > 0 {
			add(Warning, "Spanning tree", fmt.Sprintf("%d topology change(s) since last check", st.TopologyDelta), "Last change: "+orDash(snap.STP.LastChange)+". Frequent changes usually mean a flapping link or an edge port without admin-edge.")
		}
		if snap.STP.IsRoot && (snap.STP.BridgePriority == "32768" || snap.STP.BridgePriority == "8") {
			add(Warning, "Spanning tree", "Root bridge elected by default priority", "This switch is root only because of its MAC address. Set an explicit priority on the intended core.")
		}
		var blocking []string
		for _, p := range snap.STP.Ports {
			if p.State == "Blocking" || p.State == "Discarding" {
				blocking = append(blocking, p.Port)
			}
		}
		if len(blocking) > 0 {
			add(Info, "Spanning tree", fmt.Sprintf("%d port(s) blocking", len(blocking)), "Ports: "+strings.Join(blocking, ", ")+". Expected on redundant links.")
		}
	}

	// management plane security
	sec := snap.Security
	if sec.TelnetEnabled {
		fix := "Disable with: no telnet-server"
		if snap.Family == aruba.FamilyAOSCX {
			fix = "Disable with: no telnet-server vrf <vrf>"
		}
		seen := "The switch accepts connections on TCP port 23. "
		if sec.TelnetPort == "" {
			seen = ""
		}
		add(Critical, "Security", "Telnet is enabled", seen+"Credentials travel in clear text. "+fix)
	}
	if snap.Family == aruba.FamilyAOSS && sec.HTTPPlain {
		seen := "The switch answers on TCP port 80. "
		if sec.HTTPPort == "" {
			seen = ""
		}
		add(Warning, "Security", "Plain HTTP web management is enabled", seen+"Use web-management ssl and disable plaintext.")
	}
	if sec.SNMPDefault && !set.IgnoreSNMP {
		add(Critical, "Security", "Default SNMP community in use", "Community \"public\" or \"private\" is configured.")
	}
	if snap.RunningConfig != "" {
		if len(sec.NTPServers) == 0 {
			add(Warning, "Management", "No NTP/SNTP server", "Log timestamps and certificates depend on correct time.")
		}
		if len(sec.LogServers) == 0 {
			add(Info, "Management", "No syslog server", "Events are only kept in the switch's local buffer.")
		}
		if len(sec.AAA) == 0 {
			add(Info, "Management", "Local accounts only", "No RADIUS/TACACS+ authentication configured.")
		}
		if snap.Family == aruba.FamilyAOSS && !sec.ManagerPassword {
			add(Critical, "Security", "No manager password set", "Anyone with console or network access gets full rights.")
		}
	}
	if snap.Facts.ConfigSaved != nil && !*snap.Facts.ConfigSaved {
		add(Warning, "Config", "Unsaved configuration changes", "Running config differs from startup. Changes will be lost on reboot.")
	}

	// hardware
	for _, e := range snap.Env {
		if !e.OK {
			kind := map[string]string{"fan": "Fan", "psu": "Power supply", "temp": "Temperature"}[e.Kind]
			add(Critical, "Hardware", kind+" problem", e.Status)
		}
	}
	if snap.Facts.CPU >= 0 && set.CPUWarn > 0 && snap.Facts.CPU >= set.CPUWarn {
		add(Warning, "Health", fmt.Sprintf("High CPU: %d%%", snap.Facts.CPU), "")
	}
	if snap.Facts.Memory >= 0 && set.MemWarn > 0 && snap.Facts.Memory >= set.MemWarn {
		add(Warning, "Health", fmt.Sprintf("High memory: %d%%", snap.Facts.Memory), "")
	}

	// logs
	major := 0
	var sample string
	for _, l := range snap.Logs {
		if l.Severity == "M" || l.Severity == "E" {
			major++
			if sample == "" {
				sample = l.Text
			}
		}
	}
	if major > 0 {
		add(Warning, "Logs", fmt.Sprintf("%d major/error event(s) in recent log", major), sample)
	}
	if snap.CollectedAt.Before(time.Now().Add(-6 * time.Hour)) {
		add(Info, "SSH", "Data is more than 6 hours old", "Last collected "+snap.CollectedAt.Format("Jan 2 15:04"))
	}
	return out
}

// networkIssues looks across switches (e.g. more than one STP root).
func networkIssues(snaps map[string]*aruba.Snapshot, names map[string]string) []Issue {
	roots := map[string][]string{}
	for id, s := range snaps {
		if s == nil || !s.STP.Enabled || s.STP.RootMAC == "" {
			continue
		}
		roots[s.STP.RootMAC] = append(roots[s.STP.RootMAC], names[id])
	}
	if len(roots) <= 1 {
		return nil
	}
	var parts []string
	for mac, sws := range roots {
		sort.Strings(sws)
		parts = append(parts, fmt.Sprintf("%s (%s)", mac, strings.Join(sws, ", ")))
	}
	sort.Strings(parts)
	return []Issue{{
		Severity: Warning, Category: "Spanning tree", SwitchName: "Network",
		Title:  fmt.Sprintf("%d different spanning-tree roots", len(roots)),
		Detail: "Expected one root per STP domain. Roots seen: " + strings.Join(parts, "; ") + ". Mixed MST regions or isolated segments can cause this.",
	}}
}

func sortIssues(is []Issue) {
	sort.SliceStable(is, func(i, j int) bool {
		if is[i].Severity.rank() != is[j].Severity.rank() {
			return is[i].Severity.rank() < is[j].Severity.rank()
		}
		return is[i].SwitchName < is[j].SwitchName
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func displayName(sw store.Switch, snap *aruba.Snapshot) string {
	if sw.Name != "" {
		return sw.Name
	}
	if snap != nil && snap.Facts.Hostname != "" {
		return snap.Facts.Hostname
	}
	return sw.Host
}
