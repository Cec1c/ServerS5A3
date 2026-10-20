package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场回归（投影侧）：没有耐久（运行时 PVF 未声明 `[durability]`）的装备 ——
// 首饰 `[amulet]`/`[wrist]`/`[ring]`、左右槽 `[support]`/`[magic stone]`、
// 耳环 `[earring]` —— 穿上之后服务端投出去的 0x0B 必须是 **0**，而不是被丢掉、
// 也不能被"发明"成一个非零耐久。
//
// PVF 证据（runtime/data/dnf/Script.pvf，equipment/equipment.lst 全量实测）：这六类
// 文档一份都没有 `[durability]` 段（ring 3142/3142、amulet 3022/3022、wrist
// 3012/3012、support 2677/2677、magic stone 1651/1651、earring 25/25）。
// 参考实现（86JP ItemMetadataResolver.HasDurabilityByType）只给武器/护甲耐久，
// 这些类型一律 Durability = 0；真实存档 dnf_equipment_entries 里已穿戴的
// amulet(20002)/wrist(22002)/ring(24002) 0x77 行 0x0B 也都是 0。
//
// 所以"投影行耐久字段正确"= 0x0B 恒为 0、行照常投出去、mode1 的 durabilityKnown
// 仍然是 true（0 耐久 ≠ 未知耐久，重登时 actor 装备行不会被降级或丢弃）。
type noDurabilityProjectionCase struct {
	name          string
	itemID        int64
	pvfPath       string
	equipmentType string
	actorSlot     int16
	qualitySeed   uint32
}

var noDurabilityProjectionCases = []noDurabilityProjectionCase{
	{"amulet", 20002, "equipment/character/common/amulet/neck_cloth.equ", "[amulet]", 19, 246295013},
	{"wrist", 22002, "equipment/character/common/wrist/wrist_cloth.equ", "[wrist]", 20, 224077087},
	{"ring", 24002, "equipment/character/common/ring/ring_straw.equ", "[ring]", 21, 746040639},
	{"support", 100341597, "equipment/character/common/support/100341597.equ", "[support]", 22, 572024372},
	{"magic stone", 2747564, "equipment/character/common/magicstone/chn_magicstone_dragonroad_2747564.equ", "[magic stone]", 23, 345678901},
	{"earring", 100390011, "equipment/character/common/earring/100390011.equ", "[earring]", 25, 123456789},
}

// wornNoDurabilityEquipmentEntry 是 equip owner 在 op19 穿上之后落库的 0x77 行形状：
// 耐久字段 0x0B = 0（PVF 未声明 [durability]），身份证据（品质种子）与槽位证据齐全。
func wornNoDurabilityEquipmentEntry(testCase noDurabilityProjectionCase, sourceInstance uint32) dnfrepo.EquipmentEntry {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(testCase.actorSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(testCase.itemID))
	binary.LittleEndian.PutUint32(raw[6:10], testCase.qualitySeed)
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 0)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], sourceInstance)
	return dnfrepo.EquipmentEntry{
		SlotIndex: testCase.actorSlot,
		ItemID:    testCase.itemID,
		RawEntry:  raw,
		Extra: map[string]string{
			"item_kind":                  "equipment",
			"pvf_path":                   testCase.pvfPath,
			"equipment_type":             testCase.equipmentType,
			"quality_seed":               strconv.FormatUint(uint64(testCase.qualitySeed), 10),
			"current_exe_runtime_move":   "1",
			"current_exe_equipment_type": strconv.Itoa(int(testCase.actorSlot)),
			"equipped_slot":              strconv.Itoa(int(testCase.actorSlot)),
			// 注意：**没有** durability / max_durability 键 —— 这正是 PVF 未声明
			// [durability] 的装备在发放端与掉落端的真实形状。
		},
	}
}

