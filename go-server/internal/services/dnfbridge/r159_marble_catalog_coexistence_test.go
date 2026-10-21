package dnfbridge

import (
	"bytes"
	"testing"
)

func TestR159MarblePreservesCumulativeTownFiendWarCatalog(t *testing.T) {
	var towns []uint16
	for _, activity := range currentImageTownActivities() {
		towns = append(towns, activity.id)
	}
	if len(towns) == 0 {
		t.Fatal("expected existing cumulative town activities")
	}
	build := func(marble bool) []byte {
		t.Helper()
		body, err := buildCurrentActivityEventInfoBodyWithRuntimeActivitiesAndMarble([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, true, towns, true, true, true, marble)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	before, after := parseCurrentSpendTimeEventInfoBody(t, build(false)), parseCurrentSpendTimeEventInfoBody(t, build(true))
	if after.baseCount != before.baseCount+1 || after.activeCount != before.activeCount+1 {
		t.Fatal("marble must add exactly one base row and one active row")
	}
	base, active := map[uint16][]byte{}, map[uint16][]byte{}
	for _, row := range after.baseRows {
		if _, duplicate := base[row.eventID]; duplicate {
			t.Fatalf("duplicate base row %d", row.eventID)
		}
		base[row.eventID] = row.raw
	}
	for _, row := range after.activeRows {
		if _, duplicate := active[row.eventID]; duplicate {
			t.Fatalf("duplicate active row %d", row.eventID)
		}
		active[row.eventID] = row.raw
	}
	for _, row := range before.baseRows {
		if !bytes.Equal(base[row.eventID], row.raw) {
			t.Fatalf("existing base row %d changed", row.eventID)
		}
	}
	for _, row := range before.activeRows {
		if !bytes.Equal(active[row.eventID], row.raw) {
			t.Fatalf("existing active row %d changed", row.eventID)
		}
	}
	for _, id := range append(towns, currentFiendWarChannelEventID, currentSeriaLuckEventID, currentAttendanceMarbleEventID) {
		if base[id] == nil || active[id] == nil {
			t.Fatalf("required cumulative activity %d lost from one directory", id)
		}
	}
}
