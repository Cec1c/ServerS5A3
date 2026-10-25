package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestItemUsageSuccessfulRoutesAndReload(t *testing.T) {
	for _, kind := range []string{"normal", "random_box", "selection_box"} {
		t.Run(kind, func(t *testing.T) {
			catalog := mustCurrentBoosterTestCatalog(t)
			source := catalog.source.(dungeonDropCatalogTestSource)
			source["stackable/stackable.lst"] += "900 `potion.stk`\n"
			source["stackable/potion.stk"] = "[stackable type]\n`[waste]` 0\n[stack limit]\n100\n[total usable count]\n3\n"
			for _, path := range []string{"stackable/cash/random.stk", "stackable/cash/selection.stk"} {
				source[path] += "[total usable count]\n1\n[total weekly usable count]\n2\n[total weekly usable count reset day]\n4\n"
			}
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			id, opcode, limit := uint32(900), uint16(44), byte(3)
			body := make([]byte, 15)
			binary.LittleEndian.PutUint16(body, 75)
			binary.LittleEndian.PutUint32(body[3:], 4242)
			binary.LittleEndian.PutUint32(body[7:], id)
			if kind == "random_box" {
				id, opcode, limit = 500, 160, 1
				body = []byte{75, 0}
			}
			if kind == "selection_box" {
				id, opcode, limit = 501, 160, 1
				body = make([]byte, 10)
				binary.LittleEndian.PutUint16(body, 75)
				binary.LittleEndian.PutUint32(body[4:], 700)
			}
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); err != nil {
				t.Fatal(err)
			}
			item, err := catalog.ResolveItem(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:75": {ItemID: int64(id), Count: 5, Extra: map[string]string{"item_kind": "stackable", "pvf_path": item.PVFPath}}}}); err != nil {
				t.Fatal(err)
			}
			service := currentBoosterTestService(catalog, g)
			service.options.currentClientVersion = "official-24.9"
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
			service.gameplayTimers = queue
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "usage-" + kind, selectedCharacterID: 19, characterGeneration: 1}
			check := func(data []byte, want byte, requireAck bool) {
				t.Helper()
				at, ackAt, slotAt, usageAt := 0, -1, -1, -1
				var usageBodies [][]byte
				for len(data) > 0 {
					packet, rest := splitGameServerUpperPacket(t, data)
					data = rest
					if packet.Header.Classification == 1 && packet.Header.MsgID == opcode {
						if len(packet.Body) == 0 || packet.Body[0] != 1 {
							t.Fatalf("failure ACK %x", packet.Body)
						}
						ackAt = at
					}
					if packet.Header.Classification == 0 && packet.Header.MsgID == 14 {
						slotAt = at
					}
					if packet.Header.Classification == 0 && packet.Header.MsgID == 541 {
						usageAt = at
						usageBodies = append(usageBodies, packet.Body)
					}
					at++
				}
				if len(usageBodies) != 1 || requireAck && (ackAt != 0 || slotAt <= ackAt || usageAt <= slotAt) {
					t.Fatalf("order ack=%d slot=%d usage=%d bodies=%d", ackAt, slotAt, usageAt, len(usageBodies))
				}
				d, w := decodeItemUsagePages(t, usageBodies)
				if !reflect.DeepEqual(d, map[uint32]byte{id: want}) {
					t.Fatalf("daily=%v want=%d", d, want)
				}
				if kind != "normal" {
					if !reflect.DeepEqual(w, map[uint32]byte{id: 1}) {
						t.Fatalf("weekly=%v", w)
					}
				} else if len(w) != 0 {
					t.Fatal("normal use created weekly count")
				}
			}
			for used := byte(1); used <= limit; used++ {
				conn.write.Reset()
				if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), opcode, body); err != nil {
					t.Fatal(err)
				}
				check(conn.write.Bytes(), used, true)
			}
			beforeC, _, _ := g.Character.Load(ctx, "19")
			beforeI, _, _ := g.Inventory.Load(ctx, "19")
			conn.write.Reset()
			if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), opcode, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != opcode || ack.Body[0] != 0 || len(rest) != 0 {
				t.Fatalf("exhaustion response=%x trailing=%d", ack.Body, len(rest))
			}
			afterC, _, _ := g.Character.Load(ctx, "19")
			afterI, _, _ := g.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatal("exhausted item debited or granted reward")
			}
			if afterI.Slots["0:75"].Count != 5-int64(limit) {
				t.Fatal("wrong source count")
			}
			conn.write.Reset()
			replacement := &gameSession{conn: conn, connID: "reload-" + strconv.Itoa(int(id)), selectedCharacterID: 19, characterGeneration: 2}
			if err := service.sendCurrentItemUsageForSession(replacement, "test_reload"); err != nil {
				t.Fatal(err)
			}
			check(conn.write.Bytes(), limit, false)
			queue.now = queue.now.Add(24 * time.Hour)
			conn.write.Reset()
			if err := service.sendCurrentItemUsageForSession(replacement, "test_daily_refresh"); err != nil {
				t.Fatal(err)
			}
			check(conn.write.Bytes(), 0, false)
		})
	}
}
