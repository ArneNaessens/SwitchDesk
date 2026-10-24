package core

import (
	"sort"
	"strings"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

type Node struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"` // switch, external-switch, router, ap, phone, host
	Managed  bool     `json:"managed"`
	Host     string   `json:"host"`
	Model    string   `json:"model"`
	Online   bool     `json:"online"`
	Disabled bool     `json:"disabled"`
	Severity Severity `json:"severity"`
	Issues   int      `json:"issues"`
	StpRoot  bool     `json:"stpRoot"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
}

type Edge struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	FromPort  string   `json:"fromPort"`
	ToPort    string   `json:"toPort"`
	Protocols []string `json:"protocols"`
	Blocking  bool     `json:"blocking"`
	Up        bool     `json:"up"`
}

type Topology struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type switchInfo struct {
	sw     store.Switch
	snap   *aruba.Snapshot
	online bool
	issues []Issue
}

func buildTopology(infos []switchInfo, layout map[string]store.Point, showEndpoints bool) Topology {
	idx := map[string]string{}           // match key -> node id
	macs := map[string]map[string]bool{} // node id -> MACs that switch is known by
	nodes := map[string]*Node{}
	for _, in := range infos {
		n := &Node{ID: in.sw.ID, Label: displayName(in.sw, in.snap), Kind: "switch", Managed: true, Host: in.sw.Host, Online: in.online, Disabled: in.sw.Disabled}
		idx["ip:"+in.sw.Host] = in.sw.ID
		if in.snap != nil {
			n.Model = in.snap.Facts.Model
			n.StpRoot = in.snap.STP.Enabled && in.snap.STP.IsRoot
			if h := strings.ToLower(in.snap.Facts.Hostname); h != "" {
				idx["name:"+h] = in.sw.ID
			}
			for _, m := range []string{in.snap.Facts.BaseMAC, in.snap.STP.BridgeMAC} {
				if mac := aruba.NormMAC(m); mac != "" {
					idx["mac:"+mac] = in.sw.ID
					if macs[in.sw.ID] == nil {
						macs[in.sw.ID] = map[string]bool{}
					}
					macs[in.sw.ID][mac] = true
				}
			}
		}
		if n.Label != "" {
			idx["name:"+strings.ToLower(n.Label)] = in.sw.ID
		}
		n.Issues = len(in.issues)
		n.Severity = Info
		for _, is := range in.issues {
			if is.Severity.rank() < n.Severity.rank() {
				n.Severity = is.Severity
			}
		}
		if len(in.issues) == 0 {
			n.Severity = ""
		}
		nodes[n.ID] = n
	}

	// A chassis MAC is unique; an IP address or a name isn't. Branches behind
	// routers often reuse the same private subnets and switch names, so a
	// neighbor that matches a managed switch by IP or name is only taken to be
	// that switch when its chassis MAC doesn't say otherwise.
	// A stack can advertise a MAC we never read, so a disagreeing MAC is still
	// accepted when the other switch reports the link back.
	byID := map[string]switchInfo{}
	for _, in := range infos {
		byID[in.sw.ID] = in
	}
	seesBack := func(id, reporter string) bool {
		other, me := byID[id], byID[reporter]
		if other.snap == nil {
			return false
		}
		for _, nb := range other.snap.Neighbors {
			if (nb.MgmtIP != "" && nb.MgmtIP == me.sw.Host) || macs[reporter][aruba.NormMAC(nb.ChassisID)] {
				return true
			}
		}
		return false
	}
	chassis := func(nb aruba.Neighbor) string { return aruba.NormMAC(nb.ChassisID) }
	var reporter string
	agrees := func(id string, nb aruba.Neighbor) bool {
		mac := chassis(nb)
		if mac == "" || len(macs[id]) == 0 || macs[id][mac] {
			return true
		}
		return seesBack(id, reporter)
	}
	resolve := func(nb aruba.Neighbor) (string, bool) {
		if mac := chassis(nb); mac != "" {
			if id, ok := idx["mac:"+mac]; ok {
				return id, true
			}
		}
		if nb.MgmtIP != "" {
			if id, ok := idx["ip:"+nb.MgmtIP]; ok && agrees(id, nb) {
				return id, true
			}
		}
		name := strings.ToLower(nb.SysName)
		if name != "" {
			if id, ok := idx["name:"+name]; ok && agrees(id, nb) {
				return id, true
			}
			// CDP device IDs are often FQDNs
			if i := strings.IndexByte(name, '.'); i > 0 {
				if id, ok := idx["name:"+name[:i]]; ok && agrees(id, nb) {
					return id, true
				}
			}
		}
		key := ""
		switch {
		case aruba.NormMAC(nb.ChassisID) != "":
			key = "ext:" + aruba.NormMAC(nb.ChassisID)
		case name != "":
			key = "ext:" + name
		case nb.MgmtIP != "":
			key = "ext:" + nb.MgmtIP
		default:
			return "", false
		}
		// one external node per name even if chassis ids differ (stacks)
		if name != "" {
			if id, ok := idx["extname:"+name]; ok {
				return id, false
			}
			idx["extname:"+name] = key
		}
		return key, false
	}

	edges := map[string]*Edge{}
	for _, in := range infos {
		if in.snap == nil {
			continue
		}
		blocking := map[string]bool{}
		for _, p := range in.snap.STP.Ports {
			if p.State == "Blocking" || p.State == "Discarding" {
				blocking[p.Port] = true
			}
		}
		portUp := map[string]bool{}
		for _, p := range in.snap.Ports {
			portUp[p.Name] = p.Up
		}
		reporter = in.sw.ID
		for _, nb := range in.snap.Neighbors {
			to, managed := resolve(nb)
			if to == "" || to == in.sw.ID {
				continue
			}
			kind := classify(nb)
			if !managed {
				if !showEndpoints && (kind == "phone" || kind == "host") {
					continue
				}
				if _, ok := nodes[to]; !ok {
					label := nb.SysName
					if label == "" {
						label = nb.MgmtIP
					}
					nodes[to] = &Node{ID: to, Label: label, Kind: kind, Host: nb.MgmtIP, Model: shorten(nb.Description), Online: true}
				}
			}
			remotePort := nb.PortID
			if aruba.NormMAC(remotePort) != "" && nb.PortDescr != "" {
				remotePort = nb.PortDescr
			}
			a, pa, b, pb := in.sw.ID, nb.LocalPort, to, remotePort
			if a > b {
				a, pa, b, pb = b, pb, a, pa
			}
			key := a + "|" + pa + "|" + b + "|" + pb
			// if the other side already reported this link with a different
			// remote-port spelling, merge on the port we know is exact
			if _, ok := edges[key]; !ok {
				for k, e := range edges {
					if (e.From == in.sw.ID && e.To == to && e.FromPort == nb.LocalPort) || (e.To == in.sw.ID && e.From == to && e.ToPort == nb.LocalPort) {
						key = k
						break
					}
				}
			}
			e, ok := edges[key]
			if !ok {
				e = &Edge{ID: key, From: a, To: b, FromPort: pa, ToPort: pb, Up: true}
				edges[key] = e
			}
			if !contains(e.Protocols, nb.Protocol) {
				e.Protocols = append(e.Protocols, nb.Protocol)
			}
			if blocking[nb.LocalPort] {
				e.Blocking = true
			}
			if up, ok := portUp[nb.LocalPort]; ok && !up {
				e.Up = false
			}
		}
	}

	t := Topology{}
	for _, n := range nodes {
		if p, ok := layout[n.ID]; ok {
			x, y := p.X, p.Y
			n.X, n.Y = &x, &y
		}
		t.Nodes = append(t.Nodes, *n)
	}
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].ID < t.Nodes[j].ID })
	for _, e := range edges {
		t.Edges = append(t.Edges, *e)
	}
	sort.Slice(t.Edges, func(i, j int) bool { return t.Edges[i].ID < t.Edges[j].ID })
	return t
}

func classify(nb aruba.Neighbor) string {
	d := strings.ToLower(nb.Description + " " + nb.SysName)
	c := strings.ToLower(nb.Capabilities)
	switch {
	case strings.Contains(d, "phone") || strings.HasPrefix(strings.ToLower(nb.SysName), "sep") || strings.Contains(c, "telephone"):
		return "phone"
	case strings.Contains(d, "access point") || strings.Contains(d, "air-ap") || strings.Contains(d, "instant on ap") ||
		looksLikeAP(d) || strings.Contains(c, "wlan"):
		return "ap"
	case strings.Contains(c, "bridge") || strings.Contains(c, "switch") || strings.Contains(d, "switch"):
		return "external-switch"
	case strings.Contains(c, "router") || strings.Contains(d, "fortigate") || strings.Contains(d, "router") || strings.Contains(d, "mikrotik"):
		return "router"
	}
	return "host"
}

func looksLikeAP(d string) bool {
	for _, k := range []string{" ap-", "ap1", "ap2", "ap3", "ap5", "iap", "instant on ap", "ap22", "ap25", "ap51", "ap6"} {
		if strings.Contains(d, k) {
			return true
		}
	}
	return false
}

func shorten(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
