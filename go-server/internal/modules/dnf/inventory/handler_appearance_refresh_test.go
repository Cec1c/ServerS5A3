package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerAppliedEquipmentMoveUsesCommittedEndpointsWithContainerAndAppearanceRefresh(t *testing.T) {
	for _, test := range []struct {
		slot int16
	}{
		{slot: 0},
		{slot: 8},
		{slot: 9},
		{slot: 10},
		{slot: 11},
		{slot: 12},
		{slot: 25},
		{slot: 26},
		{slot: 27},
		{slot: 29},
		{slot: 30},
		{slot: 32},
	} {
		result := ownerAppliedEquipmentMoveResult(19, Command{
			Operation:            "move_itemspace",
			SourceListType:       listTypeAvatar,
			SourceSlotIndex:      10,
			DestinationListType:  listTypeActorWornAlt,
			DestinationSlotIndex: test.slot,
			MoveCount:            1,
		}, dnfequip.MoveResult{
			Mode:                 "equip",
			SourceListType:       listTypeAvatar,
			SourceSlotIndex:      11,
			DestinationListType:  listTypeEquipment,
			DestinationSlotIndex: test.slot,
			Changed:              true,
		}, false, false)
		wantActions := []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		}
		if len(result.PostActions) != len(wantActions) {
			t.Fatalf("slot %d post actions=%v, want %v", test.slot, result.PostActions, wantActions)
		}
		for index := range wantActions {
			if result.PostActions[index] != wantActions[index] {
				t.Fatalf("slot %d post actions=%v, want %v", test.slot, result.PostActions, wantActions)
			}
		}
		if len(result.ItemSlotRefreshes) != 0 {
			t.Fatalf("slot %d wear move emitted duplicate item slot refreshes=%v", test.slot, result.ItemSlotRefreshes)
		}
		if len(result.UpperResponses) != 1 || len(result.UpperResponses[0].Body) != 11 {
			t.Fatalf("slot %d ACK=%+v", test.slot, result.UpperResponses)
		}
		ack := result.UpperResponses[0].Body
		if ack[1] != listTypeAvatar ||
			binary.LittleEndian.Uint16(ack[2:4]) != 11 ||
			ack[8] != listTypeActorWornAlt ||
			binary.LittleEndian.Uint16(ack[9:11]) != uint16(test.slot) {
			t.Fatalf("slot %d ACK endpoints=%x", test.slot, ack)
		}
	}
}

func TestEquipmentAndAvatarWearMovesNeverEmitItemSlotRefreshes(t *testing.T) {
	for _, test := range []struct {
		name    string
		command Command
		result  dnfequip.MoveResult
	}{
		{
			name: "equipment_equip",
			command: Command{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      44,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 12,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      44,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 12,
				Changed:              true,
			},
		},
		{
			name: "equipment_unequip",
			command: Command{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      45,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 12,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      45,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 12,
				Changed:              true,
			},
		},
		{
			name: "avatar_equip",
			command: Command{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      3,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 8,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      3,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 8,
				Changed:              true,
			},
		},
		{
			name: "avatar_unequip",
			command: Command{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      4,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 7,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      4,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 7,
				Changed:              true,
			},
		},
		{
			name: "alternate_worn_endpoint_list17",
			command: Command{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      5,
				DestinationListType:  listTypeActorWornAlt,
				DestinationSlotIndex: 6,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeAvatar,
				SourceSlotIndex:      5,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 6,
				Changed:              true,
			},
		},
		{
			name: "unchanged_move_has_no_refresh",
			command: Command{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      9,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 11,
			},
			result: dnfequip.MoveResult{
				SourceListType:       listTypeMain,
				SourceSlotIndex:      9,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 11,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.command.Operation = "move_itemspace"
			test.command.MoveCount = 1
			result := ownerAppliedEquipmentMoveResult(19, test.command, test.result, false, false)
			if len(result.ItemSlotRefreshes) != 0 {
				t.Fatalf("wear move emitted duplicate item slot refreshes=%v", result.ItemSlotRefreshes)
			}
			var wantActions []alignedcmd.PostAction
			if test.result.Changed {
				// 穿脱路径用**专用**的整容器动作：它与通用动作逐字节等价，但只有它
				// 能被 dnfbridge 单独按开关取消（DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH）。
				// 见 dnfbridge/equip_move_refresh.go。
				wantActions = []alignedcmd.PostAction{
					alignedcmd.PostActionRefreshSelectedWearContainers,
					alignedcmd.PostActionRefreshSelectedActorAppearance,
				}
			}
			if len(result.PostActions) != len(wantActions) {
				t.Fatalf("wear move post actions=%v, want %v", result.PostActions, wantActions)
			}
			for index := range wantActions {
				if result.PostActions[index] != wantActions[index] {
					t.Fatalf("wear move post actions=%v, want %v", result.PostActions, wantActions)
				}
			}
			if len(result.UpperResponses) != 1 || result.UpperResponses[0].MsgID != 19 {
				t.Fatalf("wear move responses=%+v, want one op19 ACK", result.UpperResponses)
			}
		})
	}
}