func TestNoDurabilityEquipmentProjectsWornRowWithZeroDurability(t *testing.T) {
	const sourceInstance uint32 = 0x05F8A08C
	for _, testCase := range noDurabilityProjectionCases {
		t.Run(testCase.name, func(t *testing.T) {
			equipped := wornNoDurabilityEquipmentEntry(testCase, sourceInstance)

			// 1) 场景/背包装备列表行（op13/op14 的 0x77 行）。
			projected, ok := currentItemListEntryFromEquipment(equipped)
			if !ok {
				t.Fatalf("%s worn row was not projected at all (actor slot %d)", testCase.equipmentType, testCase.actorSlot)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0:2]); got != uint16(testCase.actorSlot) {
				t.Fatalf("projected slot = %d, want %d", got, testCase.actorSlot)
			}
			if got := binary.LittleEndian.Uint32(projected.data[2:6]); got != uint32(testCase.itemID) {
				t.Fatalf("projected item = %d, want %d", got, testCase.itemID)
			}
			if got := binary.LittleEndian.Uint32(projected.data[6:10]); got != testCase.qualitySeed {
				t.Fatalf("projected quality seed = %d, want %d", got, testCase.qualitySeed)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != 0 {
				t.Fatalf("projected durability = %d, want 0 for a PVF item with no [durability]", got)
			}
			if got := currentItemListEquipmentDurability(equipped); got != 0 {
				t.Fatalf("currentItemListEquipmentDurability = %d, want 0", got)
			}

			// 2) 装备更新行（op19/mode 刷新）。
			update := currentEquipmentUpdateEntryFromEquipment(equipped)
			if got := binary.LittleEndian.Uint16(update.data[0x00:0x02]); got != uint16(testCase.actorSlot) {
				t.Fatalf("update slot = %d, want %d", got, testCase.actorSlot)
			}
			if got := binary.LittleEndian.Uint32(update.data[0x02:0x06]); got != uint32(testCase.itemID) {
				t.Fatalf("update item = %d, want %d", got, testCase.itemID)
			}
			if got := binary.LittleEndian.Uint16(update.data[0x0B:0x0D]); got != 0 {
				t.Fatalf("update durability = %d, want 0", got)
			}

			// 3) mode1 重登投影：0 耐久是"已知的 0"，不是"未知"，否则 create 行
			//    会被 currentMode1EquipmentCreateRowHasVerifiedState 降级成 update。
			word, known := currentMode1EquipmentDurabilityWord(equipped, projected)
			if !known || word != 0 {
				t.Fatalf("mode1 durability word = (%d, %v), want (0, true)", word, known)
			}
			row := currentMode1EquipmentObjectRow{
				createEnabled:      true,
				slot:               byte(testCase.actorSlot),
				itemID:             uint32(testCase.itemID),
				equipmentType:      uint64(testCase.actorSlot),
				equipmentTypeKnown: true,
				durability:         word,
				durabilityKnown:    known,
			}
			actorSlot, ok := currentMode1EquipmentActorSlot(row)
			if !ok || actorSlot != byte(testCase.actorSlot) {
				t.Fatalf("mode1 actor slot for %s = (%d, %v), want (%d, true)", testCase.equipmentType, actorSlot, ok, testCase.actorSlot)
			}
			if !row.durabilityKnown {
				t.Fatalf("%s create row lost durabilityKnown with a legitimate 0 durability", testCase.equipmentType)
			}

			// 4) actor 槽位证据：穿上闸门放行的槽位与 PVF 类型映射必须一致。
			runtimeSlot, ok := currentEXEActorEquipmentSlot(equipped)
			if !ok || runtimeSlot != uint64(testCase.actorSlot) {
				t.Fatalf("currentEXEActorEquipmentSlot = (%d, %v), want (%d, true)", runtimeSlot, ok, testCase.actorSlot)
			}
			rule, ok := currentEquipmentPlacementRuleForPVFType(testCase.equipmentType)
			if !ok {
				t.Fatalf("PVF type %q is not mapped by the placement validator", testCase.equipmentType)
			}
			if rule.targetSlot != testCase.actorSlot {
				t.Fatalf("PVF type %q target slot = %d, want %d", testCase.equipmentType, rule.targetSlot, testCase.actorSlot)
			}
			if rule.class != currentEquipmentPlacementClassNormal {
				t.Fatalf("PVF type %q class = %d, want normal", testCase.equipmentType, rule.class)
			}
		})
	}
}

