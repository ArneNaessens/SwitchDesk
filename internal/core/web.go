package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jaba0x/switchdesk/internal/ionlocal"
	"github.com/jaba0x/switchdesk/internal/store"
)

// Instant On switches in local management mode have no SSH CLI. They're read
// through their web interface's XML pages with the switch's admin login.

// ErrWebOnly is returned for actions that need SSH.
var ErrWebOnly = errors.New("this Instant On switch is read through its web interface and has no SSH; make changes on the switch's web page")

func webClient(sw store.Switch) *ionlocal.Client {
	host := sw.Host
	if sw.Port != 0 && sw.Port != 80 && sw.Port != 443 {
		host = net.JoinHostPort(sw.Host, strconv.Itoa(sw.Port))
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return ionlocal.New(host, sw.Port == 443 || sw.Port%1000 == 443) // 443, 8443
}

func (m *Manager) webRead(sw store.Switch) {
	st := m.stat(sw.ID)
	fail := func(err error) {
		m.mu.Lock()
		st.CollectError = err.Error()
		st.AuthFailed = errors.Is(err, ionlocal.ErrLogin)
		st.LastCollect = time.Now()
		m.mu.Unlock()
	}
	cred, err := m.st.Credential(sw.CredentialID)
	if err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	c := webClient(sw)
	err = c.Login(ctx, cred.Username, cred.Password)
	m.readReach(sw.ID, err)
	if err != nil {
		fail(err)
		return
	}
	snap, err := ionlocal.Collect(ctx, c)
	c.Logout(context.Background())
	if err != nil {
		fail(err)
		return
	}
	applyPortProbes(&snap, sw.Host)

	m.mu.Lock()
	prev := m.snaps[sw.ID]
	st.TopologyDelta = 0
	if prev != nil && prev.STP.Enabled && snap.STP.TopologyChanges > prev.STP.TopologyChanges {
		st.TopologyDelta = snap.STP.TopologyChanges - prev.STP.TopologyChanges
	}
	m.snaps[sw.ID] = &snap
	st.LastCollect = time.Now()
	st.CollectError, st.AuthFailed, st.HostKeyChanged = "", false, false
	m.mu.Unlock()
	if m.ctx.Err() == nil {
		m.st.SaveSnapshot(sw.ID, snap)
	}
}

// testWeb signs in once for the Add switch dialog. When the port wasn't
// reachable it tries the other of 443 and 80, and says which one worked.
func (m *Manager) testWeb(sw store.Switch) (string, error) {
	cred, err := m.st.Credential(sw.CredentialID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if sw.Port == 0 || sw.Port == 22 {
		sw.Port = 443
	}
	try := []int{sw.Port}
	if sw.Port == 443 {
		try = append(try, 80)
	} else if sw.Port == 80 {
		try = append(try, 443)
	}
	var lastErr error
	for _, p := range try {
		sw.Port = p
		c := webClient(sw)
		if err := c.Login(ctx, cred.Username, cred.Password); err != nil {
			var ne net.Error
			if errors.As(err, &ne) || strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "connection reset") {
				lastErr = err
				continue
			}
			return "", err
		}
		sys, err := c.Get(ctx, "SystemGlobalSetting")
		c.Logout(context.Background())
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Signed in to %s (%s)", sys.Text("systemName"), strings.SplitN(sys.Text("systemDescription"), ",", 2)[0])
		if p != try[0] {
			msg += fmt.Sprintf(". Port %d didn't answer; port %d works, so set the port to %d", try[0], p, p)
		}
		return msg, nil
	}
	return "", lastErr
}
