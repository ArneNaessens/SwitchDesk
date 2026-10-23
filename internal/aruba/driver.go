package aruba

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Runner is the subset of an SSH session the collector needs.
type Runner interface {
	Run(ctx context.Context, cmd string, timeout time.Duration) (string, error)
}

type command struct {
	key string
	cmd string
	alt []string // fallbacks tried when the switch rejects cmd
	t   time.Duration
}

func commandsFor(f Family) []command {
	switch f {
	case FamilyAOSCX:
		return []command{
			{key: "system", cmd: "show system"},
			{key: "version", cmd: "show version"},
			{key: "stp", cmd: "show spanning-tree"},
			{key: "ssh", cmd: "show ssh server all-vrfs", alt: []string{"show ssh server"}},
			{key: "vlans", cmd: "show vlan"},
			{key: "interfaces", cmd: "show interface brief"},
			{key: "lldp", cmd: "show lldp neighbor-info detail"},
			{key: "cdp", cmd: "show cdp neighbor-info detail", alt: []string{"show cdp neighbor-info"}},
			{key: "fans", cmd: "show environment fan"},
			{key: "psu", cmd: "show environment power-supply"},
			{key: "temp", cmd: "show environment temperature"},
			{key: "logs", cmd: "show events -r -n 60", alt: []string{"show logging -r -n 60"}},
			{key: "config", cmd: "show running-config", t: 90 * time.Second},
		}
	case FamilyInstantOn:
		// Instant On switches expose a very small CLI; ask for what usually exists.
		return []command{
			{key: "system", cmd: "show system"},
			{key: "version", cmd: "show version"},
			{key: "stp", cmd: "show spanning-tree"},
			{key: "vlans", cmd: "show vlan"},
			{key: "interfaces", cmd: "show interfaces status", alt: []string{"show interfaces brief"}},
			{key: "lldp", cmd: "show lldp neighbors detail", alt: []string{"show lldp neighbors"}},
			{key: "config", cmd: "show running-config", t: 60 * time.Second},
		}
	default:
		return []command{
			{key: "system", cmd: "show system"},
			{key: "version", cmd: "show version"},
			{key: "stp", cmd: "show spanning-tree"},
			{key: "ssh", cmd: "show ip ssh"},
			{key: "vlans", cmd: "show vlans"},
			{key: "interfaces", cmd: "show interfaces brief"},
			{key: "lldp", cmd: "show lldp info remote-device detail"},
			{key: "cdp", cmd: "show cdp neighbors detail"},
			{key: "fans", cmd: "show system fans"},
			{key: "psu", cmd: "show system power-supply"},
			{key: "temp", cmd: "show system temperature"},
			{key: "configstatus", cmd: "show config status"},
			{key: "logs", cmd: "show logging -r", t: 40 * time.Second},
			{key: "config", cmd: "show running-config", t: 90 * time.Second},
		}
	}
}

// Prepare disables paging and widens the terminal. Errors are ignored: not every
// family accepts every command.
func Prepare(ctx context.Context, r Runner, f Family) {
	switch f {
	case FamilyAOSCX:
		r.Run(ctx, "no page", 5*time.Second)
		r.Run(ctx, "terminal-length 1000", 5*time.Second)
	case FamilyInstantOn:
		r.Run(ctx, "terminal length 0", 5*time.Second)
		r.Run(ctx, "terminal datadump", 5*time.Second)
	default:
		r.Run(ctx, "no page", 5*time.Second)
		r.Run(ctx, "terminal width 200", 5*time.Second)
	}
}

// Detect runs "show version" and decides the family unless one is forced.
func Detect(ctx context.Context, r Runner, forced Family, banner string) (Family, string) {
	r.Run(ctx, "no page", 5*time.Second)
	out, _ := r.Run(ctx, "show version", 20*time.Second)
	if forced != "" && forced != FamilyAuto {
		return forced, out
	}
	return DetectFamily(out, banner), out
}

