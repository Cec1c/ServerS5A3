package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"testing"
)

func TestXunlongNPCEquipmentSaleUsesOrdinaryPrice(t *testing.T) {
	for _, worn := range []bool{false, true} {
		name := "unequipped"
		if worn {
			name = "equipped"
		}
		t.Run(name, func(t *testing.T) {
			shop, items := mustCurrentNPCShopTestCatalogs(t)
			repos := mustCurrentNPCShopRepositories(t, 1000, map[string]repo.ItemStack{"0:65": {ItemID: 700, Count: 1}})
			if worn {
				e := repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{"22": {SlotIndex: 22, ItemID: 100341606}}}
				if err := repos.Equipment.Save(context.Background(), e); err != nil {
					t.Fatal(err)
				}
			}
			conn := &bufferConn{}
			service := currentNPCShopTestService(repos, shop, items)
			if err := service.handleGameCommand(&gameSession{conn: conn, selectedCharacterID: 19}, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketSellItem), currentNPCShopSellTestBody(0, 65, 1, 0)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != uint16(dnfenum.CmdPacketSellItem) || len(ack.Body) != 12 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != 1100 {
				t.Fatalf("sale acknowledgement: %x", ack.Body)
			}
			update, rest := splitGameServerUpperPacket(t, rest)
			if len(rest) != 0 || update.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
				t.Fatal("wrong wallet update")
			}
			assertCurrentNPCShopIncrementalRows(t, update.Body, 1100, 65, math.MaxUint32, 0)
			c, ok, err := repos.Character.Load(context.Background(), "19")
			if err != nil || !ok || c.Stats["gold"] != 1100 {
				t.Fatalf("wallet %v found %v err %v", c.Stats, ok, err)
			}
		})
	}
}
