package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jaba0x/switchdesk/internal/instanton"
	"github.com/jaba0x/switchdesk/internal/store"
)

// Instant On switches managed from the cloud have no SSH, so they're read
// from the Instant On portal. One account can hold many sites; each site's
// inventory is fetched once and shared by every switch on it.

// ErrCloudOnly is returned for actions that need SSH.
var ErrCloudOnly = errors.New("this Instant On switch is read from the Instant On portal and has no SSH; make changes in the Instant On app or portal")

// cloudFresh is how long a site's inventory is reused before asking the portal again.
var cloudFresh = 25 * time.Second

// newPortal is swapped out in tests to point at a fake portal.
var newPortal = func(email, password string) *instanton.Client { return instanton.New(email, password) }

type ionAccount struct {
	email, password string
	c               *instanton.Client
	mu              sync.Mutex
	sites           map[string]*siteCache
}

type siteCache struct {
	at     time.Time
	devs   []instanton.Device
	alerts []instanton.Alert
}

type cloudState struct {
	mu       sync.Mutex
	accounts map[string]*ionAccount // credential id -> account
}

func (m *Manager) ionAccount(credID string) (*ionAccount, error) {
	cred, err := m.st.Credential(credID)
	if err != nil {
		return nil, err
	}
	if cred.Kind != store.KindInstantOn {
		return nil, errors.New("the selected credentials aren't an Instant On account")
	}
	m.cloud.mu.Lock()
	defer m.cloud.mu.Unlock()
	if m.cloud.accounts == nil {
		m.cloud.accounts = map[string]*ionAccount{}
	}
	a := m.cloud.accounts[credID]
	if a == nil || a.email != cred.Username || a.password != cred.Password {
		a = &ionAccount{email: cred.Username, password: cred.Password, c: newPortal(cred.Username, cred.Password), sites: map[string]*siteCache{}}
		m.cloud.accounts[credID] = a
	}
	return a, nil
}

// site returns a site's devices, from cache when it's fresh enough. Alerts
// are only fetched when withAlerts is set; otherwise the last ones are kept.
func (a *ionAccount) site(ctx context.Context, siteID string, maxAge time.Duration, withAlerts bool) (*siteCache, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sc := a.sites[siteID]
	if sc != nil && time.Since(sc.at) < maxAge && !withAlerts {
		return sc, nil
	}
	devs, err := a.c.Inventory(ctx, siteID)
	if err != nil {
		return nil, err
	}
	next := &siteCache{at: time.Now(), devs: devs}
	if sc != nil {
		next.alerts = sc.alerts
	}
	if withAlerts {
		if al, err := a.c.Alerts(ctx, siteID); err == nil {
			next.alerts = al
		}
	}
	a.sites[siteID] = next
	return next, nil
}

func findDevice(devs []instanton.Device, id string) (instanton.Device, bool) {
	for _, d := range devs {
		if strings.EqualFold(d.ID, id) || strings.EqualFold(d.MACAddress, id) {
			return d, true
		}
	}
	return instanton.Device{}, false
}

// cloudRead fetches a cloud switch and stores its status and snapshot. It is
// both the "ping" (every ping interval, from cache) and the full read.
func (m *Manager) cloudRead(sw store.Switch, full bool) {
	st := m.stat(sw.ID)
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	fail := func(err error) {
		m.mu.Lock()
		st.CollectError = "Instant On portal: " + err.Error()
		st.AuthFailed = errors.Is(err, instanton.ErrLogin)
		if full {
			st.LastCollect = time.Now()
		}
		m.mu.Unlock()
	}
	acct, err := m.ionAccount(sw.CredentialID)
	if err != nil {
		fail(err)
		return
	}
	age := cloudFresh
	if full {
		age = 0
	}
	sc, err := acct.site(ctx, sw.Cloud.SiteID, age, full)
	if err != nil {
		fail(err)
		return
	}
	d, found := findDevice(sc.devs, sw.Cloud.DeviceID)

	m.mu.Lock()
	online := found && d.Online()
	if !st.Checked || st.Online != online {
		st.Since = time.Now()
	}
	st.Online, st.Checked, st.LastCheck = online, true, time.Now()
	st.LatencyMs, st.PingError = 0, ""
	st.CollectError, st.AuthFailed = "", false
	switch {
	case !found:
		st.PingError = "no longer in the site's inventory on the Instant On portal"
	case !online:
		st.PingError = "the Instant On portal reports it as down"
	}
	if found {
		snap := instanton.Snapshot(d, sw.Cloud.SiteName, sc.alerts)
		m.snaps[sw.ID] = &snap
		if full {
			st.LastCollect = time.Now()
		}
	}
	m.mu.Unlock()

	if found {
		if ip := d.IPAddress; ip != "" && ip != sw.Host {
			m.st.SetCloudHost(sw.ID, ip)
		}
		if full {
			m.mu.Lock()
			snap := *m.snaps[sw.ID]
			m.mu.Unlock()
			m.st.SaveSnapshot(sw.ID, snap)
		}
	}
}

// CloudSite is one site of an Instant On account, with its switches.
type CloudSite struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Error    string        `json:"error,omitempty"`
	Switches []CloudSwitch `json:"switches"`
}

type CloudSwitch struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	IP       string `json:"ip"`
	Online   bool   `json:"online"`
	Ports    int    `json:"ports"`
	AddedAs  string `json:"addedAs,omitempty"` // id of the switch already in SwitchDesk
}

// DiscoverCloud logs in to an Instant On account and lists the switches on
// every site it can see, marking the ones already in SwitchDesk.
func (m *Manager) DiscoverCloud(credID string) ([]CloudSite, error) {
	acct, err := m.ionAccount(credID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sites, err := acct.c.Sites(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]string{}
	for _, sw := range m.st.Switches() {
		if sw.Cloud != nil {
			have[strings.ToLower(sw.Cloud.DeviceID)] = sw.ID
		}
	}
	out := make([]CloudSite, 0, len(sites))
	for _, s := range sites {
		cs := CloudSite{ID: s.ID, Name: s.Name, Switches: []CloudSwitch{}}
		sc, err := acct.site(ctx, s.ID, 0, false)
		if err != nil {
			cs.Error = err.Error()
			out = append(out, cs)
			continue
		}
		for _, d := range sc.devs {
			if !d.IsSwitch() {
				continue
			}
			cs.Switches = append(cs.Switches, CloudSwitch{
				DeviceID: d.ID, Name: d.DisplayName(), Model: d.Model, IP: d.IPAddress,
				Online: d.Online(), Ports: len(d.Ports), AddedAs: have[strings.ToLower(d.ID)],
			})
		}
		sort.Slice(cs.Switches, func(i, j int) bool { return cs.Switches[i].Name < cs.Switches[j].Name })
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CloudPick is a switch chosen in the Add switch dialog.
type CloudPick struct {
	SiteID   string `json:"siteId"`
	SiteName string `json:"siteName"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
}

// AddCloud adds the picked Instant On switches and reads them right away.
func (m *Manager) AddCloud(credID string, picks []CloudPick) (int, error) {
	added := 0
	var errs []string
	for _, p := range picks {
		sw, err := m.st.SaveSwitch(store.Switch{
			Name: p.Name, Host: p.IP, CredentialID: credID, Site: p.SiteName,
			Cloud: &store.CloudRef{SiteID: p.SiteID, SiteName: p.SiteName, DeviceID: p.DeviceID},
		})
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		added++
		m.stat(sw.ID)
		go m.Enqueue(sw.ID)
	}
	m.changed()
	if len(errs) > 0 {
		return added, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return added, nil
}
