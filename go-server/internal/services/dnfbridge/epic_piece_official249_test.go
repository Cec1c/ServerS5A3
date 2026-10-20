package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOfficialEpicPiecePickupRestoresSentinelWithoutGrantOrOverwrite(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		service, runtime, repos := prepareCurrentEpicPieceDropTest(t)
		service.options.currentClientVersion = "official-24.9"
		inventory, _, _ := repos.Inventory.Load(context.Background(), "99")
		if occupied {
			inventory.Slots = make(map[string]dnfrepo.ItemStack)
			inventory.Slots["0:64"] = dnfrepo.ItemStack{ItemID: 9001, Count: 1}
		}
		if err := repos.Inventory.Save(context.Background(), inventory); err != nil {
			t.Fatal(err)
		}
		session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
		key := seedCurrentDungeonPickupDrop(t, service, runtime, epicPieceBookTestPieceA, currentSceneActorObjectKey(99))
		for i := 0; i < 2; i++ {
			session.conn.(*bufferConn).write.Reset()
			if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			if len(packets) != 3 || packets[0].Header.MsgID != 39 || packets[1].Header.MsgID != 14 || packets[2].Header.MsgID != 1149 {
				t.Fatalf("occupied=%v packets=%+v", occupied, packets)
			}
			row := packets[1].Body
			if len(row) != 122 || row[0] != 0 || binary.LittleEndian.Uint16(row[1:3]) != 1 || binary.LittleEndian.Uint16(row[3:5]) != 64 {
				t.Fatalf("slot refresh=%x", row)
			}
			wantID := ^uint32(0) // native incremental empty-row marker
			if occupied {
				wantID = 9001
			}
			if binary.LittleEndian.Uint32(row[5:9]) != wantID {
				t.Fatalf("authoritative original item not restored: %x", row)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "99")
			if !reflect.DeepEqual(inventory.Slots, after.Slots) {
				t.Fatal("book-only pickup modified bag")
			}
			account, _, _ := repos.Account.Load(context.Background(), epicPieceBookTestAccountID)
			if dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA] != 1 {
				t.Fatal("replay credited twice")
			}
		}
	}
}
