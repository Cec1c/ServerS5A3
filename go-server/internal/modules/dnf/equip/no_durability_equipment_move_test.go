package equip

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场回归：**没有耐久（运行时 PVF 未声明 `[durability]`）的装备穿不上**。
//
// PVF 证据（runtime/data/dnf/Script.pvf，equipment/equipment.lst 全量实测）：
// `[ring]` 3142/3142、`[amulet]` 3022/3022、`[wrist]` 3012/3012、`[support]`
// 2677/2677、`[magic stone]` 1651/1651、`[earring]` 25/25 份文档**都没有
// `[durability]` 段**（同时也没有 `[maximum durability]` / `[max durability]`），
// 只有 `[grade]`/`[rarity]`/`[value]`/`[price]`/`[equipment type]`。
//
// 发放端与掉落端因此只在 PVF 声明了正耐久时才写 durability/max_durability
// （gmtool.grantAttachmentExtra、buildCurrentDungeonPickupStack、
// mergeCurrentMailAttachmentDurabilityExtra 全是 `> 0` 才写），所以 GM 邮件领出来的
// 首饰/左右槽/耳环 Extra 里一条耐久键都没有。旧闸门把"没有耐久键"读成
// `durability = -1` → 当作证据缺失 → ErrMoveRawEntryMissing → 事务回滚、服务端
// 静默不回包，客户端本地乐观穿上、重登又掉下来 —— 这就是操作者看到的"穿不上"。
//
// 契约（参考实现 + 真实存档）：
//   - 86JP `ItemMetadataResolver.HasDurabilityByType` 只给武器/护甲耐久，
//     首饰/左右槽/耳环/装扮/宠物一律 Durability = 0；
//   - dnf_equipment_entries 里已穿戴的 amulet(20002)/wrist(22002)/ring(24002)
//     119 字节 0x77 行 0x0B 全部是 0。
//
// 所以"一条耐久键都没有"必须判成**耐久不适用（0x0B 写 0）**，
// 而不是"证据缺失"；身份证据（quality_seed + op19 源实例）的要求一点不放宽。
type noDurabilityEquipmentCase struct {
	name          string
	itemID        int64
	pvfPath       string
	equipmentType string
	equipSlot     int16
}

// 六个类别各取一件真实 PVF 文档（路径与 [equipment type] 均为脚本实测值）。
var noDurabilityEquipmentMoveCases = []noDurabilityEquipmentCase{
	{
		name:          "amulet",
		itemID:        20002,
		pvfPath:       "equipment/character/common/amulet/neck_cloth.equ",
		equipmentType: "[amulet]",
		equipSlot:     19,
	},
	{
		name:          "wrist",
		itemID:        22002,
		pvfPath:       "equipment/character/common/wrist/wrist_cloth.equ",
		equipmentType: "[wrist]",
		equipSlot:     20,
	},
	{
		name:          "ring",
		itemID:        24002,
		pvfPath:       "equipment/character/common/ring/ring_straw.equ",
		equipmentType: "[ring]",
		equipSlot:     21,
	},
	{
		name:          "support",
		itemID:        100341597,
		pvfPath:       "equipment/character/common/support/100341597.equ",
		equipmentType: "[support]",
		equipSlot:     22,
	},
	{
		name:          "magic stone",
		itemID:        2747564,
		pvfPath:       "equipment/character/common/magicstone/chn_magicstone_dragonroad_2747564.equ",
		equipmentType: "[magic stone]",
		equipSlot:     23,
	},
	{
		name:          "earring",
		itemID:        100390011,
		pvfPath:       "equipment/character/common/earring/100390011.equ",
		equipmentType: "[earring]",
		equipSlot:     25,
	},
}

// noDurabilityClaimedExtra 是 GM 邮件领取落地到背包的形状：身份证据齐全
// （item_kind / pvf_path / equipment_type / quality_seed），但**没有**
// durability/max_durability —— 因为运行时 PVF 根本没声明 [durability]。
func noDurabilityClaimedExtra(testCase noDurabilityEquipmentCase, seed uint32) map[string]string {
	return map[string]string{
		"item_kind":      "equipment",
		"pvf_path":       testCase.pvfPath,
		"equipment_type": testCase.equipmentType,
		"quality_seed":   strconv.FormatUint(uint64(seed), 10),
	}
}

