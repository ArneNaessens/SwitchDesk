package aruba

import (
	"context"
	"strings"
	"testing"
	"time"
)

type scriptRunner struct {
	sent []string
	cfg  string
}

func (r *scriptRunner) Run(ctx context.Context, cmd string, _ time.Duration) (string, error) {
	r.sent = append(r.sent, cmd)
	if cmd == "show running-config" {
		return r.cfg, nil
	}
	return "", nil
}

func TestPushTarget(t *testing.T) {
	if p, ok := PushTarget(FamilyAOSS, "secondary"); !ok || p != "/os/secondary" {
		t.Fatal(p, ok)
	}
	if _, ok := PushTarget(FamilyAOSCX, "primary"); ok {
		t.Fatal("AOS-CX should stay on TFTP")
	}
	if _, ok := PushTarget(FamilyAOSS, "weird"); ok {
		t.Fatal("unknown bank")
	}
}

func TestFileTransferEnableRestore(t *testing.T) {
	// default switch: filetransfer off, TFTP on
	r := &scriptRunner{cfg: "hostname \"sw\"\nip ssh\n"}
	st, err := EnableFileTransfer(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.sent, "|"); got != "show running-config|configure terminal|ip ssh filetransfer|end" {
		t.Fatal(got)
	}
	if got := strings.Join(RestoreCommands(st), "|"); got != "no ip ssh filetransfer|tftp client|tftp server" {
		t.Fatal(got)
	}

	// TFTP server was already off: leave it off
	st = ParseFileTransferState("no tftp server\n")
	if got := strings.Join(RestoreCommands(st), "|"); got != "no ip ssh filetransfer|tftp client" {
		t.Fatal(got)
	}

	// already on: change nothing, restore nothing
	r = &scriptRunner{cfg: "ip ssh filetransfer\nno tftp client\nno tftp server\n"}
	st, _ = EnableFileTransfer(context.Background(), r)
	if len(r.sent) != 1 || RestoreCommands(st) != nil {
		t.Fatal(r.sent, RestoreCommands(st))
	}
}
