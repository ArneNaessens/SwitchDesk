// Package store keeps the inventory, credentials, last snapshots and config
// backups as plain files in the user's config directory.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
)

type Switch struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Host         string       `json:"host"`
	Port         int          `json:"port"`
	Family       aruba.Family `json:"family"`
	CredentialID string       `json:"credentialId"`
	Site         string       `json:"site"`
	Notes        string       `json:"notes"`
	HostKey      string       `json:"hostKey"`
	Disabled     bool         `json:"disabled"` // paused: not polled, not read, no alerts
	CreatedAt    time.Time    `json:"createdAt"`
	// Cloud is set for Instant On switches read from the portal instead of SSH.
	// CredentialID then points to an Instant On account.
	Cloud *CloudRef `json:"cloud,omitempty"`
	// Web is set for Instant On switches in local management mode, read
	// through their web interface (port 443 for HTTPS, 80 for HTTP).
	Web bool `json:"web,omitempty"`
}

type CloudRef struct {
	SiteID   string `json:"siteId"`
	SiteName string `json:"siteName"`
	DeviceID string `json:"deviceId"` // the device MAC the portal uses as its id
}

type Credential struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // encrypted on disk, never sent to the UI
	Kind     string `json:"kind,omitempty"`     // "" for SSH, "instanton" for an Instant On portal account
}

const KindInstantOn = "instanton"

type Settings struct {
	PingInterval    int  `json:"pingInterval"`    // seconds
	CollectInterval int  `json:"collectInterval"` // minutes, 0 = manual only
	Parallel        int  `json:"parallel"`
	KeepBackups     int  `json:"keepBackups"`
	CPUWarn         int  `json:"cpuWarn"`
	MemWarn         int  `json:"memWarn"`
	IgnoreSNMP      bool `json:"ignoreSnmp"`
	// Firmware: "off", "stage" (copy to the spare bank only) or "install"
	// (copy, then reboot inside the maintenance window).
	FirmwareAuto string `json:"firmwareAuto"`
	WindowStart  string `json:"windowStart"` // "02:00"
	WindowEnd    string `json:"windowEnd"`   // "05:00"
}

func DefaultSettings() Settings {
	return Settings{PingInterval: 30, CollectInterval: 15, Parallel: 4, KeepBackups: 20, CPUWarn: 80, MemWarn: 90, FirmwareAuto: "off", WindowStart: "02:00", WindowEnd: "05:00"}
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type data struct {
	Switches    []Switch         `json:"switches"`
	Credentials []Credential     `json:"credentials"`
	Settings    Settings         `json:"settings"`
	Layout      map[string]Point `json:"layout"`
}

type Store struct {
	dir string
	key []byte
	mu  sync.Mutex
	d   data
}

func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "SwitchDesk"), nil
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700); err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Join(dir, "backups"), 0o700)
	s := &Store{dir: dir}
	keyPath := filepath.Join(dir, "secret.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s is corrupt", keyPath)
	}
	s.key = key
	s.d.Settings = DefaultSettings()
	s.d.Layout = map[string]Point{}
	b, err := os.ReadFile(filepath.Join(dir, "inventory.json"))
	if err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("inventory.json: %w", err)
		}
	}
	if s.d.Layout == nil {
		s.d.Layout = map[string]Point{}
	}
	def := DefaultSettings()
	if s.d.Settings.PingInterval < 5 {
		s.d.Settings.PingInterval = def.PingInterval
	}
	if s.d.Settings.Parallel < 1 {
		s.d.Settings.Parallel = def.Parallel
	}
	if s.d.Settings.KeepBackups < 1 {
		s.d.Settings.KeepBackups = def.KeepBackups
	}
	if s.d.Settings.CPUWarn == 0 {
		s.d.Settings.CPUWarn = def.CPUWarn
	}
	if s.d.Settings.MemWarn == 0 {
		s.d.Settings.MemWarn = def.MemWarn
	}
	if s.d.Settings.FirmwareAuto == "" {
		s.d.Settings.FirmwareAuto, s.d.Settings.WindowStart, s.d.Settings.WindowEnd = def.FirmwareAuto, def.WindowStart, def.WindowEnd
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "inventory.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "inventory.json"))
}

// ---- secrets ----

func (s *Store) encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	blk, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(blk)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return "enc:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) decrypt(v string) (string, error) {
	if !strings.HasPrefix(v, "enc:") {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v[4:])
	if err != nil {
		return "", err
	}
	blk, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(blk)
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	p, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(p), err
}

// ---- switches ----

