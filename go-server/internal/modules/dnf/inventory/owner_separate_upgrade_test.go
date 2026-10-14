package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是 op438 锻造（别升级 / separate upgrade）领域层的回归：普通锻造、
// 便携式锻造炉、锻造券三条路都必须在**同一个角色物品事务**里完成
// "材料消耗 + 锻造等级写入"，成功/掷点失败/校验失败三条出口都要有确定结果，
// 且校验类拒绝与存储失败都不许动任何资产。

const (
	forgingTestWeaponItemID   int64 = 700
	forgingTestMaterialItemID int64 = 3326
	forgingTestPortableItemID int64 = 10164035
	forgingTestTicketItemID   int64 = 490000622
	forgingTestLuckyTicketID  int64 = 10007408

	forgingTestTargetSlot   int16 = 9
	forgingTestMaterialSlot int16 = 121
	forgingTestFurnaceSlot  int16 = 30
	forgingTestWornSlot     int16 = 11

	forgingTestTargetDurability = 57
)

// forgingTestRawRow 造一行当前客户端的 0x77 物品行：slot@0、itemID@2、
// ext@0x0A、耐久@0x0B、锻造@0x57、解封等级@0x58、交易限制@0x59
// （与 gmtool 的 ITEMCFG-D3 证据链同一套偏移）。
func forgingTestRawRow(itemID int64, level byte, durability uint16) []byte {
	raw := make([]byte, currentItemListEntrySize)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	raw[0x57] = level
	raw[0x58] = 9
	raw[0x59] = 1
	return raw
}

// forgingTestLevelRows 返回 8 档、每档必定成功的等级表（掷点确定性：
// rand(10000) < 10000 恒真；材料权重恒为 1，于是消耗只由品级基础数量与稀有度决定）。
func forgingTestLevelRows() []alignedcmd.ForgingLevelRow {
	rows := make([]alignedcmd.ForgingLevelRow, 8)
	for index := range rows {
		rows[index] = alignedcmd.ForgingLevelRow{TargetLevel: index + 1, SuccessWeight: 10000, MaterialWeight: 1}
	}
	return rows
}

// forgingTestResolution 是默认的一次成功解析：材料 = 3326、基础数量 2、
// 目标品级 1 / 稀有度 1（权重 1.0）⇒ 每档消耗 max(1, 2 x 1.0 x 1) = 2。
func forgingTestResolution(overrides func(*alignedcmd.ForgingResolution)) alignedcmd.ForgingResolution {
	resolution := alignedcmd.ForgingResolution{
		TablePVFPath: "etc/upgrade_separate.etc",
		MaxLevel:     8,
		Levels:       forgingTestLevelRows(),
		MaterialsByGrade: map[int]alignedcmd.ForgingMaterialRule{
			1: {ItemTemplateID: int(forgingTestMaterialItemID), BaseCount: 2},
		},
		ItemWeightsByRarity: []float64{0.4, 1.0},
		TargetIsWeapon:      true,
		TargetGrade:         1,
		TargetRarity:        1,
		TargetDurability:    forgingTestTargetDurability,
		MaterialItemID:      int(forgingTestMaterialItemID),
	}
	if overrides != nil {
		overrides(&resolution)
	}
	return resolution
}

func forgingTestResolver(resolution alignedcmd.ForgingResolution, err error) alignedcmd.ForgingResolver {
	return func(int64, int64) (alignedcmd.ForgingResolution, error) {
		return resolution, err
	}
}

func forgingCommand(targetList byte, targetSlot int16, targetItemID uint32, materialSlot int16) Command {
	return NewSeparateUpgradeCommand(
		alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77},
		&alignedcmd.ForgingRequest{
			List:         targetList,
			Slot:         targetSlot,
			ItemID:       targetItemID,
			MaterialSlot: materialSlot,
		},
	)
}

