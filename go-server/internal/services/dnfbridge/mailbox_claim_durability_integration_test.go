package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 端到端回归钉（现场：GM 工具发放的道具是 0 耐久、领取到角色背包后无法修理，
// 并且穿上后重登会掉下来）：
//
//	GM 面板邮件（附件只有 {ItemID, Count}）→ op95 领取 → 落地堆栈带
//	durability/max_durability（运行时 PVF [durability]）+ 合法 quality_seed（实例身份）
//	→ 下发给客户端的 0x77 行 0x06=种子、0x0B=耐久
//	→ op35 修理把耐久从不足恢复到 max 且金币扣减正确
//	→ op19 穿上（equip owner Move）能从这些证据重建 raw 行并落库（不再静默拒绝）。
//
// 修理用 alignedcmd.RepairCostResolver 风格的桩（与线上 dnfbridge 的
// currentRepairCostCatalog.resolve 同形：PVF 的 [durability]/[repair price]/[grade]）。
func TestMailboxClaimedEquipmentDurabilityCanBeRepaired(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "78",
		AccountID:   "acc-durable",
		Name:        "durable",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"gold": 5000},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "78",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "78",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatalf("save equipment: %v", err)
	}
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "78",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID: "1",
				// GM 面板邮件的形状：没有 Extra、没有 raw 行。
				Attachments: []dnfrepo.MailAttachment{{ItemID: 9001, Count: 1}},
				Metadata:    map[string]string{"source": "dnf90-gm-tool"},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     mustMailboxDurabilityCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                &bufferConn{},
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		selectedCharacterID: 78,
	}
	service.bindGameSessionCharacter(session, 78)

	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMailboxExtractItem),
		mailIDListBody(1),
	)
	if err != nil {
		t.Fatalf("mailbox extract: %v", err)
	}
	if !handled {
		t.Fatal("mailbox extract was not handled by the aligned mail module")
	}

	inventory, found, err := repositories.Inventory.Load(ctx, "78")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	claimed, slot, ok := mailboxDurabilityClaimedStack(inventory)
	if !ok {
		t.Fatalf("claimed equipment missing: %+v", inventory.Slots)
	}
	if got := claimed.Extra["durability"]; got != "57" {
		t.Fatalf("claimed durability = %q, want the runtime PVF [durability] 57 (extra=%+v)", got, claimed.Extra)
	}
	if got := claimed.Extra["max_durability"]; got != "57" {
		t.Fatalf("claimed max_durability = %q, want 57 (extra=%+v)", got, claimed.Extra)
	}
	// 实例身份：op19 穿上的 raw 行重建闸门要求 itemquality.Valid(quality_seed)。
	seedRaw := claimed.Extra["quality_seed"]
	seed, seedErr := strconv.ParseUint(seedRaw, 10, 32)
	if seedErr != nil || !itemquality.Valid(uint32(seed)) {
		t.Fatalf("claimed quality_seed = %q (err=%v), want a valid instance seed (extra=%+v)", seedRaw, seedErr, claimed.Extra)
	}
	if claimed.Extra["item_kind"] != "equipment" || claimed.Extra["pvf_path"] == "" {
		t.Fatalf("PVF identity projection incomplete on the claimed stack: %+v", claimed.Extra)
	}
	// 邮件信封的记账键不许留在背包里。
	if _, framed := claimed.Extra["mailbox_pvf_kind"]; framed {
		t.Fatalf("mailbox framing evidence leaked into the inventory stack: %+v", claimed.Extra)
	}

	// 下发给客户端的 0x77 行：0x06 = 品质种子（装备行的实例/数量字段），
	// 0x0B = 耐久 —— 客户端读到的就是这两条，op19 回传的也是这个字段。
	row := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, claimed)
	if got := binary.LittleEndian.Uint32(row.data[6:10]); got != uint32(seed) {
		t.Fatalf("client row identity = %d, want the claimed quality seed %d", got, seed)
	}
	if got := binary.LittleEndian.Uint16(row.data[0x0b:0x0d]); got != 57 {
		t.Fatalf("client row durability = %d, want 57", got)
	}

	// 模拟使用磨损：耐久掉到 5，正是"领出来之后要修"的状态。
	claimed.Extra["durability"] = "5"
	inventory.Slots[mailboxSlotKeyForTest(slot)] = claimed
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatalf("save worn inventory: %v", err)
	}

	evidence := alignedcmd.RepairCostEvidence{
		EquipmentType:   "[weapon]",
		MaxDurability:   57,
		RepairPrice:     6400,
		Grade:           20,
		RepairCostRate:  0.08,
		QuickRepairRate: 1.5,
		UpgradeRates:    []float64{1, 1, 1},
	}
	repair, err := dnfinventory.NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketRepairEquipment),
		Body: []byte{
			dnfrepo.MainInventoryListType,
			byte(slot), 0x00,
			0xFF, 0xFF,
			0x00, 0x00, 0x00,
		},
		AccountID:           "acc-durable",
		SelectedCharacterID: 78,
		Repositories:        repositories,
		RepairCostResolver: func(itemID int64) (alignedcmd.RepairCostEvidence, error) {
			if itemID != 9001 {
				t.Fatalf("repair resolved the wrong item: %d", itemID)
			}
			return evidence, nil
		},
	})
	if err != nil {
		t.Fatalf("repair op: %v", err)
	}
	if !repair.Handled || !repair.ResponseAllowed || repair.Operation != "repair_equipment" {
		t.Fatalf("repair result = %+v (reason=%s)", repair, repair.Reason)
	}

	wantCost := dnfequip.CalcRepairCost(evidence, 5, 0, false)
	if wantCost <= 0 {
		t.Fatalf("test evidence must price a real repair, got cost=%d", wantCost)
	}
	repaired, found, err := repositories.Inventory.Load(ctx, "78")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	stack := repaired.Slots[mailboxSlotKeyForTest(slot)]
	if got := stack.Extra["durability"]; got != "57" {
		t.Fatalf("repaired durability = %q, want 57 (extra=%+v)", got, stack.Extra)
	}
	if got := stack.Extra["max_durability"]; got != "57" {
		t.Fatalf("repaired max_durability = %q, want 57", got)
	}
	character, found, err := repositories.Character.Load(ctx, "78")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	if got := character.Stats["gold"]; got != 5000-wantCost {
		t.Fatalf("gold after repair = %d, want %d (cost=%d)", got, 5000-wantCost, wantCost)
	}

	// op19 穿上：客户端回传的 source identity 就是它读到的装备行实例字段（品质种子）。
	equipOwner, err := dnfequip.NewOwnerWithPlacementValidator(repositories, dnfequip.PlacementValidatorFunc(
		func(context.Context, dnfequip.Placement) error { return nil },
	))
	if err != nil {
		t.Fatalf("equip owner: %v", err)
	}
	move, err := equipOwner.Move(ctx, dnfequip.MoveCommand{
		SelectedCharacterID:      78,
		SourceListType:           dnfrepo.MainInventoryListType,
		SourceSlotIndex:          slot,
		SourceInstanceValue:      int32(seed),
		MoveCount:                1,
		DestinationListType:      mailboxDurabilityTestEquipmentListType,
		DestinationSlotIndex:     mailboxDurabilityTestWeaponSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("equip move error = %v, want a committed equip (the claimed evidence must rebuild the 0x77 row)", err)
	}
	if !move.Changed || move.Mode != "equip" {
		t.Fatalf("equip move result = %+v", move)
	}
	worn, found, err := repositories.Equipment.Load(ctx, "78")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	entry, ok := worn.Entries[strconv.Itoa(int(mailboxDurabilityTestWeaponSlot))]
	if !ok {
		t.Fatalf("worn weapon slot %d missing: %+v", mailboxDurabilityTestWeaponSlot, worn.Entries)
	}
	if len(entry.RawEntry) != 0x77 {
		t.Fatalf("worn raw entry size = %d, want 0x77", len(entry.RawEntry))
	}
	if got := binary.LittleEndian.Uint32(entry.RawEntry[6:10]); got != uint32(seed) {
		t.Fatalf("worn raw identity = %d, want %d", got, seed)
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0b:0x0d]); got != 57 {
		t.Fatalf("worn raw durability = %d, want 57", got)
	}
}

const (
	mailboxDurabilityTestEquipmentListType byte  = 3
	mailboxDurabilityTestWeaponSlot        int16 = 12
)

// mailboxDurabilityClaimedStack 找到领取落地的装备（以及它的槽位号）。
func mailboxDurabilityClaimedStack(inventory dnfrepo.InventoryRecord) (dnfrepo.ItemStack, int16, bool) {
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(mailboxDurabilityTestEquipmentID) {
			continue
		}
		separator := strings.Index(key, ":")
		if separator < 0 {
			continue
		}
		slot, err := strconv.Atoi(key[separator+1:])
		if err != nil {
			continue
		}
		return stack, int16(slot), true
	}
	return dnfrepo.ItemStack{}, 0, false
}

func mailboxSlotKeyForTest(slot int16) string {
	return strconv.Itoa(int(dnfrepo.MainInventoryListType)) + ":" + strconv.Itoa(int(slot))
}
