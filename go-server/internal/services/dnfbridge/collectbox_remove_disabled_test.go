package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCollectboxWithdrawalRejectsWithoutReturningOrLosingBead(t *testing.T) {
	s, session, repos, conn := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	item := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repos, map[string]dnfrepo.ItemStack{"0:30": {ItemID: int64(item), Count: 1}})
	session.collectboxSelectedIndex = 2
	if err := s.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, 30, item)); err != nil {
		t.Fatal(err)
	}
	before := currentCollectboxLoadSlots(t, repos)
	conn.write.Reset()
	body := make([]byte, 5)
	body[0] = 2
	binary.LittleEndian.PutUint32(body[1:], item)
	registry := mustGameplayModuleRegistry(collectboxGameplayModule())
	if handled, err := registry.DispatchLegacy(s, session, currentCollectboxRemoveItemMsgID, body); !handled || err != nil {
		t.Fatalf("withdraw dispatch handled=%v err=%v", handled, err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 2 || packets[0].classification != 1 || packets[0].msgID != currentCollectboxRemoveItemMsgID || packets[0].bodyHex != "0012" {
		t.Fatalf("withdraw must fail then restore authoritative box, got %+v", packets)
	}
	state, ok := currentCollectboxStatePacketForBox(t, packets, 2)
	if !ok || state[7] != 1 || binary.LittleEndian.Uint32(state[8:12]) != item {
		t.Fatalf("withdraw lost registered bead in UI: %x", state)
	}
	if after := currentCollectboxLoadSlots(t, repos); !reflect.DeepEqual(before, after) {
		t.Fatalf("withdraw modified assets: before=%+v after=%+v", before, after)
	}
}