// saveForgingFixture 造一份"主背包里一把武器 + 一叠材料"的存档。
func saveForgingFixture(
	t *testing.T,
	ctx context.Context,
	repos dnfrepo.Group,
	targetLevel byte,
	targetExtra map[string]string,
	materialItemID int64,
	materialCount int64,
) {
	t.Helper()
	extra := map[string]string{
		"item_kind":      "equipment",
		"durability":     "57",
		"max_durability": "57",
		"keep_me":        "must-survive",
	}
	for key, value := range targetExtra {
		extra[key] = value
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, forgingTestTargetSlot): {
				ItemID:   forgingTestWeaponItemID,
				Count:    1,
				RawEntry: forgingTestRawRow(forgingTestWeaponItemID, targetLevel, forgingTestTargetDurability),
				Extra:    extra,
			},
			slotKey(listTypeMain, forgingTestMaterialSlot): {
				ItemID:   materialItemID,
				Count:    materialCount,
				RawEntry: forgingTestRawRow(materialItemID, 0, 0),
				Extra: map[string]string{
					"item_kind": "stackable",
					"pvf_path":  "stackable/material/material_3326.stk",
					"keep_me":   "material-must-survive",
				},
			},
		},
	})
}

func newForgingOwner(t *testing.T, repos dnfrepo.Group) *Owner {
	t.Helper()
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	return owner
}

func TestOwnerSeparateUpgradeOrdinarySuccessWritesForgeRow(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 0, nil, forgingTestMaterialItemID, 5)
	owner := newForgingOwner(t, repos)

	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(forgingTestResolution(nil), nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || result.Rejected || !result.Succeeded {
		t.Fatalf("result = %+v, want applied success", result)
	}
	if result.OldLevel != 0 || result.NewLevel != 1 || result.MaterialCost != 2 || result.MaterialRemainingStackCount != 3 {
		t.Fatalf("result levels/cost = %+v", result)
	}
	if !result.LevelChanged || result.PortableFurnace || result.TicketApplied {
		t.Fatalf("result flags = %+v", result)
	}

	loaded := loadTestInventory(t, ctx, repos, "77")
	target := loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]
	if target.Extra["byte_57"] != "1" || target.Extra["value_57"] != "1" {
		t.Fatalf("forge Extra = %+v, want byte_57/value_57 = 1", target.Extra)
	}
	if target.RawEntry[0x57] != 1 {
		t.Fatalf("raw 0x57 = %d, want 1", target.RawEntry[0x57])
	}
	// 额外属性保留：无关 Extra 键与 0x58/0x59（解封等级/交易限制）一个字节都不许动。
	if target.Extra["keep_me"] != "must-survive" || target.Extra["item_kind"] != "equipment" || target.Extra["durability"] != "57" {
		t.Fatalf("target Extra mutated: %+v", target.Extra)
	}
	if target.RawEntry[0x58] != 9 || target.RawEntry[0x59] != 1 {
		t.Fatalf("raw 0x58/0x59 mutated: %x", target.RawEntry[0x56:0x5A])
	}
	if binary.LittleEndian.Uint32(target.RawEntry[2:6]) != uint32(forgingTestWeaponItemID) {
		t.Fatalf("target raw identity mutated: %x", target.RawEntry[:8])
	}

	material := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)]
	if material.Count != 3 {
		t.Fatalf("material count = %d, want 3", material.Count)
	}
	if material.Extra["keep_me"] != "material-must-survive" {
		t.Fatalf("material Extra mutated: %+v", material.Extra)
	}
	if got := binary.LittleEndian.Uint32(material.RawEntry[0x06:0x0A]); got != 3 {
		t.Fatalf("material raw amount = %d, want 3", got)
	}
}

func TestOwnerSeparateUpgradeRollFailureKeepsLevelAndConsumesMaterial(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 3, nil, forgingTestMaterialItemID, 5)
	owner := newForgingOwner(t, repos)

	resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
		r.Levels = forgingTestLevelRows()
		r.Levels[3].SuccessWeight = 0 // 第 4 档（oldLevel 3 -> 4）必定失败
	})
	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(resolution, nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || result.Rejected || result.Succeeded {
		t.Fatalf("result = %+v, want handled roll failure", result)
	}
	if result.OldLevel != 3 || result.NewLevel != 3 || result.LevelChanged {
		t.Fatalf("levels changed on failed roll: %+v", result)
	}
	if result.MaterialRemainingStackCount != 3 {
		t.Fatalf("material left = %d, want 3 (材料在失败时照扣)", result.MaterialRemainingStackCount)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	target := loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]
	if forgingLevelOf(target) != 3 {
		t.Fatalf("target level changed on failed roll: %d", forgingLevelOf(target))
	}
}

