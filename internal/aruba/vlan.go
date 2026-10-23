package aruba

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// VLANChange describes one VLAN edit coming from the UI.
type VLANChange struct {
	Action      string   `json:"action"` // create, update, delete
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	AddTagged   []string `json:"addTagged"`
	AddUntagged []string `json:"addUntagged"`
	RemovePorts []string `json:"removePorts"`
	Save        bool     `json:"save"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{1,32}$`)
var safePort = regexp.MustCompile(`^([A-Za-z]{0,3}\d+(/\d+){0,2}|lag\d+|Trk\d+)$`)

func (c VLANChange) validate(current []VLAN) error {
	if c.ID < 1 || c.ID > 4094 {
		return fmt.Errorf("VLAN ID must be 1-4094")
	}
	switch c.Action {
	case "create", "update", "delete":
	default:
		return fmt.Errorf("unknown action %q", c.Action)
	}
	if c.Action == "delete" && c.ID == 1 {
		return fmt.Errorf("VLAN 1 can't be deleted")
	}
	if c.Name != "" && !safeName.MatchString(c.Name) {
		return fmt.Errorf("name may use letters, digits, space, _ . - (max 32)")
	}
	for _, list := range [][]string{c.AddTagged, c.AddUntagged, c.RemovePorts} {
		for _, p := range list {
			if !safePort.MatchString(p) {
				return fmt.Errorf("invalid port %q", p)
			}
		}
	}
	exists := false
	for _, v := range current {
		if v.ID == c.ID {
			exists = true
		}
	}
	if c.Action == "create" && exists {
		return fmt.Errorf("VLAN %d already exists", c.ID)
	}
	if (c.Action == "update" || c.Action == "delete") && !exists && len(current) > 0 {
		return fmt.Errorf("VLAN %d doesn't exist on this switch", c.ID)
	}
	return nil
}

// BuildVLANCommands returns the exact CLI lines that will be sent.
func BuildVLANCommands(f Family, c VLANChange, current []VLAN) ([]string, error) {
	if err := c.validate(current); err != nil {
		return nil, err
	}
	if f == FamilyInstantOn {
		return nil, fmt.Errorf("VLAN changes over SSH aren't supported on Instant On switches; use the Instant On portal or web UI")
	}
	cmds := []string{"configure terminal"}
	switch f {
	case FamilyAOSCX:
		if c.Action == "delete" {
			cmds = append(cmds, fmt.Sprintf("no vlan %d", c.ID))
			break
		}
		cmds = append(cmds, fmt.Sprintf("vlan %d", c.ID))
		if c.Name != "" {
			cmds = append(cmds, "name "+c.Name)
		}
		cmds = append(cmds, "exit")
		for _, p := range c.AddUntagged {
			cmds = append(cmds, "interface "+p, "no routing", fmt.Sprintf("vlan access %d", c.ID), "exit")
		}
		for _, p := range c.AddTagged {
			cmds = append(cmds, "interface "+p, "no routing", fmt.Sprintf("vlan trunk allowed %d", c.ID), "exit")
		}
		for _, p := range c.RemovePorts {
			if memberTagged(current, c.ID, p) {
				cmds = append(cmds, "interface "+p, fmt.Sprintf("no vlan trunk allowed %d", c.ID), "exit")
			} else {
				cmds = append(cmds, "interface "+p, "no vlan access", "exit")
			}
		}
	default: // AOS-S
		if c.Action == "delete" {
			cmds = append(cmds, fmt.Sprintf("no vlan %d", c.ID))
			break
		}
		cmds = append(cmds, fmt.Sprintf("vlan %d", c.ID))
		if c.Name != "" {
			cmds = append(cmds, fmt.Sprintf("name \"%s\"", c.Name))
		}
		if len(c.AddUntagged) > 0 {
			cmds = append(cmds, "untagged "+CompressPorts(c.AddUntagged))
		}
		if len(c.AddTagged) > 0 {
			cmds = append(cmds, "tagged "+CompressPorts(c.AddTagged))
		}
		var rmT, rmU []string
		for _, p := range c.RemovePorts {
			if memberTagged(current, c.ID, p) {
				rmT = append(rmT, p)
			} else {
				rmU = append(rmU, p)
			}
		}
		if len(rmT) > 0 {
			cmds = append(cmds, "no tagged "+CompressPorts(rmT))
		}
		if len(rmU) > 0 {
			cmds = append(cmds, "no untagged "+CompressPorts(rmU))
		}
		cmds = append(cmds, "exit")
	}
	cmds = append(cmds, "end")
	if c.Save {
		cmds = append(cmds, "write memory")
	}
	return cmds, nil
}

func memberTagged(vlans []VLAN, id int, port string) bool {
	for _, v := range vlans {
		if v.ID == id {
			for _, p := range v.Tagged {
				if p == port {
					return true
				}
			}
		}
	}
	return false
}

// ApplyResult is the transcript of a change.
type ApplyResult struct {
	OK         bool     `json:"ok"`
	Transcript []string `json:"transcript"`
	Error      string   `json:"error"`
}

// ApplyCommands sends config lines one by one and stops at the first rejection.
// "write memory" is only reached if every previous line was accepted.
func ApplyCommands(ctx context.Context, r Runner, cmds []string) ApplyResult {
	res := ApplyResult{}
	for _, c := range cmds {
		out, err := r.Run(ctx, c, 30*time.Second)
		res.Transcript = append(res.Transcript, "> "+c)
		if strings.TrimSpace(out) != "" {
			res.Transcript = append(res.Transcript, out)
		}
		if err != nil {
			res.Error = fmt.Sprintf("%s: %v", c, err)
			r.Run(ctx, "end", 5*time.Second)
			return res
		}
		if IsCLIError(out) || strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "0 error") {
			res.Error = fmt.Sprintf("switch rejected %q", c)
			r.Run(ctx, "end", 5*time.Second)
			return res
		}
	}
	res.OK = true
	return res
}