// 领取落地（背包侧）投影：GM 邮件领出来的首饰/左右槽/耳环行没有 0x77 raw 行、也没有
// 耐久键，但身份（quality_seed）齐全；背包行 0x0B 同样是 0，品质种子仍然落在 0x06。
func TestNoDurabilityClaimedStackProjectsBagRowWithZeroDurability(t *testing.T) {
	for _, testCase := range noDurabilityProjectionCases {
		t.Run(testCase.name, func(t *testing.T) {
			stack := dnfrepo.ItemStack{
				ItemID: testCase.itemID,
				Count:  1,
				Extra: map[string]string{
					"item_kind":      "equipment",
					"pvf_path":       testCase.pvfPath,
					"equipment_type": testCase.equipmentType,
					"quality_seed":   strconv.FormatUint(uint64(testCase.qualitySeed), 10),
				},
			}
			projected := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 9, stack)
			if got := binary.LittleEndian.Uint16(projected.data[0:2]); got != 9 {
				t.Fatalf("bag row slot = %d, want 9", got)
			}
			if got := binary.LittleEndian.Uint32(projected.data[2:6]); got != uint32(testCase.itemID) {
				t.Fatalf("bag row item = %d, want %d", got, testCase.itemID)
			}
			if got := binary.LittleEndian.Uint32(projected.data[6:10]); got != testCase.qualitySeed {
				t.Fatalf("bag row quality seed = %d, want %d", got, testCase.qualitySeed)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != 0 {
				t.Fatalf("bag row durability = %d, want 0 for a PVF item with no [durability]", got)
			}
		})
	}
}

// mustNoDurabilityAccessoryCatalog 用真实 PVF 的形状造一份最小目录：六类装备各一件，
// 文档里**没有** `[durability]`（与 runtime/data/dnf/Script.pvf 里这六类完全一致）。
func mustNoDurabilityAccessoryCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "",
	}
	var list strings.Builder
	for _, testCase := range noDurabilityProjectionCases {
		fmt.Fprintf(&list, "%d `%s`\n", testCase.itemID, strings.TrimPrefix(testCase.pvfPath, "equipment/"))
		source[testCase.pvfPath] = fmt.Sprintf(
			"[name] `%s`\n[equipment type] `%s`\n[rarity] 2\n[value] 1000\n[price] 2000\n[minimum level] 1\n",
			testCase.name,
			testCase.equipmentType,
		)
	}
	source["equipment/equipment.lst"] = list.String()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build no-durability accessory catalog: %v", err)
	}
	return catalog
}