// 宠物本体（槽 26）的 op19 回包顺序。
//
// **穿上/换装**：ACK → 宠物投影（op14 槽 26 + op105 + op102）→ 宠物容器（list 7）
// 的**变动槽**（默认档）→ appearance。宠物投影必须先发：2026-09-28 第一轮操作者报障
// "给宠物打完宝珠后重新穿上，约 1 秒内宠物面板里看不到附魔属性"。附魔只由 list 3
// 槽 26 的 op14 行带给客户端，而整容器重发在线上要 ≈0.95 s（客户端 trace：穿宠物
// ACK→mode1 976–1047 ms，穿普通装备只有 41–94 ms），所以宠物投影必须排在容器
// **前面**。
//
// **脱下**：逐字节保持旧顺序 —— 这条路上 `sendSelectedEquippedCreatureItemRefresh`
// 会以 `reason=equipment_slot26_absent` 跳过，根本没有带附魔的槽 26 行可提前。
//
// 2026-09-28 第二轮（操作者报"穿戴宠物后宠物面板里背包和已装备的宠物都消失"）：
// 动作名从 `refresh_selected_item_containers`（5 个整容器）改成
// `refresh_selected_pet_move_slots`（只发本次变动的那一格 list 7），顺序不变。
// 证据与三档回退见 dnfbridge/equip_move_refresh.go 的 petMoveContainerRefreshEnv。
func TestChangedPetEquipRefreshCreatureStateBeforeContainerThenAppearance(t *testing.T) {
	tests := []struct {
		mode string
		want []alignedcmd.PostAction
	}{
		{
			mode: "equip",
			want: []alignedcmd.PostAction{
				alignedcmd.PostActionRefreshSelectedCreatureState,
				alignedcmd.PostActionRefreshSelectedPetMoveSlots,
				alignedcmd.PostActionRefreshSelectedActorAppearance,
			},
		},
		{
			mode: "equip_swap",
			want: []alignedcmd.PostAction{
				alignedcmd.PostActionRefreshSelectedCreatureState,
				alignedcmd.PostActionRefreshSelectedPetMoveSlots,
				alignedcmd.PostActionRefreshSelectedActorAppearance,
			},
		},
		{
			mode: "unequip",
			want: []alignedcmd.PostAction{
				alignedcmd.PostActionRefreshSelectedPetMoveSlots,
				alignedcmd.PostActionRefreshSelectedCreatureState,
				alignedcmd.PostActionRefreshSelectedActorAppearance,
			},
		},
		{
			mode: "unequip_swap",
			want: []alignedcmd.PostAction{
				alignedcmd.PostActionRefreshSelectedPetMoveSlots,
				alignedcmd.PostActionRefreshSelectedCreatureState,
				alignedcmd.PostActionRefreshSelectedActorAppearance,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			result := ownerAppliedPetEquipmentMoveResult(19, Command{
				Operation:            "move_itemspace",
				SourceListType:       listTypePet,
				SourceSlotIndex:      48,
				DestinationListType:  listTypeActorWornAlt,
				DestinationSlotIndex: 26,
				MoveCount:            1,
			}, dnfequip.MoveResult{
				Mode:                 test.mode,
				SourceListType:       listTypePet,
				SourceSlotIndex:      48,
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 26,
				Changed:              true,
			})
			if len(result.ItemSlotRefreshes) != 0 {
				t.Fatalf("pet %s emitted duplicate item slot refreshes=%v", test.mode, result.ItemSlotRefreshes)
			}
			// 宠物容器（list 7）的变动槽只报一次：穿上方向是离开的 48。
			wantSlots := []alignedcmd.ItemSlotRefresh{{ListType: listTypePet, SlotIndex: 48}}
			if len(result.PetMoveSlotRefreshes) != 1 || result.PetMoveSlotRefreshes[0] != wantSlots[0] {
				t.Fatalf("pet %s list-7 changed slots=%v, want %v", test.mode, result.PetMoveSlotRefreshes, wantSlots)
			}
			if len(result.PostActions) != len(test.want) {
				t.Fatalf("pet %s post actions=%v, want %v", test.mode, result.PostActions, test.want)
			}
			for index := range test.want {
				if result.PostActions[index] != test.want[index] {
					t.Fatalf("pet %s post actions=%v, want %v", test.mode, result.PostActions, test.want)
				}
			}
		})
	}
}

