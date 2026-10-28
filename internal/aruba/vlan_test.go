package aruba

import (
	"reflect"
	"testing"
)

func TestAfterChange(t *testing.T) {
	cur := []VLAN{
		{ID: 1, Name: "DEFAULT", Untagged: []string{"1", "2", "3"}},
		{ID: 20, Name: "Users", Untagged: []string{"4"}, Tagged: []string{"24"}},
	}
	ids := func(vs []VLAN) []int {
		var out []int
		for _, v := range vs {
			out = append(out, v.ID)
		}
		return out
	}

	if got := ids(AfterChange(cur, VLANChange{Action: "delete", ID: 20})); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("delete: %v", got)
	}

	got := AfterChange(cur, VLANChange{Action: "create", ID: 10, Name: "Voice", AddUntagged: []string{"2"}, AddTagged: []string{"24"}})
	if !reflect.DeepEqual(ids(got), []int{1, 10, 20}) {
		t.Fatalf("create order: %v", ids(got))
	}
	if !reflect.DeepEqual(got[0].Untagged, []string{"1", "3"}) {
		t.Fatalf("port 2 should leave VLAN 1 untagged: %v", got[0].Untagged)
	}
	if got[1].Name != "Voice" || !reflect.DeepEqual(got[1].Tagged, []string{"24"}) {
		t.Fatalf("new VLAN: %+v", got[1])
	}

	got = AfterChange(cur, VLANChange{Action: "update", ID: 20, Name: "Staff", RemovePorts: []string{"24"}})
	if got[1].Name != "Staff" || len(got[1].Tagged) != 0 || !reflect.DeepEqual(got[1].Untagged, []string{"4"}) {
		t.Fatalf("update: %+v", got[1])
	}

	if cur[0].Name != "DEFAULT" || len(cur[0].Untagged) != 3 || len(cur[1].Tagged) != 1 {
		t.Fatalf("the current table must not be changed: %+v", cur)
	}
}
