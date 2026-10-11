package inventory

import (
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
)

// TestEquipmentMoveAffectsSelectedActorAppearance 钉住外观相关性闸门的**纯域判定**：
// 现代运行时穿戴槽（== 外观槽）落在 0..13 才算会改变 mode0 纸娃娃外观；时装列表 17
// 永远算、宠物列表 7 永远不算。槽位口径来源见 dnfbridge/current_actor_mode0_appearance.go
// （`0 <= SlotIndex < 14` 直接分支）与 dnfbridge/equipment_placement.go 的
// currentEquipmentPlacementRuleForPVFType（装扮 0..10 / 武器 12 / 称号 13 / 护甲首饰 14..25 /
// 名牌 30 / 勋章 32）。
func TestEquipmentMoveAffectsSelectedActorAppearance(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		move dnfequip.MoveResult
		want bool
	}{
		{name: "weapon_worn_12", cmd: equipCommand(12), move: equipMove(12), want: true},
		{name: "title_worn_13", cmd: equipCommand(13), move: equipMove(13), want: true},
		{name: "legacy_weapon_worn_11_still_visible", cmd: equipCommand(11), move: equipMove(11), want: true},
		{name: "coat_worn_14_not_visible", cmd: equipCommand(14), move: equipMove(14), want: false},
		{name: "amulet_worn_19_not_visible", cmd: equipCommand(19), move: equipMove(19), want: false},
		{name: "magic_stone_worn_23_not_visible", cmd: equipCommand(23), move: equipMove(23), want: false},
		{name: "earring_worn_25_not_visible", cmd: equipCommand(25), move: equipMove(25), want: false},
		{name: "name_tag_worn_30_not_visible", cmd: equipCommand(30), move: equipMove(30), want: false},
		{name: "flag_worn_32_not_visible", cmd: equipCommand(32), move: equipMove(32), want: false},
		{name: "avatar_list_17_always_visible", cmd: equipCommandWithList(17, 3), move: equipMoveWithList(17, 3), want: true},
		{
			name: "pet_list_7_never_visible",
			cmd:  Command{SourceListType: listTypePet, SourceSlotIndex: 48, DestinationListType: listTypeEquipment, DestinationSlotIndex: 26},
			move: dnfequip.MoveResult{SourceListType: listTypePet, SourceSlotIndex: 48, DestinationListType: listTypeEquipment, DestinationSlotIndex: 26},
			want: false,
		},
	}

	// 装扮/普通护甲按区间批量覆盖：0..10 可见，14..22 不可见。
	for _, slot := range []int16{0, 3, 5, 10} {
		tests = append(tests, struct {
			name string
			cmd  Command
			move dnfequip.MoveResult
			want bool
		}{name: "clothing_worn_slot_visible", cmd: equipCommand(slot), move: equipMove(slot), want: true})
	}
	for _, slot := range []int16{14, 15, 16, 17, 18, 19, 20, 21, 22} {
		tests = append(tests, struct {
			name string
			cmd  Command
			move dnfequip.MoveResult
			want bool
		}{name: "accessory_worn_slot_not_visible", cmd: equipCommand(slot), move: equipMove(slot), want: false})
	}

	// swap：一端可见即真，两端都不可见即假。
	swapWeapon := Command{SourceListType: listTypeMain, SourceSlotIndex: 40, DestinationListType: listTypeEquipment, DestinationSlotIndex: 12}
	swapWeaponMove := dnfequip.MoveResult{Mode: "equip_swap", SourceListType: listTypeMain, SourceSlotIndex: 40, DestinationListType: listTypeEquipment, DestinationSlotIndex: 12, Changed: true, SwappedItemID: 1}
	swapEarring := Command{SourceListType: listTypeMain, SourceSlotIndex: 40, DestinationListType: listTypeEquipment, DestinationSlotIndex: 25}
	swapEarringMove := dnfequip.MoveResult{Mode: "equip_swap", SourceListType: listTypeMain, SourceSlotIndex: 40, DestinationListType: listTypeEquipment, DestinationSlotIndex: 25, Changed: true, SwappedItemID: 1}
	tests = append(tests,
		struct {
			name string
			cmd  Command
			move dnfequip.MoveResult
			want bool
		}{name: "swap_weapon_one_side_visible", cmd: swapWeapon, move: swapWeaponMove, want: true},
		struct {
			name string
			cmd  Command
			move dnfequip.MoveResult
			want bool
		}{name: "swap_earring_neither_side_visible", cmd: swapEarring, move: swapEarringMove, want: false},
	)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := equipmentMoveAffectsSelectedActorAppearance(test.cmd, test.move); got != test.want {
				t.Fatalf("equipmentMoveAffectsSelectedActorAppearance(cmd, move) = %t, want %t (cmd=%+v move=%+v)", got, test.want, test.cmd, test.move)
			}
		})
	}
}