func NewID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Store) Switches() []Switch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Switch(nil), s.d.Switches...)
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name+out[i].Host) < strings.ToLower(out[j].Name+out[j].Host)
	})
	return out
}

func (s *Store) Switch(id string) (Switch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sw := range s.d.Switches {
		if sw.ID == id {
			return sw, true
		}
	}
	return Switch{}, false
}

func (s *Store) SaveSwitch(sw Switch) (Switch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sw.Host = strings.TrimSpace(sw.Host)
	if sw.Cloud != nil {
		if sw.Cloud.SiteID == "" || sw.Cloud.DeviceID == "" {
			return sw, errors.New("pick the Instant On site and switch")
		}
		sw.Family, sw.Port, sw.HostKey = aruba.FamilyInstantOn, 0, ""
		for _, o := range s.d.Switches {
			if o.ID != sw.ID && o.Cloud != nil && strings.EqualFold(o.Cloud.DeviceID, sw.Cloud.DeviceID) {
				return sw, fmt.Errorf("%s is already in the inventory", firstNonEmpty(o.Name, sw.Name, sw.Cloud.DeviceID))
			}
		}
	} else {
		if sw.Host == "" {
			return sw, errors.New("host is required")
		}
		if sw.Web {
			sw.Family, sw.HostKey = aruba.FamilyInstantOn, ""
			if sw.Port == 0 || sw.Port == 22 {
				sw.Port = 443
			}
		}
		if sw.Port == 0 {
			sw.Port = 22
		}
		if sw.Family == "" {
			sw.Family = aruba.FamilyAuto
		}
		for _, o := range s.d.Switches {
			if o.ID != sw.ID && o.Cloud == nil && strings.EqualFold(o.Host, sw.Host) && o.Port == sw.Port {
				return sw, fmt.Errorf("%s is already in the inventory", sw.Host)
			}
		}
	}
	if sw.ID == "" {
		sw.ID = NewID()
		sw.CreatedAt = time.Now()
		s.d.Switches = append(s.d.Switches, sw)
	} else {
		found := false
		for i, o := range s.d.Switches {
			if o.ID == sw.ID {
				if sw.Host != o.Host {
					sw.HostKey = "" // new address, new trust decision
				} else if sw.HostKey == "" {
					sw.HostKey = o.HostKey
				}
				sw.CreatedAt = o.CreatedAt
				s.d.Switches[i] = sw
				found = true
			}
		}
		if !found {
			return sw, errors.New("switch not found")
		}
	}
	return sw, s.saveLocked()
}

// SetCloudHost records the address the Instant On portal reports for a switch.
func (s *Store) SetCloudHost(id, host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, o := range s.d.Switches {
		if o.ID == id && o.Cloud != nil && o.Host != host {
			s.d.Switches[i].Host = host
			s.saveLocked()
		}
	}
}

func (s *Store) SetHostKey(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.d.Switches {
		if s.d.Switches[i].ID == id && s.d.Switches[i].HostKey != key {
			s.d.Switches[i].HostKey = key
			s.saveLocked()
		}
	}
}

// SetDisabled pauses or resumes the given switches.
func (s *Store) SetDisabled(ids []string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for i := range s.d.Switches {
		if want[s.d.Switches[i].ID] {
			s.d.Switches[i].Disabled = disabled
		}
	}
	return s.saveLocked()
}

func (s *Store) DeleteSwitch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.d.Switches[:0]
	for _, sw := range s.d.Switches {
		if sw.ID != id {
			out = append(out, sw)
		}
	}
	s.d.Switches = out
	delete(s.d.Layout, id)
	os.Remove(filepath.Join(s.dir, "snapshots", id+".json"))
	os.RemoveAll(filepath.Join(s.dir, "backups", id))
	return s.saveLocked()
}

// ---- credentials ----

func (s *Store) Credentials() []Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Credential, 0, len(s.d.Credentials))
	for _, c := range s.d.Credentials {
		c.Password = ""
		out = append(out, c)
	}
	return out
}

func (s *Store) Credential(id string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.d.Credentials {
		if c.ID == id {
			p, err := s.decrypt(c.Password)
			if err != nil {
				return c, fmt.Errorf("can't decrypt password for %q: %w", c.Name, err)
			}
			c.Password = p
			return c, nil
		}
	}
	return Credential{}, errors.New("no credentials assigned")
}