func TestOwnerSeparateUpgradeEmptiesMaterialSlotAndReportsZero(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 0, nil, forgingTestMaterialItemID, 2)
	owner := newForgingOwner(t, repos)

	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(forgingTestResolution(nil), nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || result.MaterialRemainingStackCount != 0 {
		t.Fatalf("result = %+v, want empty material slot", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if _, found := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)]; found {
		t.Fatalf("material slot still present: %+v", loaded.Slots)
	}
}

func TestOwnerSeparateUpgradePortableFurnaceNeedsNoBreathMaterial(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	// 主背包里没有 3326，材料槽放的是便携式锻造炉本身。
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, forgingTestTargetSlot): {
				ItemID:   forgingTestWeaponItemID,
				Count:    1,
				RawEntry: forgingTestRawRow(forgingTestWeaponItemID, 0, forgingTestTargetDurability),
				Extra:    map[string]string{"item_kind": "equipment", "durability": "57", "max_durability": "57"},
			},
			slotKey(listTypeMain, forgingTestFurnaceSlot): {
				ItemID:   forgingTestPortableItemID,
				Count:    3,
				RawEntry: forgingTestRawRow(forgingTestPortableItemID, 0, 0),
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/10164001/10164035.stk",
					"stackable_type": "[town and dungeon] 0",
				},
			},
		},
	})
	owner := newForgingOwner(t, repos)

	resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
		r.PortableFurnace = true
		r.MaterialItemID = int(forgingTestPortableItemID)
		r.MaterialPVFPath = "stackable/10164001/10164035.stk"
	})
	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestFurnaceSlot),
		forgingTestResolver(resolution, nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || !result.PortableFurnace || result.MaterialCost != 1 || result.NewLevel != 1 {
		t.Fatalf("result = %+v, want portable furnace applying one forge for one furnace", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, forgingTestFurnaceSlot)].Count; got != 2 {
		t.Fatalf("furnace count = %d, want 2", got)
	}
	if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != 1 {
		t.Fatalf("target forge level = %d, want 1", got)
	}
}

func TestOwnerSeparateUpgradeFixedTicketJumpsToTargetLevel(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 1, nil, forgingTestTicketItemID, 2)
	owner := newForgingOwner(t, repos)

	resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
		r.Ticket = alignedcmd.ForgingTicketDefinition{
			TargetLevel:   5,
			SuccessWeight: 10000,
			IsFixed:       true,
			ApplyValue:    -1,
			PVFPath:       "stackable/event/ticket.stk",
		}
		r.TicketWhitelist = []int64{forgingTestWeaponItemID}
		r.MaterialItemID = int(forgingTestTicketItemID)
	})
	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(resolution, nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || !result.TicketApplied || result.NewLevel != 5 || result.MaterialCost != 1 {
		t.Fatalf("result = %+v, want fixed ticket jumping 1 -> 5", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != 5 {
		t.Fatalf("target forge level = %d, want 5", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)].Count; got != 1 {
		t.Fatalf("ticket count = %d, want 1", got)
	}
}

func TestOwnerSeparateUpgradeTicketWhitelistAndAdditionalCap(t *testing.T) {
	ctx := context.Background()
	t.Run("target outside whitelist", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveForgingFixture(t, ctx, repos, 2, nil, forgingTestTicketItemID, 2)
		owner := newForgingOwner(t, repos)
		resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
			r.Ticket = alignedcmd.ForgingTicketDefinition{TargetLevel: 5, SuccessWeight: 10000, IsFixed: true}
			r.TicketWhitelist = []int64{999999}
			r.MaterialItemID = int(forgingTestTicketItemID)
		})
		result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
			forgingTestResolver(resolution, nil))
		if err != nil {
			t.Fatalf("SeparateUpgrade error = %v", err)
		}
		if result.Success || !result.Rejected || result.ErrorCode != forgingErrorInvalidTarget {
			t.Fatalf("result = %+v, want whitelist rejection", result)
		}
		loaded := loadTestInventory(t, ctx, repos, "77")
		if got := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)].Count; got != 2 {
			t.Fatalf("ticket consumed on rejection: %d", got)
		}
		if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != 2 {
			t.Fatalf("target changed on rejection: %d", got)
		}
	})

	t.Run("additional ticket above apply value", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveForgingFixture(t, ctx, repos, 3, nil, forgingTestTicketItemID, 2)
		owner := newForgingOwner(t, repos)
		resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
			r.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel: 1, SuccessWeight: 10000, IsAdditional: true, ApplyValue: 2,
			}
			r.TicketWhitelist = []int64{forgingTestWeaponItemID}
			r.MaterialItemID = int(forgingTestTicketItemID)
		})
		result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
			forgingTestResolver(resolution, nil))
		if err != nil {
			t.Fatalf("SeparateUpgrade error = %v", err)
		}
		if result.Success || result.ErrorCode != forgingErrorMaxLevel {
			t.Fatalf("result = %+v, want max-level rejection for +1 ticket at +3 with apply value 2", result)
		}
	})

	t.Run("additional ticket at boundary increments level", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveForgingFixture(t, ctx, repos, 2, nil, forgingTestTicketItemID, 2)
		owner := newForgingOwner(t, repos)
		resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
			r.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel: 1, SuccessWeight: 10000, IsAdditional: true, ApplyValue: 2,
			}
			r.TicketWhitelist = []int64{forgingTestWeaponItemID}
			r.MaterialItemID = int(forgingTestTicketItemID)
		})
		result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
			forgingTestResolver(resolution, nil))
		if err != nil {
			t.Fatalf("SeparateUpgrade error = %v", err)
		}
		if !result.Success || result.NewLevel != 3 {
			t.Fatalf("result = %+v, want +1 ticket at boundary 2 -> 3", result)
		}
	})
}

