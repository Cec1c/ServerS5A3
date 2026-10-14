package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归：DNFBRIDGE_REAL_PVF_SMOKE 指向线上同源 Script.pvf 时才跑。
// 走的是**完整链路**：C2S 438 字节 → 桥接层解码/PVF 解析 → inventory owner 事务
// → class1 438 回包字节，并回读落库的锻造等级（0x77 行 0x57 + Extra byte_57/value_57）
// 与材料数量。
//
// 真档参数（同一份运行时 Script.pvf，SHA256 369d072b…）：
//
//	etc/upgrade_separate.etc  [separate upgrade max] 8、[table] 10 档、3326 材料表、
//	                          7 个稀有度权重（index 4 = 1.40）
//	101040493 圣耀救赎光剑      grade 96 / rarity 4 / [weapon] / [durability] 48
//	3326 强烈的气息            [material]
//	10164035 便携式锻造炉       [action type] [portable genuine damage upgrade upgrede]
//	490000622 +5 传承武器锻造券  [equipment separate reinforcement ticket] 5 100 fixed -1
//	                           [reinforcement usable item list] = 401000024 等 29 件
//	401000024 传承短剑          [weapon] / [durability] 45（券白名单成员）
const (
	realForgingWeaponItemID   = 101040493
	realForgingBreathItemID   = 3326
	realForgingFurnaceItemID  = 10164035
	realForgingTicketItemID   = 490000622
	realForgingTicketTargetID = 401000024

	realForgingTargetSlot   = 9
	realForgingMaterialSlot = 121
	realForgingFurnaceSlot  = 30
)

func realForgingPVFPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	return path
}

// realForgingRow 造一行当前客户端的 0x77 物品行（slot@0、itemID@2、耐久@0x0B）。
func realForgingRow(itemID int64, durability uint16) []byte {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	return raw
}

func realForgingStack(itemID int64, count int64, durability uint16, extra map[string]string) dnfrepo.ItemStack {
	values := map[string]string{"pvf_path": "stackable/real.stk"}
	for key, value := range extra {
		values[key] = value
	}
	return dnfrepo.ItemStack{
		ItemID:   itemID,
		Count:    count,
		RawEntry: realForgingRow(itemID, durability),
		Extra:    values,
	}
}

func realForgingWeaponStack(itemID int64, durability uint16) dnfrepo.ItemStack {
	return realForgingStack(itemID, 1, durability, map[string]string{
		"item_kind":      "equipment",
		"durability":     strconv.Itoa(int(durability)),
		"max_durability": strconv.Itoa(int(durability)),
	})
}

// realForgingBody 按当前 EXE 的写序拼一个 C2S 438 请求体。
func realForgingBody(list byte, slot int16, itemID uint32, materialSlot int16) []byte {
	name := []byte("wf")
	body := make([]byte, 0, 14+len(name))
	body = append(body, list)
	body = append(body, byte(slot), byte(uint16(slot)>>8))
	body = binary.LittleEndian.AppendUint32(body, itemID)
	body = append(body, byte(materialSlot), byte(uint16(materialSlot)>>8))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(name)))
	body = append(body, name...)
	body = append(body, 0)
	return body
}

// runRealForgingCommand 把一条 438 交给桥接层，返回 class1 438 回包体与落库后的背包。
func runRealForgingCommand(t *testing.T, pvfPath string, repos dnfrepo.Group, body []byte) ([]byte, dnfrepo.InventoryRecord) {
	t.Helper()
	service := &Service{
		options: options{
			accountID:          "dnf:forging-real",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			pvfPath:            pvfPath,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		selectedCharacterID: 77,
	}
	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUpgradeItemSeparate),
		body,
	)
	if err != nil {
		t.Fatalf("handleAlignedGameCommand error = %v", err)
	}
	if !handled {
		t.Fatal("forging command was not handled")
	}
	var ack []byte
	remaining := conn.write.Bytes()
	for len(remaining) > 0 {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketUpgradeItemSeparate) &&
			packet.Header.Classification == dnfproto.DefaultChannelClassification {
			ack = append([]byte(nil), packet.Body...)
		}
		remaining = rest
	}
	if ack == nil {
		t.Fatalf("no class1/438 ack on the wire: %x", conn.write.Bytes())
	}
	record, found, loadErr := repos.Inventory.Load(context.Background(), "77")
	if loadErr != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, loadErr)
	}
	return ack, record
}