func TestPetMoveDetectsWornCreatureSlotAcrossWireAndCommittedEndpoints(t *testing.T) {
	for _, test := range []struct {
		name   string
		cmd    Command
		result dnfequip.MoveResult
	}{
		{
			name: "wire_destination_list17",
			cmd: Command{
				DestinationListType:  listTypeActorWornAlt,
				DestinationSlotIndex: 26,
			},
		},
		{
			name: "wire_source_list3",
			cmd: Command{
				SourceListType:  listTypeEquipment,
				SourceSlotIndex: 26,
			},
		},
		{
			name: "committed_destination_list3",
			result: dnfequip.MoveResult{
				DestinationListType:  listTypeEquipment,
				DestinationSlotIndex: 26,
			},
		},
		{
			name: "committed_source_list17",
			result: dnfequip.MoveResult{
				SourceListType:  listTypeActorWornAlt,
				SourceSlotIndex: 26,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !petMoveTouchesWornCreatureSlot(test.cmd, test.result) {
				t.Fatalf("cmd=%+v result=%+v did not detect worn creature slot", test.cmd, test.result)
			}
		})
	}

	if petMoveTouchesWornCreatureSlot(
		Command{DestinationListType: listTypeActorWornAlt, DestinationSlotIndex: 27},
		dnfequip.MoveResult{DestinationListType: listTypeEquipment, DestinationSlotIndex: 29},
	) {
		t.Fatal("pet artifact endpoints were classified as the worn creature slot")
	}
}

