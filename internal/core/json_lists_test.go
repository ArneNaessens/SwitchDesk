package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

// A healthy switch has no problems. The UI reads these lists' lengths, so
// they must arrive as [] and never as null (issue #4).
func TestHealthySwitchSendsEmptyLists(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sw, _ := st.SaveSwitch(store.Switch{Host: "10.0.0.5", Port: 443, Web: true})
	m := New(st, nil)
	// disabled: evaluate() is skipped, so issues stay empty
	st.SetDisabled([]string{sw.ID}, true)
	m.snaps[sw.ID] = &aruba.Snapshot{Family: aruba.FamilyInstantOn, Web: true, CollectedAt: time.Now()}

	det, err := m.Detail(sw.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(det)
	for _, f := range []string{`"issues":null`, `"backups":null`} {
		if strings.Contains(string(b), f) {
			t.Errorf("switch page data has %s", f)
		}
	}
	d, _ := json.Marshal(m.Dashboard())
	if strings.Contains(string(d), `"issues":null`) {
		t.Error("dashboard has issues:null")
	}
}