func TestOwnerSeparateUpgradeLuckyTicketNeverLosesLevelOnFailure(t *testing.T) {
	ctx := context.Background()
	t.Run("failure keeps level", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveForgingFixture(t, ctx, repos, 4, nil, forgingTestLuckyTicketID, 1)
		owner := newForgingOwner(t, repos)
		resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
			r.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel: 1, SuccessWeight: 0, IsAdditional: true, ApplyValue: -1,
				FailureDecrementLevel: 0, PVFPath: "stackable/cash/luckyticket.stk",
			}
			r.MaterialItemID = int(forgingTestLuckyTicketID)
		})
		result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
			forgingTestResolver(resolution, nil))
		if err != nil {
			t.Fatalf("SeparateUpgrade error = %v", err)
		}
		if !result.Success || result.Succeeded || result.NewLevel != 4 || result.LevelChanged {
			t.Fatalf("result = %+v, want lucky ticket failure keeping the level", result)
		}
	})

	t.Run("explicit failure decrement applies", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveForgingFixture(t, ctx, repos, 4, nil, forgingTestLuckyTicketID, 1)
		owner := newForgingOwner(t, repos)
		resolution := forgingTestResolution(func(r *alignedcmd.ForgingResolution) {
			r.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel: 1, SuccessWeight: 0, IsAdditional: true, ApplyValue: -1, FailureDecrementLevel: 2,
			}
			r.MaterialItemID = int(forgingTestLuckyTicketID)
		})
		result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot),
			forgingTestResolver(resolution, nil))
		if err != nil {
			t.Fatalf("SeparateUpgrade error = %v", err)
		}
		if result.Succeeded || result.NewLevel != 2 || !result.LevelChanged {
			t.Fatalf("result = %+v, want explicit decrement 4 -> 2", result)
		}
	})
}