// 端到端（bridge 层）钉子：整条现场链路一次走通 ——
//
//	GM 面板邮件（附件只有 {ItemID, Count}）→ op95 领取落地
//	→ 堆栈带身份（item_kind/pvf_path/equipment_type/quality_seed）但**没有**耐久键
//	  （因为运行时 PVF 没声明 [durability]，发放端与领取端都不发明耐久）
//	→ op19 穿上必须提交（旧闸门把"没有耐久键"当证据缺失 → ErrMoveRawEntryMissing
//	  → 服务端静默不回包、客户端本地乐观穿上、重登掉下来）
//	→ 落库的 0x77 行 0x0B = 0，重登投影仍然是同一条、槽位正确。
func TestNoDurabilityAccessoryMailClaimToEquipEndToEnd(t *testing.T) {
	ctx := context.Background()
	catalog := mustNoDurabilityAccessoryCatalog(t)
	for _, testCase := range noDurabilityProjectionCases {
		t.Run(testCase.name, func(t *testing.T) {
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "78",
				AccountID:   "acc-nodeu",
				Name:        "nodeu",
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
						// GM 面板邮件的形状：附件没有 Extra、没有 raw 行。
						Attachments: []dnfrepo.MailAttachment{{ItemID: testCase.itemID, Count: 1}},
						Metadata:    map[string]string{"source": "dnf90-gm-tool"},
					},
				},
			}); err != nil {
				t.Fatalf("save mailbox: %v", err)
			}

			service := &Service{
				options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				pvfItemCatalog:     catalog,
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

			// 1) 领取落地：身份齐全、耐久键一条都没有。
			inventory, found, err := repositories.Inventory.Load(ctx, "78")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			claimed, slot, ok := noDurabilityClaimedStack(inventory, testCase.itemID)
			if !ok {
				t.Fatalf("claimed %s missing: %+v", testCase.equipmentType, inventory.Slots)
			}
			if claimed.Extra["item_kind"] != "equipment" || claimed.Extra["pvf_path"] != testCase.pvfPath {
				t.Fatalf("claimed identity projection incomplete: %+v", claimed.Extra)
			}
			if _, present := claimed.Extra["durability"]; present {
				t.Fatalf("claimed %s invented a durability key: %+v", testCase.equipmentType, claimed.Extra)
			}
			if _, present := claimed.Extra["max_durability"]; present {
				t.Fatalf("claimed %s invented a max_durability key: %+v", testCase.equipmentType, claimed.Extra)
			}
			seedRaw := claimed.Extra["quality_seed"]
			seed, seedErr := strconv.ParseUint(seedRaw, 10, 32)
			if seedErr != nil || !itemquality.Valid(uint32(seed)) {
				t.Fatalf("claimed quality_seed = %q (err=%v), want a valid instance seed", seedRaw, seedErr)
			}
			if len(claimed.RawEntry) != 0 {
				t.Fatalf("GM-mail claimed stack already has a raw row: % X", claimed.RawEntry)
			}

			// 2) op19 穿上：客户端回传的 source identity 就是它读到的实例字段（品质种子）。
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
				DestinationListType:      3,
				DestinationSlotIndex:     testCase.actorSlot,
				DestinationInstanceValue: 0,
			})
			if err != nil {
				t.Fatalf("equip move error = %v, want a committed equip: the %s has no PVF [durability], which must not be read as missing evidence", err, testCase.equipmentType)
			}
			if !move.Changed || move.Mode != "equip" {
				t.Fatalf("equip move result = %+v", move)
			}

			// 3) 落库的 0x77 行：0x0B = 0，身份与槽位证据齐全。
			worn, found, err := repositories.Equipment.Load(ctx, "78")
			if err != nil || !found {
				t.Fatalf("load equipment found=%t err=%v", found, err)
			}
			entry, ok := worn.Entries[strconv.Itoa(int(testCase.actorSlot))]
			if !ok {
				t.Fatalf("worn slot %d missing: %+v", testCase.actorSlot, worn.Entries)
			}
			if len(entry.RawEntry) != currentItemListEntryWireSize {
				t.Fatalf("worn raw entry size = %d, want 0x%X", len(entry.RawEntry), currentItemListEntryWireSize)
			}
			if got := binary.LittleEndian.Uint32(entry.RawEntry[6:10]); got != uint32(seed) {
				t.Fatalf("worn raw identity = %d, want %d", got, seed)
			}
			if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 0 {
				t.Fatalf("worn raw durability = %d, want 0 for a PVF item with no [durability]", got)
			}
			if got := binary.LittleEndian.Uint32(entry.RawEntry[0x0E:0x12]); got != uint32(seed) {
				t.Fatalf("worn raw source identity = %#x, want the op19 source identity %#x", got, uint32(seed))
			}

			// 4) 重登投影：同一条装备行还在原槽，0x0B 仍然是 0。
			reloaded, found, err := repositories.Equipment.Load(ctx, "78")
			if err != nil || !found {
				t.Fatalf("reload equipment found=%t err=%v", found, err)
			}
			projected, ok := currentItemListEntryFromEquipment(reloaded.Entries[strconv.Itoa(int(testCase.actorSlot))])
			if !ok {
				t.Fatalf("reloaded %s row was not projected", testCase.equipmentType)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0:2]); got != uint16(testCase.actorSlot) {
				t.Fatalf("reloaded projected slot = %d, want %d", got, testCase.actorSlot)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != 0 {
				t.Fatalf("reloaded projected durability = %d, want 0", got)
			}
			afterClaim, _, err := repositories.Inventory.Load(ctx, "78")
			if err != nil {
				t.Fatalf("load inventory after equip: %v", err)
			}
			if _, still := afterClaim.Slots[mailboxSlotKeyForTest(slot)]; still {
				t.Fatalf("claimed stack is still in the bag after the equip committed: %+v", afterClaim.Slots)
			}
		})
	}
}

