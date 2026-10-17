package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFWornShoesBeadCapturedEndpoint(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 1254)
	s.pvfItemCatalog = items
	s.initialEquipmentArchive = archive
	saveChronicleWornFixture(t, g, 17, 100280474)
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	bead := inv.Slots["0:65"]
	bead.ItemID, bead.Count = 10002536, 2 // 真实PVF的鞋部位宝珠，保留现场穿戴目标及协议槽。
	inv.Slots["0:103"] = bead
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	body, _ := hex.DecodeString("00670003110000000000")
	resolver := func(bead, target int64) (alignedcmd.EnchantBeadResolution, error) {
		return resolveCurrentEnchantBeadMetadata(items, archive, bead, target)
	}
	resolution, err := resolver(10002536, 100280474)
	if err != nil || resolution.CardItemID == 0 {
		t.Fatalf("真实宝珠无法解析：%+v %v", resolution, err)
	}
	result, err := dnfinventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 272, Body: body,
		AccountID: "account-1", SelectedCharacterID: 2, Repositories: g, EnchantBeadResolver: resolver})
	if err != nil || len(result.WornEquipmentSlotRefreshes) != 1 || result.WornEquipmentSlotRefreshes[0] != 17 {
		t.Fatalf("穿戴附魔未提交：%+v %v", result, err)
	}
	if err := s.sendAlignedUpperResponses(session, result); err != nil {
		t.Fatal(err)
	}
	data := conn.write.Bytes()
	targetSeen, ackSeen := false, false
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == 342 || packet.Header.MsgID == 2 || (packet.Header.MsgID == 13 && len(packet.Body) != 0 && packet.Body[0] == 3) {
			t.Fatal("附魔原位更新不应重建对象或发送被屏蔽快照")
		}
		if packet.Header.MsgID == 272 {
			if !targetSeen || !bytes.Equal(packet.Body, []byte{1, 3, 17, 0}) {
				t.Fatalf("穿戴附魔回包顺序或槽位错误：%x", packet.Body)
			}
			ackSeen = true
		}
		if packet.Header.MsgID == 14 && len(packet.Body) != 0 && packet.Body[0] == 3 {
			if ackSeen || len(packet.Body) != 3+currentItemListEntryWireSize || binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 || binary.LittleEndian.Uint16(packet.Body[3:5]) != 17 || binary.LittleEndian.Uint32(packet.Body[5:9]) != 100280474 || binary.LittleEndian.Uint32(packet.Body[9:13]) != 999999998 || int64(binary.LittleEndian.Uint32(packet.Body[3+0x0e:3+0x12])) != resolution.CardItemID {
				t.Fatalf("附魔结果前目标119字节行的部位、品质或卡片不符：%x", packet.Body)
			}
			targetSeen = true
		}
	}
	if !ackSeen {
		t.Fatal("穿戴附魔没有结果回包")
	}
	inv, _, _ = g.Inventory.Load(ctx, "2")
	eq, _, _ := g.Equipment.Load(ctx, "2")
	row, ok := currentItemListEntryFromEquipment(eq.Entries["17"])
	if !ok || int64(binary.LittleEndian.Uint32(row.data[0x0e:0x12])) != resolution.CardItemID || inv.Slots["0:103"].Count != 1 || inv.Slots["0:17"].Count != 1 || binary.LittleEndian.Uint32(row.data[6:10]) != 999999998 {
		t.Fatal("附魔、材料、背包或品质状态不符")
	}
}