func TestNoDurabilityEquipmentMoveEquipsAndPersists(t *testing.T) {
	const sourceInstance int32 = 0x05F8A08C
	for index, testCase := range noDurabilityEquipmentMoveCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seed := uint32(1000000 + index)
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					"0:9": {
						ItemID: testCase.itemID,
						Count:  1,
						Extra:  noDurabilityClaimedExtra(testCase, seed),
					},
				},
			})
			saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
			owner, err := newValidatedTestOwner(repos)
			if err != nil {
				t.Fatal(err)
			}

			result, err := owner.Move(ctx, MoveCommand{
				SelectedCharacterID:      77,
				SourceListType:           wireListMain,
				SourceSlotIndex:          9,
				SourceInstanceValue:      sourceInstance,
				MoveCount:                1,
				DestinationListType:      wireListEquipment,
				DestinationSlotIndex:     testCase.equipSlot,
				DestinationInstanceValue: 0,
			})
			if err != nil {
				t.Fatalf(
					"Move error = %v, want a committed equip: PVF %s declares no [durability], so the absent durability key means \"not applicable\", not \"evidence missing\"",
					err,
					testCase.equipmentType,
				)
			}
			if !result.Changed || result.Mode != "equip" || result.ItemID != testCase.itemID {
				t.Fatalf("result = %+v", result)
			}

			key := strconv.Itoa(int(testCase.equipSlot))
			entry, found := loadTestEquipment(t, ctx, repos, "77").Entries[key]
			if !found {
				t.Fatalf("worn %s slot %d missing: %+v", testCase.equipmentType, testCase.equipSlot, loadTestEquipment(t, ctx, repos, "77").Entries)
			}
			if len(entry.RawEntry) != currentEquipmentRawEntryWireSize {
				t.Fatalf("worn raw entry size = %d, want 0x%X", len(entry.RawEntry), currentEquipmentRawEntryWireSize)
			}
			if got := binary.LittleEndian.Uint16(entry.RawEntry[0:2]); got != uint16(testCase.equipSlot) {
				t.Fatalf("raw slot = %d, want %d", got, testCase.equipSlot)
			}
			if got := binary.LittleEndian.Uint32(entry.RawEntry[2:6]); got != uint32(testCase.itemID) {
				t.Fatalf("raw item = %d, want %d", got, testCase.itemID)
			}
			if got := binary.LittleEndian.Uint32(entry.RawEntry[6:10]); got != seed {
				t.Fatalf("raw quality seed = %d, want %d", got, seed)
			}
			// 耐久不适用 ⇒ 0x0B = 0（与参考实现的 0 及真实存档一致，绝不发明默认耐久）。
			if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 0 {
				t.Fatalf("raw durability = %d, want 0 for a PVF item with no [durability]", got)
			}
			if got := binary.LittleEndian.Uint32(entry.RawEntry[0x0E:0x12]); got != uint32(sourceInstance) {
				t.Fatalf("raw source identity = %#x, want the op19 source identity %#x", got, uint32(sourceInstance))
			}
			// 投影所需的槽位证据：op19 目标槽必须落进 Extra，否则重登时投影
			// 无法把这一行放回 actor 装备表（currentEXEActorEquipmentSlot）。
			if got := entry.Extra["current_exe_equipment_type"]; got != key {
				t.Fatalf("current_exe_equipment_type = %q, want %q", got, key)
			}
			// 穿上必须真的落库（背包行消失），否则就是"本地穿上、重登掉下来"。
			loaded := loadTestInventory(t, ctx, repos, "77")
			if _, ok := loaded.Slots["0:9"]; ok {
				t.Fatalf("claimed stack is still in the bag after the equip committed: %+v", loaded.Slots)
			}
			// 重登：从仓库重新读出来的装备行仍然是这一件、还在同一个槽。
			reloaded := loadTestEquipment(t, ctx, repos, "77").Entries[key]
			if reloaded.ItemID != testCase.itemID || reloaded.SlotIndex != testCase.equipSlot {
				t.Fatalf("reloaded worn row = %+v, want item=%d slot=%d", reloaded, testCase.itemID, testCase.equipSlot)
			}
			if len(reloaded.RawEntry) != currentEquipmentRawEntryWireSize ||
				binary.LittleEndian.Uint16(reloaded.RawEntry[0x0B:0x0D]) != 0 {
				t.Fatalf("reloaded worn raw entry = % X, want a 0x%X row with 0x0B=0", reloaded.RawEntry, currentEquipmentRawEntryWireSize)
			}
		})
	}
}