// noDurabilityClaimedStack 找到领取落地的指定道具（以及它的槽位号）。
func noDurabilityClaimedStack(inventory dnfrepo.InventoryRecord, itemID int64) (dnfrepo.ItemStack, int16, bool) {
	for key, stack := range inventory.Slots {
		if stack.ItemID != itemID {
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

// 真实运行时 PVF 抽查（可选，与 TestMailboxDurabilityRealRuntimePVF 同一开关）：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<runtime>/data/dnf/Script.pvf \
//	  go test -buildvcs=false -count=1 -run TestNoDurabilityEquipmentRealRuntimePVF ./internal/services/dnfbridge/
//
// 钉住这六类装备在**真实** PVF 里确实没有 [durability]，而且发放/掉落口径也不会
// 凭空补一个耐久出来（这正是"穿上闸门不许把无耐久当缺证据"的 PVF 依据）。
func TestNoDurabilityEquipmentRealRuntimePVF(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build real runtime-PVF catalog: %v", err)
	}
	for _, testCase := range noDurabilityProjectionCases {
		definition, resolveErr := catalog.ResolveItem(uint32(testCase.itemID))
		if resolveErr != nil {
			t.Fatalf("resolve real PVF item %d: %v", testCase.itemID, resolveErr)
		}
		if definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("item %d kind = %q, want equipment", testCase.itemID, definition.Kind)
		}
		if definition.PVFPath != testCase.pvfPath {
			t.Fatalf("item %d pvf path = %q, want %q", testCase.itemID, definition.PVFPath, testCase.pvfPath)
		}
		if gotType := strings.TrimSpace(definition.EquipmentType); gotType != testCase.equipmentType {
			t.Fatalf("item %d equipment type = %q, want %q", testCase.itemID, gotType, testCase.equipmentType)
		}
		if definition.Durability != 0 {
			t.Fatalf("item %d resolves durability %d, want 0 (the real PVF declares no [durability])", testCase.itemID, definition.Durability)
		}
		document, docErr := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if docErr != nil {
			t.Fatalf("read real PVF item %d at %s: %v", testCase.itemID, definition.PVFPath, docErr)
		}
		for _, name := range []string{"durability", "maximum durability", "max durability"} {
			if _, found := document.Section(name); found {
				t.Fatalf("item %d at %s declares [%s]; the no-durability regression case is not real any more", testCase.itemID, definition.PVFPath, name)
			}
		}
		if projected := mergeCurrentMailAttachmentDurabilityExtra(nil, definition); len(projected) != 0 {
			t.Fatalf("item %d gained invented durability evidence: %+v", testCase.itemID, projected)
		}
	}
}
