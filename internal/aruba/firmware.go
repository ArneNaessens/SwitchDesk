package aruba

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Banks describes the two firmware slots and which one the switch boots.
type Banks struct {
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Running   string `json:"running"` // "primary" or "secondary", "" if unknown
	Default   string `json:"default"` // bank used on next boot
	Known     bool   `json:"known"`
}

// Spare is the bank that isn't running, where new images go.
func (b Banks) Spare() string {
	if b.Running == "secondary" {
		return "primary"
	}
	return "secondary"
}

func (b Banks) Version(bank string) string {
	if bank == "primary" {
		return b.Primary
	}
	return b.Secondary
}

var (
	aossImageRe   = regexp.MustCompile(`(?im)^\s*(Primary|Secondary) Image\s*:\s*(?:\d[\d,]*\s+\S+\s+)?([A-Z]{2}\.\d{2}\.\d{2}\.\d{4})`)
	aossDefaultRe = regexp.MustCompile(`(?im)Default Boot Image\s*:\s*(Primary|Secondary)`)
	bootImageRe   = regexp.MustCompile(`(?im)^\s*Boot Image\s*:\s*(Primary|Secondary)`)
	cxSectionRe   = regexp.MustCompile(`(?im)^\s*(?:ArubaOS-CX|AOS-CX)\s+(Primary|Secondary)\s+Image`)
	cxVersionLine = regexp.MustCompile(`(?im)^\s*Version\s*:\s*(\S+)`)
	cxDefaultRe   = regexp.MustCompile(`(?im)Default Image\s*:\s*(primary|secondary)`)
	cxActiveRe    = regexp.MustCompile(`(?im)Active Image\s*:\s*(primary|secondary)`)
)

// ParseBanks reads "show flash" (AOS-S) or "show images" (AOS-CX) plus "show version".
func ParseBanks(f Family, images, version string) Banks {
	b := Banks{}
	if IsCLIError(images) {
		return b
	}
	switch f {
	case FamilyAOSCX:
		locs := cxSectionRe.FindAllStringSubmatchIndex(images, -1)
		for i, loc := range locs {
			end := len(images)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			bank := strings.ToLower(images[loc[2]:loc[3]])
			if m := cxVersionLine.FindStringSubmatch(images[loc[1]:end]); m != nil {
				if bank == "primary" {
					b.Primary = m[1]
				} else {
					b.Secondary = m[1]
				}
			}
		}
		if m := cxDefaultRe.FindStringSubmatch(images); m != nil {
			b.Default = strings.ToLower(m[1])
		}
		if m := cxActiveRe.FindStringSubmatch(version + "\n" + images); m != nil {
			b.Running = strings.ToLower(m[1])
		}
	default:
		for _, m := range aossImageRe.FindAllStringSubmatch(images, -1) {
			if strings.EqualFold(m[1], "primary") {
				b.Primary = m[2]
			} else {
				b.Secondary = m[2]
			}
		}
		if m := aossDefaultRe.FindStringSubmatch(images); m != nil {
			b.Default = strings.ToLower(m[1])
		}
		if m := bootImageRe.FindStringSubmatch(version); m != nil {
			b.Running = strings.ToLower(m[1])
		}
	}
	if b.Running == "" {
		b.Running = b.Default
	}
	b.Known = b.Primary != "" || b.Secondary != ""
	return b
}

// UpgradeStep reports progress to the caller.
type UpgradeStep func(stage, detail string)

// CopyImage tells the switch to pull an image from a TFTP server into bank.
// vrf is only used on AOS-CX ("mgmt" or "default").
// cliToken is what may go into a firmware CLI line as one argument: no
// spaces, quotes, separators or control characters that could end the line
// or add another command.
var cliToken = regexp.MustCompile(`^[A-Za-z0-9._+:\-\[\]]{1,200}$`)

func CopyImage(ctx context.Context, r Runner, f Family, server, file, bank, vrf string) (string, error) {
	for _, v := range []string{server, file, bank} {
		if !cliToken.MatchString(v) {
			return "", fmt.Errorf("refusing to send an unsafe value to the switch: %q", v)
		}
	}
	if vrf != "" && !cliToken.MatchString(vrf) {
		return "", fmt.Errorf("refusing to send an unsafe value to the switch: %q", vrf)
	}
	var cmd string
	switch f {
	case FamilyAOSCX:
		if vrf == "" {
			vrf = "default"
		}
		cmd = fmt.Sprintf("copy tftp://%s/%s %s vrf %s", server, file, bank, vrf)
	case FamilyAOSS:
		cmd = fmt.Sprintf("copy tftp flash %s %s %s", server, file, bank)
	default:
		return "", fmt.Errorf("firmware updates over SSH aren't supported on %s switches", f.Label())
	}
	out, err := r.Run(ctx, cmd, 40*time.Minute)
	if err != nil {
		return out, err
	}
	if msg := copyFailure(out); msg != "" {
		return out, fmt.Errorf("the switch reported: %s", msg)
	}
	return out, nil
}

var copyFailRe = regexp.MustCompile(`(?im)^.*(error|fail|invalid|not a valid|timed out|timeout|unreachable|denied|no such|not found|aborted|insufficient).*$`)

func copyFailure(out string) string {
	if IsCLIError(out) {
		return firstLine(out)
	}
	if m := copyFailRe.FindString(out); m != "" {
		l := strings.ToLower(m)
		// progress lines like "0 errors" aren't failures
		if strings.Contains(l, "0 error") {
			return ""
		}
		return strings.TrimSpace(m)
	}
	return ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return s
}

// SetDefaultBank makes bank the one used on the next boot.
func SetDefaultBank(ctx context.Context, r Runner, f Family, bank string) error {
	cmd := "boot set-default " + bank
	if f == FamilyAOSS {
		cmd = "boot set-default flash " + bank
	}
	out, err := r.Run(ctx, cmd, 60*time.Second)
	if err == nil && IsCLIError(out) {
		err = fmt.Errorf("the switch rejected %q: %s", cmd, firstLine(out))
	}
	return err
}

// Reboot saves the config and boots from bank. The SSH session drops as the
// switch goes down, so a closed connection here counts as success.
func Reboot(ctx context.Context, r Runner, f Family, bank string) error {
	r.Run(ctx, "write memory", 60*time.Second)
	cmd := "boot system " + bank
	if f == FamilyAOSS {
		cmd = "boot system flash " + bank
	}
	out, err := r.Run(ctx, cmd, 45*time.Second)
	if IsCLIError(out) {
		return fmt.Errorf("the switch rejected %q: %s", cmd, firstLine(out))
	}
	if err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "EOF") {
		return err
	}
	return nil
}

// ImagesCommand is the command that lists the firmware banks.
func ImagesCommand(f Family) string {
	if f == FamilyAOSCX {
		return "show images"
	}
	return "show flash"
}