func TestOwnerSeparateUpgradeRejectsInvalidAttemptsWithoutTouchingAssets(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		targetList byte
		targetSlot int16
		material   int16
		targetID   uint32
		level      byte
		extra      map[string]string
		overrides  func(*alignedcmd.ForgingResolution)
		wantCode   byte
	}{
		{name: "not a weapon", overrides: func(r *alignedcmd.ForgingResolution) { r.TargetIsWeapon = false }, wantCode: forgingErrorNotWeapon},
		{name: "durability not full", extra: map[string]string{"durability": "40"}, wantCode: forgingErrorDurability},
		{name: "asset locked", extra: map[string]string{"equipment_lock_state": "1"}, wantCode: forgingErrorInvalidTarget},
		{name: "template mismatch", targetID: 701, wantCode: forgingErrorInvalidTarget},
		{name: "max level reached", level: 8, wantCode: forgingErrorMaxLevel},
		{name: "grade rule missing", overrides: func(r *alignedcmd.ForgingResolution) { r.TargetGrade = 42 }, wantCode: forgingErrorUnsupported},
		{name: "rarity out of range", overrides: func(r *alignedcmd.ForgingResolution) { r.TargetRarity = 7 }, wantCode: forgingErrorUnsupported},
		{name: "level row missing", overrides: func(r *alignedcmd.ForgingResolution) { r.Levels = nil }, wantCode: forgingErrorUnsupported},
		{name: "material mismatch", overrides: func(r *alignedcmd.ForgingResolution) {
			r.MaterialsByGrade = map[int]alignedcmd.ForgingMaterialRule{1: {ItemTemplateID: 999, BaseCount: 1}}
		}, wantCode: forgingErrorInvalidMaterial},
		{name: "unsupported target list", targetList: 5, wantCode: forgingErrorInvalidTarget},
		{name: "material equals target", targetSlot: forgingTestMaterialSlot, wantCode: forgingErrorInvalidMaterial},
		{name: "material slot empty", material: 150, wantCode: forgingErrorInvalidMaterial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			saveForgingFixture(t, ctx, repos, tc.level, tc.extra, forgingTestMaterialItemID, 5)
			owner := newForgingOwner(t, repos)
			targetList := tc.targetList
			if targetList == 0 {
				targetList = listTypeMain
			}
			targetSlot := tc.targetSlot
			if targetSlot == 0 {
				targetSlot = forgingTestTargetSlot
			}
			materialSlot := tc.material
			if materialSlot == 0 {
				materialSlot = forgingTestMaterialSlot
			}
			targetID := tc.targetID
			if targetID == 0 {
				targetID = uint32(forgingTestWeaponItemID)
			}
			result, err := owner.SeparateUpgrade(ctx, forgingCommand(targetList, targetSlot, targetID, materialSlot),
				forgingTestResolver(forgingTestResolution(tc.overrides), nil))
			if err != nil {
				t.Fatalf("SeparateUpgrade error = %v", err)
			}
			if result.Success || !result.Rejected || result.ErrorCode != tc.wantCode {
				t.Fatalf("result = %+v, want rejection code %d", result, tc.wantCode)
			}
			if result.RejectionReason == "" {
				t.Fatalf("rejection without a named reason: %+v", result)
			}
			loaded := loadTestInventory(t, ctx, repos, "77")
			if got := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)].Count; got != 5 {
				t.Fatalf("material consumed on rejection: %d", got)
			}
			if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != tc.level {
				t.Fatalf("target forge level = %d, want %d", got, tc.level)
			}
		})
	}
}

func TestOwnerSeparateUpgradeWornTargetWritesEquipmentInstance(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 0, nil, forgingTestMaterialItemID, 5)
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"11": {
				SlotIndex: forgingTestWornSlot,
				ItemID:    forgingTestWeaponItemID,
				RawEntry:  forgingTestRawRow(forgingTestWeaponItemID, 2, forgingTestTargetDurability),
				Extra: map[string]string{
					"item_kind":      "equipment",
					"equipment_slot": "11",
					"durability":     "57",
					"max_durability": "57",
					"keep_me":        "worn-must-survive",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save equipment error = %v", err)
	}
	owner := newForgingOwner(t, repos)

	result, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeEquipment, forgingTestWornSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(forgingTestResolution(nil), nil))
	if err != nil {
		t.Fatalf("SeparateUpgrade error = %v", err)
	}
	if !result.Success || result.NewLevel != 3 || !result.LevelChanged {
		t.Fatalf("result = %+v, want worn weapon 2 -> 3", result)
	}
	equipment, found, err := repos.Equipment.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	entry := equipment.Entries["11"]
	if entry.Extra["byte_57"] != "3" || entry.Extra["value_57"] != "3" {
		t.Fatalf("worn forge Extra = %+v, want byte_57/value_57 = 3", entry.Extra)
	}
	if entry.RawEntry[0x57] != 3 {
		t.Fatalf("worn raw 0x57 = %d, want 3", entry.RawEntry[0x57])
	}
	if entry.Extra["keep_me"] != "worn-must-survive" || entry.RawEntry[0x58] != 9 || entry.RawEntry[0x59] != 1 {
		t.Fatalf("worn instance mutated: extra=%+v raw57..59=%x", entry.Extra, entry.RawEntry[0x57:0x5A])
	}
	// 材料照扣，且主背包的目标槽（list 0 槽 9）不受影响。
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)].Count; got != 3 {
		t.Fatalf("material count = %d, want 3", got)
	}
	if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != 0 {
		t.Fatalf("main-bag target mutated by worn forging: %d", got)
	}
}

