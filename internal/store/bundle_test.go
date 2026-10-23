package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jaba0x/switchdesk/internal/aruba"
)

func seed(t *testing.T, dir, host string) (*Store, Switch) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCredential(Credential{Name: "admins", Username: "admin", Password: "s3cret-" + host})
	if err != nil {
		t.Fatal(err)
	}
	sw, err := s.SaveSwitch(Switch{Host: host, CredentialID: c.ID, Name: "sw-" + host})
	if err != nil {
		t.Fatal(err)
	}
	s.SaveSnapshot(sw.ID, aruba.Snapshot{Family: aruba.FamilyAOSS})
	s.SaveBackup(sw.ID, "hostname x", 5)
	return s, sw
}

func TestExportImportAcrossMachines(t *testing.T) {
	pcA, pcB := t.TempDir(), t.TempDir()
	a, swA := seed(t, pcA, "10.0.0.1")
	file := filepath.Join(t.TempDir(), "x.sdbak")
	if err := a.Export(file, "short", true); err == nil {
		t.Fatal("short passphrase accepted")
	}
	if err := a.Export(file, "correct horse", true); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBundle(file, "wrong pass!"); err != ErrBadPassphrase {
		t.Fatalf("want bad passphrase, got %v", err)
	}
	info, err := InspectBundle(file, "correct horse")
	if err != nil || info.Switches != 1 || !info.History {
		t.Fatal(info, err)
	}

	// PC B has its own key and its own switch; merge keeps both
	b, _ := seed(t, pcB, "10.0.0.2")
	res, err := b.Import(file, "correct horse", "merge")
	if err != nil || res.Added != 1 || res.Credentials != 1 {
		t.Fatal(res, err)
	}
	if len(b.Switches()) != 2 {
		t.Fatal("merge lost a switch")
	}
	var imported Switch
	for _, sw := range b.Switches() {
		if sw.Host == "10.0.0.1" {
			imported = sw
		}
	}
	c, err := b.Credential(imported.CredentialID)
	if err != nil || c.Password != "s3cret-10.0.0.1" {
		t.Fatal("password didn't survive the move", c, err)
	}
	if _, ok := b.LoadSnapshot(imported.ID); !ok || len(b.Backups(imported.ID)) != 1 {
		t.Fatal("history not imported")
	}
	// merging again skips duplicates
	res, _ = b.Import(file, "correct horse", "merge")
	if res.Added != 0 || len(res.Skipped) != 1 {
		t.Fatal(res)
	}
	// replace makes B identical to A's inventory
	if _, err := b.Import(file, "correct horse", "replace"); err != nil {
		t.Fatal(err)
	}
	b2, _ := Open(pcB)
	if len(b2.Switches()) != 1 || b2.Switches()[0].ID != swA.ID {
		t.Fatal("replace", b2.Switches())
	}
	if c, err := b2.Credential(swA.CredentialID); err != nil || c.Password != "s3cret-10.0.0.1" {
		t.Fatal("replace password", err)
	}
}

func TestCopyAndRemoveData(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "new place")
	s, sw := seed(t, src, "10.0.0.9")
	if err := s.CopyData(dst); err != nil {
		t.Fatal(err)
	}
	info := InspectDir(dst, src)
	if !info.HasData || info.Switches != 1 || !info.Writable || info.Same {
		t.Fatal(info)
	}
	moved, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := moved.Credential(sw.CredentialID); err != nil || c.Password != "s3cret-10.0.0.9" {
		t.Fatal("copied folder can't decrypt", err)
	}
	s.RemoveData()
	if _, err := os.Stat(filepath.Join(src, "inventory.json")); !os.IsNotExist(err) {
		t.Fatal("old data still there")
	}
}