// 有耐久的装备行为完全不变：PVF 声明了 [durability] 的武器仍然把 PVF 耐久写进 0x0B。
func TestDurableEquipmentMoveKeepsPVFDurability(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9": {
				ItemID: mailClaimedEquipmentExtraID,
				Count:  1,
				Extra:  mailClaimedEquipmentExtra(true),
			},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      77,
		SourceListType:           wireListMain,
		SourceSlotIndex:          9,
		SourceInstanceValue:      mailClaimedSourceInstance,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     mailClaimedEquipWeaponSlot,
		DestinationInstanceValue: 0,
	}); err != nil {
		t.Fatalf("Move error = %v", err)
	}
	entry, found := loadTestEquipment(t, ctx, repos, "77").Entries["12"]
	if !found {
		t.Fatal("worn weapon slot 12 missing")
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != mailClaimedEquipmentDura {
		t.Fatalf("raw durability = %d, want the PVF durability %d", got, mailClaimedEquipmentDura)
	}
}

// 闸门只为"PVF 未声明耐久"这一种形状打开：已经写了耐久键的行必须能读出
// 0..0xffff，坏值/超范围/半截证据仍然失败关闭（不放宽校验）。
func TestEquipmentMoveRejectsBrokenDurabilityEvidence(t *testing.T) {
	cases := map[string]func(map[string]string){
		"unparsable durability": func(extra map[string]string) {
			extra["durability"] = "not-a-number"
		},
		"negative durability": func(extra map[string]string) {
			extra["durability"] = "-1"
		},
		"durability over 0xffff": func(extra map[string]string) {
			extra["durability"] = "70000"
		},
		// 只写一半：有 max_durability 却没有当前耐久，说明这一行**声明了**耐久
		// （PVF 有 [durability]）只是证据残缺 —— max_durability 不能冒充当前耐久，
		// 否则一件武器会被以 0x0B = 0 穿上（等于"穿上就坏"）。
		"only max_durability": func(extra map[string]string) {
			delete(extra, "durability")
			extra["max_durability"] = "57"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			extra := mailClaimedEquipmentExtra(true)
			mutate(extra)
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					"0:9": {ItemID: mailClaimedEquipmentExtraID, Count: 1, Extra: extra},
				},
			})
			saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
			owner, err := newValidatedTestOwner(repos)
			if err != nil {
				t.Fatal(err)
			}

			_, err = owner.Move(ctx, MoveCommand{
				SelectedCharacterID:      77,
				SourceListType:           wireListMain,
				SourceSlotIndex:          9,
				SourceInstanceValue:      mailClaimedSourceInstance,
				MoveCount:                1,
				DestinationListType:      wireListEquipment,
				DestinationSlotIndex:     mailClaimedEquipWeaponSlot,
				DestinationInstanceValue: 0,
			})
			if !errors.Is(err, ErrMoveRawEntryMissing) {
				t.Fatalf("Move error = %v, want %v (broken durability evidence must stay closed)", err, ErrMoveRawEntryMissing)
			}
			if entries := loadTestEquipment(t, ctx, repos, "77").Entries; len(entries) != 0 {
				t.Fatalf("rejected move still wrote worn entries: %+v", entries)
			}
			if _, ok := loadTestInventory(t, ctx, repos, "77").Slots["0:9"]; !ok {
				t.Fatal("rejected move removed the source stack")
			}
		})
	}
}

// 直接钉住 helper 的形状判定：无键 = 不适用（0, true）；坏值 = 失败关闭。
func TestCurrentEquipmentRawEntryDurabilityShape(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]string
		want  uint16
		ok    bool
	}{
		{"no key at all", map[string]string{"item_kind": "equipment"}, 0, true},
		{"blank durability key", map[string]string{"durability": "  "}, 0, true},
		{"explicit zero", map[string]string{"durability": "0"}, 0, true},
		{"pvfr durability", map[string]string{"durability": "57"}, 57, true},
		{"current durability alias", map[string]string{"current_durability": "33"}, 33, true},
		{"max of uint16", map[string]string{"durability": "65535"}, 65535, true},
		{"garbage", map[string]string{"durability": "abc"}, 0, false},
		{"negative", map[string]string{"durability": "-5"}, 0, false},
		{"overflow", map[string]string{"durability": "65536"}, 0, false},
		{"only max_durability", map[string]string{"max_durability": "57"}, 0, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := currentEquipmentRawEntryDurability(testCase.extra)
			if got != testCase.want || ok != testCase.ok {
				t.Fatalf("currentEquipmentRawEntryDurability(%+v) = (%d, %t), want (%d, %t)", testCase.extra, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}