func TestChangedPetArtifactWearMovesRefreshPetContainer(t *testing.T) {
	for slot := int16(27); slot <= 29; slot++ {
		result := ownerAppliedPetEquipmentMoveResult(19, Command{
			Operation:            "move_itemspace",
			SourceListType:       listTypePet,
			SourceSlotIndex:      140 + slot - 27,
			DestinationListType:  listTypeActorWornAlt,
			DestinationSlotIndex: slot,
			MoveCount:            1,
		}, dnfequip.MoveResult{
			Mode:                 "equip",
			SourceListType:       listTypePet,
			SourceSlotIndex:      140 + slot - 27,
			DestinationListType:  listTypeEquipment,
			DestinationSlotIndex: slot,
			Changed:              true,
		})
		if len(result.ItemSlotRefreshes) != 0 {
			t.Fatalf("artifact slot %d emitted duplicate item slot refreshes=%v", slot, result.ItemSlotRefreshes)
		}
		if len(result.PostActions) != 1 ||
			result.PostActions[0] != alignedcmd.PostActionRefreshSelectedPetMoveSlots {
			t.Fatalf("artifact slot %d post actions=%v, want the pet-container slot refresh", slot, result.PostActions)
		}
		if len(result.PetMoveSlotRefreshes) != 1 ||
			result.PetMoveSlotRefreshes[0] != (alignedcmd.ItemSlotRefresh{ListType: listTypePet, SlotIndex: 140 + slot - 27}) {
			t.Fatalf("artifact slot %d list-7 changed slots=%v", slot, result.PetMoveSlotRefreshes)
		}
		if len(result.UpperResponses) != 1 || result.UpperResponses[0].MsgID != 19 {
			t.Fatalf("artifact slot %d responses=%+v, want one op19 ACK", slot, result.UpperResponses)
		}
	}
}

func TestHandlerRoutesOrdinaryActorWornAltThroughEquipmentOwner(t *testing.T) {
	ctx := context.Background()

	t.Run("equip_destination", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 5): {
					ItemID:   700,
					Count:    1,
					RawEntry: make([]byte, currentItemListEntrySize),
				},
			},
		})
		if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
			CharacterID: "77",
			Entries:     map[string]dnfrepo.EquipmentEntry{},
		}); err != nil {
			t.Fatal(err)
		}

		result, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
			Body:                currentMoveItemspaceBody(listTypeMain, 5, 700, 1, listTypeActorWornAlt, 11, 0, 0, -1),
			SelectedCharacterID: 77,
			Repositories:        repos,
			EquipmentPlacement: func(_ context.Context, placement alignedcmd.EquipmentPlacementRequest) error {
				if placement.SourceListType != listTypeMain || placement.SourceSlotIndex != 5 || placement.TargetSlotIndex != 11 {
					t.Fatalf("placement=%+v", placement)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.ResponseAllowed {
			t.Fatalf("result=%+v", result)
		}
		if len(result.ItemSlotRefreshes) != 0 {
			t.Fatalf("list17 equip emitted duplicate item slot refreshes=%v", result.ItemSlotRefreshes)
		}
		wantActions := []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		}
		if len(result.PostActions) != len(wantActions) ||
			result.PostActions[0] != wantActions[0] ||
			result.PostActions[1] != wantActions[1] {
			t.Fatalf("list17 equip post actions=%v, want %v", result.PostActions, wantActions)
		}
		ack := result.UpperResponses[0].Body
		if ack[8] != listTypeActorWornAlt || binary.LittleEndian.Uint16(ack[9:11]) != 11 {
			t.Fatalf("ACK endpoints=%x", ack)
		}
		if _, occupied := loadTestInventory(t, ctx, repos, "77").Slots[slotKey(listTypeMain, 5)]; occupied {
			t.Fatal("inventory source remained occupied")
		}
		equipment, found, err := repos.Equipment.Load(ctx, "77")
		if err != nil || !found || equipment.Entries["11"].ItemID != 700 {
			t.Fatalf("equipment found=%t err=%v record=%+v", found, err, equipment)
		}
	})

	t.Run("unequip_current_exe_target_first_order", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots:       map[string]dnfrepo.ItemStack{},
		})
		if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
			CharacterID: "77",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"11": {
					SlotIndex: 11,
					ItemID:    700,
					RawEntry:  make([]byte, currentItemListEntrySize),
				},
			},
		}); err != nil {
			t.Fatal(err)
		}

		result, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode: uint16(dnfenum.CmdPacketMoveItemspace),
			// 2026-09-28 第五轮：真脱下把身份写在**源端**（穿着的那件在 17:11）；
			// A 形状（源端实例 0 + 装备端点点名那件）由方向判定判 noop。
			Body:                currentMoveItemspaceBody(listTypeActorWornAlt, 11, 700, 1, listTypeMain, 5, 0, 0, -1),
			SelectedCharacterID: 77,
			Repositories:        repos,
			EquipmentPlacement: func(context.Context, alignedcmd.EquipmentPlacementRequest) error {
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.ResponseAllowed {
			t.Fatalf("result=%+v", result)
		}
		if len(result.ItemSlotRefreshes) != 0 {
			t.Fatalf("list17 unequip emitted duplicate item slot refreshes=%v", result.ItemSlotRefreshes)
		}
		wantActions := []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		}
		if len(result.PostActions) != len(wantActions) ||
			result.PostActions[0] != wantActions[0] ||
			result.PostActions[1] != wantActions[1] {
			t.Fatalf("list17 unequip post actions=%v, want %v", result.PostActions, wantActions)
		}
		ack := result.UpperResponses[0].Body
		if ack[1] != listTypeActorWornAlt ||
			binary.LittleEndian.Uint16(ack[2:4]) != 11 ||
			ack[8] != listTypeMain ||
			binary.LittleEndian.Uint16(ack[9:11]) != 5 {
			t.Fatalf("ACK endpoints=%x", ack)
		}
		if stack := loadTestInventory(t, ctx, repos, "77").Slots[slotKey(listTypeMain, 5)]; stack.ItemID != 700 {
			t.Fatalf("unequipped stack=%+v", stack)
		}
	})
}

