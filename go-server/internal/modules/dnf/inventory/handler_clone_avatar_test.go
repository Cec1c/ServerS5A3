package inventory

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	cloneAvatarHandlerOrdinary int64 = 101580163
	cloneAvatarHandlerClone    int64 = 42609
)

// TestHandlerWearPersistsCloneAvatarAppearanceThroughRequestClassifier 钉住
// dnfbridge → inventory handler → equip owner 这条**跨层接线**：
// op19 装备搬运的 Request.AvatarClassifier 必须真的被交给 equip.Owner
// （handler.go 的 owner.SetAvatarClassifier(req.AvatarClassifier)）。
//
// 少了那一行时，equip 域拿不到分类器、一个字节都不写，本用例立刻变红 —— 这正是
// 只测 equip 域或只测 dnfbridge 投影都抓不到的缺口。
//
// 分类器语义与真档一致（dnfbridge/clone_avatar_test.go 的 real-PVF 用例）。
func TestHandlerWearPersistsCloneAvatarAppearanceThroughRequestClassifier(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	const (
		characterID uint16 = 711
		avatarSlot  int16  = 2
		wornSlot    int16  = 3
	)
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "711",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeAvatar, avatarSlot): {
				ItemID:   cloneAvatarHandlerClone,
				Count:    0,
				RawEntry: make([]byte, currentItemListEntrySize),
				Extra: map[string]string{
					"item_kind":       "avatar",
					"amount_or_count": "0",
				},
			},
		},
	})
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "711",
		Entries: map[string]dnfrepo.EquipmentEntry{
			wearRoundTripEquipmentKey(wornSlot): {
				SlotIndex: wornSlot,
				ItemID:    cloneAvatarHandlerOrdinary,
				RawEntry:  make([]byte, currentItemListEntrySize),
			},
		},
	}); err != nil {
		t.Fatalf("Save equipment error = %v", err)
	}

	placement := func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil }
	classifier := func(itemID uint32) (bool, bool) {
		switch int64(itemID) {
		case cloneAvatarHandlerOrdinary:
			return true, false
		case cloneAvatarHandlerClone:
			return true, true
		default:
			return false, false
		}
	}

	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(listTypeAvatar, avatarSlot, int32(cloneAvatarHandlerClone), 1, listTypeActorWornAlt, wornSlot, 0, 0, -1),
		SelectedCharacterID: characterID,
		Repositories:        repos,
		EquipmentPlacement:  placement,
		AvatarClassifier:    classifier,
	})
	if err != nil {
		t.Fatalf("Handle clone avatar wear error = %v", err)
	}
	if !result.Handled || !result.ResponseAllowed || result.Operation != "move_itemspace" {
		t.Fatalf("clone avatar wear result = %+v", result)
	}

	entry := requireWearRoundTripEquipmentItem(t, ctx, repos, "711", wornSlot, cloneAvatarHandlerClone, true)
	if got := entry.Extra[dnfrepo.CloneAvatarAppearanceExtraKey]; got != "101580163" {
		t.Fatalf("worn clone appearance key = %q, want the displaced ordinary avatar 101580163; entry=%+v", got, entry)
	}
	returned := requireWearRoundTripInventoryItem(t, ctx, repos, "711", listTypeAvatar, avatarSlot, cloneAvatarHandlerOrdinary, true)
	if got := returned.Extra[dnfrepo.CloneAvatarAppearanceExtraKey]; got != "" {
		t.Fatalf("returned ordinary avatar kept a clone appearance key = %q; stack=%+v", got, returned)
	}
}