func equipCommand(slot int16) Command {
	return equipCommandWithList(listTypeEquipment, slot)
}

func equipMove(slot int16) dnfequip.MoveResult {
	return equipMoveWithList(listTypeEquipment, slot)
}

func equipCommandWithList(listType byte, slot int16) Command {
	return Command{
		Operation:            "move_itemspace",
		SourceListType:       listTypeMain,
		SourceSlotIndex:      40,
		DestinationListType:  listType,
		DestinationSlotIndex: slot,
		MoveCount:            1,
	}
}

func equipMoveWithList(listType byte, slot int16) dnfequip.MoveResult {
	return dnfequip.MoveResult{
		Mode:                 "equip",
		SourceListType:       listTypeMain,
		SourceSlotIndex:      40,
		DestinationListType:  listType,
		DestinationSlotIndex: slot,
		Changed:              true,
	}
}

// TestEquipAppearanceSlotGateFalsifiesNonVisibleEarring 是证伪测试：闸门 on（默认档）时
// 非可见耳环（槽 25）的穿脱**不再**带 PostActionRefreshSelectedActorAppearance、但仍带
// PostActionRefreshSelectedWearContainers；闸门 off（一键回退）时两者都带。
// 闸门以参数直通（domain 不读 env），env 解析由 dnfbridge 的
// TestEquipAppearanceSlotGateSwitchNormalization 单独钉住。
func TestEquipAppearanceSlotGateFalsifiesNonVisibleEarring(t *testing.T) {
	equipEarring := func() (Command, dnfequip.MoveResult) {
		cmd := Command{
			Operation:            "move_itemspace",
			SourceListType:       listTypeMain,
			SourceSlotIndex:      44,
			DestinationListType:  listTypeEquipment,
			DestinationSlotIndex: 25, // 耳环：外观槽 25 ≥ 14，不进 mode0 body
			MoveCount:            1,
		}
		move := dnfequip.MoveResult{
			Mode:                 "equip",
			SourceListType:       listTypeMain,
			SourceSlotIndex:      44,
			DestinationListType:  listTypeEquipment,
			DestinationSlotIndex: 25,
			Changed:              true,
		}
		return cmd, move
	}

	cmd, move := equipEarring()
	// 末位实参 = 登录复述 noop 的权威刷新开关，取默认档 on：它对**已落库**的穿脱
	// 一个后置动作都不许增减（那条开关只作用于 noopGate=login_wear_reassert_noop）。
	gated := ownerAppliedEquipmentMoveResult(19, cmd, move, false, true)
	wantGated := []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedWearContainers}
	if !reflect.DeepEqual(gated.PostActions, wantGated) {
		t.Fatalf("gate on earring post actions = %v, want %v", gated.PostActions, wantGated)
	}

	cmd, move = equipEarring()
	rollback := ownerAppliedEquipmentMoveResult(19, cmd, move, true, true)
	wantRollback := []alignedcmd.PostAction{
		alignedcmd.PostActionRefreshSelectedWearContainers,
		alignedcmd.PostActionRefreshSelectedActorAppearance,
	}
	if !reflect.DeepEqual(rollback.PostActions, wantRollback) {
		t.Fatalf("gate off earring post actions = %v, want %v", rollback.PostActions, wantRollback)
	}
}
