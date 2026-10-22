package dnfbridge

import (
	"bytes"
	"fmt"
	"testing"
)

// The public online-panel/Seria merge and FiendWar must compose, rather than
// selecting one branch's catalog builder and silently losing another owner.
func TestR195FiendWarActivityCatalogPreservesOnlineAndSeria(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, seria := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("native=%t/seria=%t/fiend=%t", native, seria, enabled), func(t *testing.T) {
					ids := currentSpendTimeJoustCatalogGoldenRewards
					old, err := buildCurrentActivityEventInfoBodyWithRuntimeActivities(ids, 10800, true, true, true, true, true, 2418, true, true, true, true, true, native, seria)
					if err != nil {
						t.Fatal(err)
					}
					next, err := buildCurrentActivityEventInfoBodyWithRuntimeActivitiesAndFiendWar(ids, 10800, true, true, true, true, true, 2418, true, true, true, true, true, native, seria, enabled)
					if err != nil {
						t.Fatal(err)
					}
					if !enabled && !bytes.Equal(old, next) {
						t.Fatal("disabled FiendWar changed the public catalog")
					}
					a, b := parseCurrentSpendTimeEventInfoBody(t, old), parseCurrentSpendTimeEventInfoBody(t, next)
					owner, other := uint16(currentSpendTimeEventID), uint16(currentPCRoomTimePointEventID)
					if native {
						owner, other = other, owner
					}
					if !b.hasEvent(owner) || b.hasEvent(other) || b.hasEvent(currentSeriaLuckEventID) != seria || b.hasEvent(currentFiendWarChannelEventID) != enabled {
						t.Fatal("catalog owner combination changed")
					}
					var base, active []byte
					baseCount, activeCount := 0, 0
					for _, row := range b.baseRows {
						if row.eventID == currentFiendWarChannelEventID {
							baseCount++
							continue
						}
						base = append(base, row.raw...)
					}
					for _, row := range b.activeRows {
						if row.eventID == currentFiendWarChannelEventID {
							activeCount++
							continue
						}
						active = append(active, row.raw...)
					}
					var oldBase, oldActive []byte
					for _, row := range a.baseRows {
						oldBase = append(oldBase, row.raw...)
					}
					for _, row := range a.activeRows {
						oldActive = append(oldActive, row.raw...)
					}
					want := 0
					if enabled {
						want = 1
					}
					if baseCount != want || activeCount != want || !bytes.Equal(base, oldBase) || !bytes.Equal(active, oldActive) {
						t.Fatal("FiendWar duplicated or changed existing activity rows/order")
					}
				})
			}
		}
	}
}