func TestRealPVFForgingCommandAppliesOrdinaryPortableAndTicket(t *testing.T) {
	pvfPath := realForgingPVFPath(t)
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	table, err := loadCurrentForgingTable(archive)
	if err != nil {
		t.Fatalf("loadCurrentForgingTable error = %v", err)
	}
	if table.maxLevel != 8 || len(table.levels) < 8 {
		t.Fatalf("table = max %d levels %d, want max 8 with 8+ rows", table.maxLevel, len(table.levels))
	}
	if rule := table.materialsByGrade[96]; rule.ItemTemplateID != 3326 || rule.BaseCount != 48 {
		t.Fatalf("grade 96 rule = %+v, want 3326 x48", rule)
	}
	if len(table.rarityWeights) != 7 || table.rarityWeights[4] != 1.40 {
		t.Fatalf("rarity weights = %v, want index 4 = 1.40", table.rarityWeights)
	}

	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("catalog error = %v", err)
	}
	resolver := currentForgingResolver(catalog, archive, table)

	t.Run("resolver classifies real templates", func(t *testing.T) {
		ordinary, err := resolver(realForgingBreathItemID, realForgingWeaponItemID)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if !ordinary.TargetIsWeapon || ordinary.TargetGrade != 96 || ordinary.TargetRarity != 4 || ordinary.TargetDurability != 48 {
			t.Fatalf("weapon = %+v", ordinary)
		}
		if ordinary.PortableFurnace || ordinary.ForgingTicketUsable() {
			t.Fatalf("breath misclassified: %+v", ordinary)
		}
		furnace, err := resolver(realForgingFurnaceItemID, realForgingWeaponItemID)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if !furnace.PortableFurnace || furnace.ForgingTicketUsable() {
			t.Fatalf("furnace = %+v", furnace)
		}
		ticket, err := resolver(realForgingTicketItemID, realForgingTicketTargetID)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if !ticket.ForgingTicketUsable() || !ticket.Ticket.IsFixed || ticket.Ticket.TargetLevel != 5 || ticket.Ticket.SuccessWeight != 10000 {
			t.Fatalf("ticket = %+v", ticket.Ticket)
		}
		listed := false
		for _, candidate := range ticket.TicketWhitelist {
			if candidate == realForgingTicketTargetID {
				listed = true
			}
		}
		if !listed {
			t.Fatalf("whitelist = %v, want %d", ticket.TicketWhitelist, realForgingTicketTargetID)
		}
	})

	t.Run("ordinary forging consumes breath by grade and rarity", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKeyOf(realForgingTargetSlot):   realForgingWeaponStack(realForgingWeaponItemID, 48),
				slotKeyOf(realForgingMaterialSlot): realForgingStack(realForgingBreathItemID, 200, 0, map[string]string{"item_kind": "stackable"}),
			},
		}); err != nil {
			t.Fatalf("save inventory error = %v", err)
		}
		ack, record := runRealForgingCommand(t, pvfPath, repos,
			realForgingBody(0, realForgingTargetSlot, realForgingWeaponItemID, realForgingMaterialSlot))

		// 材料消耗 = max(1, int(48 x 1.40 x 1 + 1e-7)) = 67 ⇒ 剩余 133。
		want := []byte{
			0x01,
			0x79, 0x00,
			0x85, 0x00, 0x00, 0x00,
			0x00,
			0x00,
			0x01,
			0x00,
			0x09, 0x00,
		}
		if !bytes.Equal(ack, want) {
			t.Fatalf("ack = %x, want %x", ack, want)
		}
		target := record.Slots[slotKeyOf(realForgingTargetSlot)]
		if target.RawEntry[0x57] != 1 || target.Extra["byte_57"] != "1" || target.Extra["value_57"] != "1" {
			t.Fatalf("target forge level = raw %d extra %+v", target.RawEntry[0x57], target.Extra)
		}
		if target.RawEntry[0x58] != 0 || target.RawEntry[0x59] != 0 {
			t.Fatalf("tail bytes mutated: %x", target.RawEntry[0x57:0x5A])
		}
		material := record.Slots[slotKeyOf(realForgingMaterialSlot)]
		if material.Count != 133 {
			t.Fatalf("breath count = %d, want 133", material.Count)
		}
	})

	t.Run("damaged weapon is rejected with durability error", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKeyOf(realForgingTargetSlot):   realForgingWeaponStack(realForgingWeaponItemID, 40),
				slotKeyOf(realForgingMaterialSlot): realForgingStack(realForgingBreathItemID, 200, 0, map[string]string{"item_kind": "stackable"}),
			},
		}); err != nil {
			t.Fatalf("save inventory error = %v", err)
		}
		ack, record := runRealForgingCommand(t, pvfPath, repos,
			realForgingBody(0, realForgingTargetSlot, realForgingWeaponItemID, realForgingMaterialSlot))
		if !bytes.Equal(ack, []byte{0, 7}) {
			t.Fatalf("ack = %x, want failure body 0 + 7", ack)
		}
		if record.Slots[slotKeyOf(realForgingMaterialSlot)].Count != 200 {
			t.Fatalf("material consumed on rejection: %d", record.Slots[slotKeyOf(realForgingMaterialSlot)].Count)
		}
		if level := record.Slots[slotKeyOf(realForgingTargetSlot)].RawEntry[0x57]; level != 0 {
			t.Fatalf("target level changed on rejection: %d", level)
		}
	})

	t.Run("portable furnace forges without breath material", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		// 背包里**没有** 3326：便携式锻造炉自己就是材料。
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKeyOf(realForgingTargetSlot):  realForgingWeaponStack(realForgingWeaponItemID, 48),
				slotKeyOf(realForgingFurnaceSlot): realForgingStack(realForgingFurnaceItemID, 3, 0, map[string]string{"item_kind": "stackable"}),
			},
		}); err != nil {
			t.Fatalf("save inventory error = %v", err)
		}
		ack, record := runRealForgingCommand(t, pvfPath, repos,
			realForgingBody(0, realForgingTargetSlot, realForgingWeaponItemID, realForgingFurnaceSlot))
		want := []byte{
			0x01,
			0x1E, 0x00, // 材料槽 30
			0x02, 0x00, 0x00, 0x00, // 炉子 3 -> 2
			0x00, 0x00, 0x01, 0x00,
			0x09, 0x00,
		}
		if !bytes.Equal(ack, want) {
			t.Fatalf("ack = %x, want %x", ack, want)
		}
		if got := record.Slots[slotKeyOf(realForgingFurnaceSlot)].Count; got != 2 {
			t.Fatalf("furnace count = %d, want 2", got)
		}
		if got := record.Slots[slotKeyOf(realForgingTargetSlot)].RawEntry[0x57]; got != 1 {
			t.Fatalf("target level = %d, want 1", got)
		}
	})

	t.Run("fixed +5 ticket jumps the forge level", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKeyOf(realForgingTargetSlot):   realForgingWeaponStack(realForgingTicketTargetID, 45),
				slotKeyOf(realForgingMaterialSlot): realForgingStack(realForgingTicketItemID, 2, 0, map[string]string{"item_kind": "stackable"}),
			},
		}); err != nil {
			t.Fatalf("save inventory error = %v", err)
		}
		ack, record := runRealForgingCommand(t, pvfPath, repos,
			realForgingBody(0, realForgingTargetSlot, realForgingTicketTargetID, realForgingMaterialSlot))
		want := []byte{
			0x01,
			0x79, 0x00,
			0x01, 0x00, 0x00, 0x00, // 券 2 -> 1
			0x00, 0x00, 0x05, 0x00,
			0x09, 0x00,
		}
		if !bytes.Equal(ack, want) {
			t.Fatalf("ack = %x, want %x", ack, want)
		}
		target := record.Slots[slotKeyOf(realForgingTargetSlot)]
		if target.RawEntry[0x57] != 5 || target.Extra["byte_57"] != "5" {
			t.Fatalf("ticket target level = raw %d extra %+v", target.RawEntry[0x57], target.Extra)
		}
		if got := record.Slots[slotKeyOf(realForgingMaterialSlot)].Count; got != 1 {
			t.Fatalf("ticket count = %d, want 1", got)
		}
	})

	t.Run("ticket whitelist rejects an unlisted weapon", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		// 101040493 不在 +5 传承武器锻造券的白名单里（名单是 401xxxxx/402xxxxx 那一族）。
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKeyOf(realForgingTargetSlot):   realForgingWeaponStack(realForgingWeaponItemID, 48),
				slotKeyOf(realForgingMaterialSlot): realForgingStack(realForgingTicketItemID, 2, 0, map[string]string{"item_kind": "stackable"}),
			},
		}); err != nil {
			t.Fatalf("save inventory error = %v", err)
		}
		ack, record := runRealForgingCommand(t, pvfPath, repos,
			realForgingBody(0, realForgingTargetSlot, realForgingWeaponItemID, realForgingMaterialSlot))
		if !bytes.Equal(ack, []byte{0, 4}) {
			t.Fatalf("ack = %x, want failure body 0 + 4", ack)
		}
		if got := record.Slots[slotKeyOf(realForgingMaterialSlot)].Count; got != 2 {
			t.Fatalf("ticket consumed on rejection: %d", got)
		}
		if got := record.Slots[slotKeyOf(realForgingTargetSlot)].RawEntry[0x57]; got != 0 {
			t.Fatalf("target level changed on rejection: %d", got)
		}
	})
}

// slotKeyOf 是主背包 slot key 的本地写法（<list>:<slot>）：真档用例只用 list 0。
func slotKeyOf(slot int16) string {
	return strconv.Itoa(0) + ":" + strconv.Itoa(int(slot))
}
