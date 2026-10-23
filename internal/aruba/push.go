package aruba

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PushTarget is the path an image is uploaded to over SFTP/SCP so that it
// lands in bank. Only AOS-S takes images this way; other families return false
// and keep using TFTP.
func PushTarget(f Family, bank string) (string, bool) {
	if f != FamilyAOSS {
		return "", false
	}
	switch bank {
	case "primary", "secondary":
		return "/os/" + bank, true
	}
	return "", false
}

// FileTransferState is what the switch had before EnableFileTransfer changed
// it, so RestoreFileTransfer can put it back.
type FileTransferState struct {
	WasOn         bool // "ip ssh filetransfer" was already configured
	TFTPClientOff bool // "no tftp client" was already configured
	TFTPServerOff bool // "no tftp server" was already configured
}

var (
	ftOnRe      = regexp.MustCompile(`(?m)^\s*ip ssh filetransfer\s*$`)
	tftpCliOff  = regexp.MustCompile(`(?m)^\s*no tftp client\s*$`)
	tftpServOff = regexp.MustCompile(`(?m)^\s*no tftp server\s*$`)
)

// ParseFileTransferState reads the relevant lines of an AOS-S running config.
func ParseFileTransferState(runningConfig string) FileTransferState {
	return FileTransferState{
		WasOn:         ftOnRe.MatchString(runningConfig),
		TFTPClientOff: tftpCliOff.MatchString(runningConfig),
		TFTPServerOff: tftpServOff.MatchString(runningConfig),
	}
}

// EnableFileTransfer turns on SFTP/SCP on an AOS-S switch. The switch turns
// its TFTP client and server off when this is enabled, which is why the
// previous state is returned for RestoreFileTransfer.
func EnableFileTransfer(ctx context.Context, r Runner) (FileTransferState, error) {
	cfg, err := r.Run(ctx, "show running-config", 2*time.Minute)
	if err != nil {
		return FileTransferState{}, fmt.Errorf("reading the running config: %w", err)
	}
	st := ParseFileTransferState(cfg)
	if st.WasOn {
		return st, nil
	}
	if err := runConfig(ctx, r, "ip ssh filetransfer"); err != nil {
		return st, err
	}
	return st, nil
}

// RestoreCommands are the config lines that undo EnableFileTransfer.
func RestoreCommands(st FileTransferState) []string {
	if st.WasOn {
		return nil
	}
	cmds := []string{"no ip ssh filetransfer"}
	if !st.TFTPClientOff {
		cmds = append(cmds, "tftp client")
	}
	if !st.TFTPServerOff {
		cmds = append(cmds, "tftp server")
	}
	return cmds
}

// RestoreFileTransfer puts back what EnableFileTransfer changed.
func RestoreFileTransfer(ctx context.Context, r Runner, st FileTransferState) error {
	cmds := RestoreCommands(st)
	if len(cmds) == 0 {
		return nil
	}
	return runConfig(ctx, r, cmds...)
}

func runConfig(ctx context.Context, r Runner, lines ...string) error {
	all := append(append([]string{"configure terminal"}, lines...), "end")
	var bad []string
	for _, c := range all {
		out, err := r.Run(ctx, c, 30*time.Second)
		if err != nil {
			return fmt.Errorf("%q: %w", c, err)
		}
		if IsCLIError(out) {
			bad = append(bad, fmt.Sprintf("%q: %s", c, firstLine(out)))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("the switch rejected %s", strings.Join(bad, "; "))
	}
	return nil
}