// SaveCredential stores a credential set. An empty password on update keeps the old one.
func (s *Store) SaveCredential(c Credential) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(c.Username) == "" {
		return c, errors.New("username is required")
	}
	if c.Name == "" {
		c.Name = c.Username
	}
	enc, err := s.encrypt(c.Password)
	if err != nil {
		return c, err
	}
	if c.ID == "" {
		if c.Password == "" {
			return c, errors.New("password is required")
		}
		c.ID = NewID()
		c.Password = enc
		s.d.Credentials = append(s.d.Credentials, c)
	} else {
		found := false
		for i, o := range s.d.Credentials {
			if o.ID == c.ID {
				if c.Password == "" {
					c.Password = o.Password
				} else {
					c.Password = enc
				}
				s.d.Credentials[i] = c
				found = true
			}
		}
		if !found {
			return c, errors.New("credential not found")
		}
	}
	out := c
	out.Password = ""
	return out, s.saveLocked()
}

func (s *Store) DeleteCredential(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sw := range s.d.Switches {
		if sw.CredentialID == id {
			return fmt.Errorf("still used by %s", firstNonEmpty(sw.Name, sw.Host))
		}
	}
	out := s.d.Credentials[:0]
	for _, c := range s.d.Credentials {
		if c.ID != id {
			out = append(out, c)
		}
	}
	s.d.Credentials = out
	return s.saveLocked()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- settings & layout ----

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Settings
}

func (s *Store) SaveSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.PingInterval < 5 {
		v.PingInterval = 5
	}
	if v.Parallel < 1 {
		v.Parallel = 1
	}
	if v.Parallel > 16 {
		v.Parallel = 16
	}
	if v.KeepBackups < 1 {
		v.KeepBackups = 1
	}
	switch v.FirmwareAuto {
	case "off", "stage", "install":
	default:
		v.FirmwareAuto = "off"
	}
	for _, t := range []*string{&v.WindowStart, &v.WindowEnd} {
		if _, err := time.Parse("15:04", *t); err != nil {
			return fmt.Errorf("maintenance window times look like 02:00")
		}
	}
	s.d.Settings = v
	return s.saveLocked()
}

func (s *Store) Layout() map[string]Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Point{}
	for k, v := range s.d.Layout {
		out[k] = v
	}
	return out
}

func (s *Store) SaveLayout(l map[string]Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Layout = l
	return s.saveLocked()
}

// ---- snapshots & backups ----

func (s *Store) SaveSnapshot(id string, snap aruba.Snapshot) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "snapshots", id+".json"), b, 0o600)
}

func (s *Store) LoadSnapshot(id string) (aruba.Snapshot, bool) {
	var snap aruba.Snapshot
	b, err := os.ReadFile(filepath.Join(s.dir, "snapshots", id+".json"))
	if err != nil || json.Unmarshal(b, &snap) != nil {
		return snap, false
	}
	return snap, true
}

type Backup struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	Size int64     `json:"size"`
}

// SaveBackup writes the running config if it differs from the newest backup.
func (s *Store) SaveBackup(id, cfg string, keep int) (bool, error) {
	cfg = strings.TrimSpace(cfg)
	if cfg == "" {
		return false, nil
	}
	dir := filepath.Join(s.dir, "backups", id)
	os.MkdirAll(dir, 0o700)
	list := s.Backups(id)
	if len(list) > 0 {
		last, _ := os.ReadFile(filepath.Join(dir, list[0].Name))
		if normalizeCfg(string(last)) == normalizeCfg(cfg) {
			return false, nil
		}
	}
	name := time.Now().Format("20060102-150405") + ".cfg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(cfg+"\n"), 0o600); err != nil {
		return false, err
	}
	list = s.Backups(id)
	for i := keep; i < len(list); i++ {
		os.Remove(filepath.Join(dir, list[i].Name))
	}
	return true, nil
}

// normalizeCfg drops lines that change on every read (timestamps, uptime comments).
func normalizeCfg(c string) string {
	var out []string
	for _, l := range strings.Split(c, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "; Ver #") || strings.HasPrefix(t, "!Version") || strings.HasPrefix(t, "!export-password") || strings.HasPrefix(t, "Current configuration") || t == "Running configuration:" {
			continue
		}
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (s *Store) Backups(id string) []Backup {
	dir := filepath.Join(s.dir, "backups", id)
	ents, _ := os.ReadDir(dir)
	var out []Backup
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".cfg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		at, err := time.ParseInLocation("20060102-150405", strings.TrimSuffix(e.Name(), ".cfg"), time.Local)
		if err != nil {
			at = info.ModTime()
		}
		out = append(out, Backup{Name: e.Name(), At: at, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (s *Store) ReadBackup(id, name string) (string, error) {
	if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".cfg") {
		return "", errors.New("bad backup name")
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "backups", id, name))
	return string(b), err
}
