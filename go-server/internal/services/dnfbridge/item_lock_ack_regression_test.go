package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Current EXE 8bf1d39d: class1 callbacks 0x1fbd7a0/0x1fca010/
// 0x1fbd880 consume success first, then u8 list + u16 slot; unlock
// additionally reads u32 remaining seconds. Class0/op252 has no success byte.
func TestCurrentEquipmentLockAcksIncludeSuccessBeforeLocation(t *testing.T) {
	for _, list := range []byte{0, 1, 2, 7} {
		for _, opcode := range []uint16{267, 268, 269} {
			t.Run(fmt.Sprintf("list%d_op%d", list, opcode), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				key := fmt.Sprintf("%d:65", list)
				stack := dnfrepo.ItemStack{ItemID: 1001, Count: 1, Extra: map[string]string{"instance_marker": "preserved"}}
				if opcode != 267 {
					stack.Extra["equipment_lock_state"] = "1"
				}
				record := dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{}}
				if list == 2 {
					record.Warehouse = map[string]dnfrepo.ItemStack{key: stack}
				} else {
					record.Slots[key] = stack
				}
				if err := repos.Inventory.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
				s := &Service{options: options{accountID: "acc-lock", gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 27}
				handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), opcode, []byte{list, 65, 0})
				if err != nil || !handled {
					t.Fatalf("handled=%t err=%v", handled, err)
				}
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				want := []byte{1, list, 65, 0}
				if opcode == 268 {
					want = append(want, 0, 0, 0, 0)
				}
				if ack.Header.Classification != 1 || ack.Header.MsgID != opcode || !bytes.Equal(ack.Body, want) {
					t.Fatalf("current client needs success/list/slot payload: header=%+v body=%x want=%x", ack.Header, ack.Body, want)
				}
				// Read the native callback's location after the success byte.
				if ack.Body[1] != list || binary.LittleEndian.Uint16(ack.Body[2:4]) != 65 {
					t.Fatal("client location shifted")
				}
				notice, tail := splitGameServerUpperPacket(t, rest)
				if len(tail) != 0 || notice.Header.Classification != 0 {
					t.Fatal("notification ordering/framing changed")
				}
				if opcode == 268 {
					if notice.Header.MsgID != 252 || !bytes.Equal(notice.Body, []byte{list, 65, 0}) {
						t.Fatalf("unlock NOTI must not gain success prefix: %x", notice.Body)
					}
				} else if notice.Header.MsgID != 251 || !bytes.Equal(notice.Body, []byte{1, 0, list, 65, 0, 1}) {
					t.Fatalf("lock snapshot changed: %x", notice.Body)
				}
				after, found, err := repos.Inventory.Load(ctx, "27")
				if err != nil || !found {
					t.Fatalf("load found=%t err=%v", found, err)
				}
				item := after.Slots[key]
				if list == 2 {
					item = after.Warehouse[key]
				}
				wantState := "1"
				if opcode == 268 {
					wantState = ""
				}
				if item.ItemID != 1001 || item.Count != 1 || item.Extra["instance_marker"] != "preserved" || item.Extra["equipment_lock_state"] != wantState {
					t.Fatalf("unexpected committed instance: %+v", item)
				}
			})
		}
	}
}