func TestOwnerAppliedEquipmentMoveAlwaysRefreshesWearContainersThenAppearance(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     int16
		destList   byte
		dest       int16
		appearance bool
	}{
		{name: "direct_unequip_fashion", source: 3, destList: listTypeMain, dest: 28, appearance: true},
		{name: "worn_swap_clears_fashion_source", source: 8, destList: listTypeEquipment, dest: 10, appearance: true},
		{name: "aura", source: 9, destList: listTypeMain, dest: 28, appearance: true},
		{name: "weapon", source: 11, destList: listTypeMain, dest: 28, appearance: true},
		// 槽 26（宠物）/ 28（神器）对 mode0 纸娃娃贡献为零（外观槽 ≥14 被丢弃），
		// 默认档（闸门 on）不再为它们补外观刷新，只保留穿脱专用整容器动作。
		{name: "pet_creature_slot26_not_visible", source: 26, destList: listTypeMain, dest: 28, appearance: false},
		{name: "pet_artifact_slot28_not_visible", source: 28, destList: listTypeMain, dest: 28, appearance: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := ownerAppliedEquipmentMoveResult(19, Command{
				Operation:            "move_itemspace",
				SourceListType:       listTypeEquipment,
				SourceSlotIndex:      test.source,
				DestinationListType:  test.destList,
				DestinationSlotIndex: test.dest,
				MoveCount:            1,
			}, dnfequip.MoveResult{
				Mode:                 "unequip",
				SourceListType:       listTypeEquipment,
				SourceSlotIndex:      test.source,
				DestinationListType:  test.destList,
				DestinationSlotIndex: test.dest,
				Changed:              true,
			}, false, false)
			wantActions := []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedWearContainers}
			if test.appearance {
				wantActions = append(wantActions, alignedcmd.PostActionRefreshSelectedActorAppearance)
			}
			if len(result.PostActions) != len(wantActions) {
				t.Fatalf("post actions = %v, want %v", result.PostActions, wantActions)
			}
			for index := range wantActions {
				if result.PostActions[index] != wantActions[index] {
					t.Fatalf("post actions = %v, want %v", result.PostActions, wantActions)
				}
			}
			if len(result.ItemSlotRefreshes) != 0 {
				t.Fatalf("slot %d wear move emitted duplicate item slot refreshes=%v", test.source, result.ItemSlotRefreshes)
			}
			if len(result.UpperResponses) != 1 || result.UpperResponses[0].MsgID != 19 {
				t.Fatalf("slot %d responses=%+v, want one op19 ACK", test.source, result.UpperResponses)
			}
		})
	}
}