// Collect runs the family's command set and parses everything into a Snapshot.
func Collect(ctx context.Context, r Runner, f Family, versionOut string) Snapshot {
	start := time.Now()
	s := Snapshot{Family: f, Raw: map[string]string{}}
	if versionOut != "" {
		s.Raw["version"] = versionOut
	}
	for _, c := range commandsFor(f) {
		if ctx.Err() != nil {
			s.Errors = append(s.Errors, "collection cancelled")
			break
		}
		if c.key == "version" && versionOut != "" {
			continue
		}
		t := c.t
		if t == 0 {
			t = 25 * time.Second
		}
		out, err := r.Run(ctx, c.cmd, t)
		used := c.cmd
		for _, alt := range c.alt {
			if err == nil && !IsCLIError(out) {
				break
			}
			out, err = r.Run(ctx, alt, t)
			used = alt
		}
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", used, err))
		}
		s.Raw[c.key] = out
	}
	parseInto(&s)
	s.CollectedAt = time.Now()
	s.Duration = time.Since(start).Round(100 * time.Millisecond).String()
	return s
}

func parseInto(s *Snapshot) {
	raw := s.Raw
	s.Facts = ParseSystem(raw["system"])
	if v := ParseSystem(raw["version"]); s.Facts.Software == "" {
		s.Facts.Software = v.Software
	}
	if s.Facts.Software == "" {
		s.Facts.Software = versionLine(raw["version"])
	}
	if s.Facts.Model == "" {
		s.Facts.Model = ModelFromText(raw["version"] + "\n" + raw["system"] + "\n" + raw["config"])
	}
	if s.Facts.Model == "" {
		// AOS-S running-config begins with: ; JL256A Configuration Editor; Created on release #WC.16...
		for _, l := range strings.Split(raw["config"], "\n") {
			if strings.HasPrefix(l, "; ") && strings.Contains(l, "Configuration Editor") {
				s.Facts.Model = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(l, "Configuration Editor", 2)[0], ";"))
				break
			}
		}
	}

	s.STP = ParseSTP(raw["stp"])

	var cfgVLANs []VLAN
	var cfgSTP bool
	switch s.Family {
	case FamilyAOSCX:
		cfgVLANs, s.Security, cfgSTP = ParseConfigCX(raw["config"])
	default:
		cfgVLANs, s.Security, cfgSTP = ParseConfigAOSS(raw["config"])
	}
	if !s.STP.Known && raw["config"] != "" {
		s.STP.Known = true
		s.STP.Enabled = cfgSTP
	}
	if sshOut := raw["ssh"]; sshOut != "" && !IsCLIError(sshOut) {
		m := parseKV(sshOut)
		if v := first(m, "SSH Enabled"); v != "" {
			s.Security.SSHEnabled = strings.HasPrefix(strings.ToLower(v), "yes")
		}
		if v := first(m, "TCP Port Number", "TCP Port"); v != "" {
			s.Security.SSHPort = v
		}
		if v := first(m, "SSH Version", "Version"); v != "" {
			s.Security.SSHVersion = v
		}
	}
	s.VLANs = MergeVLANs(cfgVLANs, ParseVLANTable(raw["vlans"]))
	s.Ports = ParseInterfacesBrief(raw["interfaces"])
	s.Neighbors = append(ParseNeighbors(raw["lldp"], "lldp"), ParseNeighbors(raw["cdp"], "cdp")...)
	s.Env = append(append(ParseEnv("fan", raw["fans"]), ParseEnv("psu", raw["psu"])...), ParseEnv("temp", raw["temp"])...)
	s.Logs = ParseLogs(raw["logs"], 60)
	s.RunningConfig = raw["config"]
	if cs := strings.ToLower(raw["configstatus"]); cs != "" && !IsCLIError(cs) {
		saved := strings.Contains(cs, "same as the startup")
		if strings.Contains(cs, "changed") || strings.Contains(cs, "needs to be saved") || strings.Contains(cs, "differ") {
			saved = false
		}
		s.Facts.ConfigSaved = &saved
	}
	if s.Facts.Hostname == "" {
		for _, l := range strings.Split(raw["config"], "\n") {
			if strings.HasPrefix(l, "hostname ") {
				s.Facts.Hostname = unquote(strings.TrimPrefix(l, "hostname "))
				break
			}
		}
	}
}

func versionLine(v string) string {
	for _, l := range strings.Split(v, "\n") {
		t := strings.TrimSpace(l)
		if cxVersionRe.MatchString(t) || strings.HasPrefix(strings.ToLower(t), "version") {
			return t
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "Image stamp") {
			return t
		}
	}
	return ""
}

// ReadOnlyAllowed is the allow-list for the free-form command console.
func ReadOnlyAllowed(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	if c == "" || strings.ContainsAny(c, "\n\r;") {
		return false
	}
	for _, p := range []string{"show ", "ping ", "traceroute "} {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}
