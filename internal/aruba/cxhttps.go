package aruba

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HTTPSState is what an AOS-CX switch had before EnableHTTPS changed it, so
// RestoreHTTPS can put it back.
type HTTPSState struct {
	VRF       string
	WasOn     bool // "https-server vrf <vrf>" was already configured
	WasRW     bool // "https-server rest access-mode read-write" was already configured
	ChangedRW bool // we turned read-write on (older releases only; newer ones drop the command)
}

var cxRWRe = regexp.MustCompile(`(?m)^\s*https-server rest access-mode read-write\s*$`)

// ParseHTTPSState reads the relevant lines of an AOS-CX running config.
func ParseHTTPSState(runningConfig, vrf string) HTTPSState {
	on := regexp.MustCompile(`(?m)^\s*https-server vrf ` + regexp.QuoteMeta(vrf) + `\s*$`)
	return HTTPSState{VRF: vrf, WasOn: on.MatchString(runningConfig), WasRW: cxRWRe.MatchString(runningConfig)}
}

// EnableHTTPS turns on the AOS-CX web server (REST API) in vrf so that an
// image can be uploaded to it. The upload goes from this computer to the
// switch, the same direction as SSH, so it works where TFTP can't.
func EnableHTTPS(ctx context.Context, r Runner, vrf string) (HTTPSState, error) {
	cfg, err := r.Run(ctx, "show running-config", 2*time.Minute)
	if err != nil {
		return HTTPSState{VRF: vrf}, fmt.Errorf("reading the running config: %w", err)
	}
	st := ParseHTTPSState(cfg, vrf)
	if st.WasOn && st.WasRW {
		return st, nil
	}
	if _, err := r.Run(ctx, "configure terminal", 30*time.Second); err != nil {
		return st, err
	}
	defer r.Run(context.Background(), "end", 30*time.Second)
	if !st.WasOn {
		out, err := r.Run(ctx, "https-server vrf "+vrf, 30*time.Second)
		if err != nil {
			return st, err
		}
		if IsCLIError(out) {
			return st, fmt.Errorf("the switch rejected \"https-server vrf %s\": %s", vrf, firstLine(out))
		}
	}
	if !st.WasRW {
		// releases before 10.08 need this for uploads; newer ones reject it, which is fine
		out, err := r.Run(ctx, "https-server rest access-mode read-write", 30*time.Second)
		if err == nil && !IsCLIError(out) {
			st.ChangedRW = true
		}
	}
	return st, nil
}

// HTTPSRestoreCommands are the config lines that undo EnableHTTPS.
func HTTPSRestoreCommands(st HTTPSState) []string {
	var cmds []string
	if st.ChangedRW {
		cmds = append(cmds, "https-server rest access-mode read-only")
	}
	if !st.WasOn && st.VRF != "" {
		cmds = append(cmds, "no https-server vrf "+st.VRF)
	}
	return cmds
}

// RestoreHTTPS puts back what EnableHTTPS changed.
func RestoreHTTPS(ctx context.Context, r Runner, st HTTPSState) error {
	cmds := HTTPSRestoreCommands(st)
	if len(cmds) == 0 {
		return nil
	}
	return runConfig(ctx, r, cmds...)
}

// CXBank is the REST name of a bank ("primary" or "secondary").
func CXBank(bank string) (string, bool) {
	b := strings.ToLower(bank)
	return b, b == "primary" || b == "secondary"
}
