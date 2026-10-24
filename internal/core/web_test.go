package core

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/ionlocal/localfake"
	"github.com/jaba0x/switchdesk/internal/store"
)

func TestWebSwitch(t *testing.T) {
	fake := localfake.New("admin", "secret")
	defer fake.Close()
	host, portS, _ := net.SplitHostPort(fake.Host())
	port, _ := strconv.Atoi(portS)

	st, _ := store.Open(t.TempDir())
	cred, _ := st.SaveCredential(store.Credential{Username: "admin", Password: "secret"})
	sw, err := st.SaveSwitch(store.Switch{Host: host, Port: port, CredentialID: cred.ID, Web: true})
	if err != nil {
		t.Fatal(err)
	}
	if sw.Family != aruba.FamilyInstantOn {
		t.Fatalf("family %s", sw.Family)
	}
	m := New(st, nil)
	m.ctx, m.stop = contextWithCancel()
	t.Cleanup(m.stop)

	msg, err := m.TestConnection(sw)
	if err != nil || !strings.Contains(msg, "SW-BRANCH-114") {
		t.Fatalf("test connection: %q %v", msg, err)
	}
	m.ping(sw)
	m.collect(sw.ID)
	d, _ := m.Detail(sw.ID)
	if !d.Status.Online || d.Status.CollectError != "" || d.Snapshot == nil || !d.Snapshot.Web || d.Summary == nil {
		t.Fatalf("status %+v", d.Status)
	}
	var titles []string
	for _, i := range d.Issues {
		titles = append(titles, i.Title)
	}
	all := strings.Join(titles, " | ")
	for _, want := range []string{"Spanning tree is disabled", "Clock is off by", "No loop protection", "Loop detected", "Fan problem", "Unsaved configuration changes"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in: %s", want, all)
		}
	}
	if strings.Contains(all, "Telnet") || strings.Contains(all, "No syslog") {
		t.Errorf("unexpected: %s", all)
	}
	if fake.Sessions() != 0 {
		t.Fatalf("%d web sessions left open on the switch", fake.Sessions())
	}
	if _, err := m.RunCommand(sw.ID, "show version"); !errors.Is(err, ErrWebOnly) {
		t.Fatalf("console: %v", err)
	}

	// a changed password shows as a login failure
	fake.Password = "changed"
	m.collect(sw.ID)
	d, _ = m.Detail(sw.ID)
	if !d.Status.AuthFailed {
		t.Fatalf("auth failure not flagged: %+v", d.Status)
	}
}
