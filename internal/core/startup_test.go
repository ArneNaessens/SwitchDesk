package core

import (
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

// Issue #7: saved data is read in the background without its raw output,
// and the switch page still gets the raw output from disk.
func TestSavedDataLoadsInBackground(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	sw, _ := st.SaveSwitch(store.Switch{Host: "10.9.9.1"})
	st.SaveSnapshot(sw.ID, aruba.Snapshot{Family: aruba.FamilyAOSS, CollectedAt: time.Now(),
		Facts: aruba.Facts{Hostname: "core"}, RunningConfig: "hostname \"core\"\n",
		Raw: map[string]string{"system": "System Name : core"}})

	m := New(st, nil)
	m.WaitLoaded()
	m.mu.Lock()
	snap := m.snaps[sw.ID]
	m.mu.Unlock()
	if snap == nil || snap.Facts.Hostname != "core" || snap.RunningConfig == "" {
		t.Fatalf("snapshot not loaded: %+v", snap)
	}
	if snap.Raw != nil {
		t.Fatal("raw output kept in memory at startup")
	}
	if d := m.Dashboard(); d.Loading != 0 || d.Switches[0].Summary == nil {
		t.Fatalf("dashboard after loading: %+v", d)
	}
	det, err := m.Detail(sw.ID)
	if err != nil || det.Snapshot.Raw["system"] != "System Name : core" {
		t.Fatalf("detail raw: %v %v", det.Snapshot, err)
	}
	if m.stat(sw.ID).LastCollect.IsZero() {
		t.Fatal("last read time not restored, so every switch would be read again at once")
	}
}