func TestOwnerSeparateUpgradeRollsBackWhenEquipmentStoreFails(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 2, nil, forgingTestMaterialItemID, 5)
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"11": {
				SlotIndex: forgingTestWornSlot,
				ItemID:    forgingTestWeaponItemID,
				RawEntry:  forgingTestRawRow(forgingTestWeaponItemID, 2, forgingTestTargetDurability),
				Extra:     map[string]string{"item_kind": "equipment", "durability": "57", "max_durability": "57"},
			},
		},
	}); err != nil {
		t.Fatalf("save equipment error = %v", err)
	}
	// 同一个角色物品事务里装备表写入失败：整条事务都必须回滚。
	repos.CharacterItems = forgingFailingEquipmentSaveUOW{base: repos.CharacterItems}
	owner := newForgingOwner(t, repos)

	if _, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeEquipment, forgingTestWornSlot, 700, forgingTestMaterialSlot),
		forgingTestResolver(forgingTestResolution(nil), nil)); err == nil {
		t.Fatal("SeparateUpgrade succeeded despite injected equipment save failure")
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, forgingTestMaterialSlot)].Count; got != 5 {
		t.Fatalf("material count after rollback = %d, want 5", got)
	}
	if got := forgingLevelOf(loaded.Slots[slotKey(listTypeMain, forgingTestTargetSlot)]); got != 2 {
		t.Fatalf("main-bag target changed after rollback: %d", got)
	}
	equipment, found, loadErr := repos.Equipment.Load(ctx, "77")
	if loadErr != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, loadErr)
	}
	entry := equipment.Entries["11"]
	// 回滚断言：穿戴行必须与注入失败前逐字相同（锻造字节仍是 2、Extra 里没有被写入
	// byte_57/value_57、也没有被动过 keep_me）。
	if entry.RawEntry[0x57] != 2 {
		t.Fatalf("worn raw 0x57 = %d, want 2 after rollback", entry.RawEntry[0x57])
	}
	if _, found := entry.Extra["byte_57"]; found {
		t.Fatalf("worn Extra gained byte_57 despite rollback: %+v", entry.Extra)
	}
	if _, found := entry.Extra["value_57"]; found {
		t.Fatalf("worn Extra gained value_57 despite rollback: %+v", entry.Extra)
	}
}

func TestOwnerSeparateUpgradeRequiresResolver(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveForgingFixture(t, ctx, repos, 0, nil, forgingTestMaterialItemID, 5)
	owner := newForgingOwner(t, repos)
	if _, err := owner.SeparateUpgrade(ctx, forgingCommand(listTypeMain, forgingTestTargetSlot, 700, forgingTestMaterialSlot), nil); err == nil {
		t.Fatal("SeparateUpgrade accepted a nil resolver")
	}
}

// forgingFailingEquipmentSaveUOW 复刻"同一事务里装备表写入失败"的存储故障：
// owner 在事务内先写装备表再写背包表，装备表 Save 返回错误 ⇒ 整条事务不提交。
type forgingFailingEquipmentSaveUOW struct {
	base dnfrepo.CharacterItemUnitOfWork
}

func (u forgingFailingEquipmentSaveUOW) WithinCharacterItems(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return u.base.WithinCharacterItems(ctx, characterID, func(inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		return apply(inventory, forgingFailingEquipmentRepository{base: equipment})
	})
}

type forgingFailingEquipmentRepository struct {
	base dnfrepo.EquipmentRepository
}

func (r forgingFailingEquipmentRepository) Load(ctx context.Context, key string) (dnfrepo.EquipmentRecord, bool, error) {
	return r.base.Load(ctx, key)
}

func (r forgingFailingEquipmentRepository) Save(context.Context, dnfrepo.EquipmentRecord) error {
	return errors.New("injected equipment save failure")
}
