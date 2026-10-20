package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 40734 is an existing permanent-avatar fixture. Count0 is constructed here to
// reach the supported non-stackable branch; this is not q7a's actual row/count.
func reviewAvatar0Inventory(t *testing.T, r repo.Group) {
	t.Helper()
	if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{
		"1:2": {ItemID: 40734, Count: 0, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[coat avatar]", "quality_seed": "78750866"}},
	}}); err != nil {
		t.Fatal(err)
	}
}

func reviewAvatar0Snapshot(t *testing.T, packets []struct {
	msgID uint16
	body  []byte
}, index uint16, bound bool) {
	t.Helper()
	want := []byte{38, 0, 33}
	for slot := byte(0); slot < 33; slot++ {
		row := []byte{0, slot, 45, 255, 255}
		if slot == 3 && bound {
			row = []byte{1, 3, 1, byte(index), byte(index >> 8)}
		}
		want = append(want, row...)
	}
	if len(packets) < 1 || packets[len(packets)-1].msgID != 1368 || !bytes.Equal(packets[len(packets)-1].body, want) {
		t.Fatalf("Count0 avatar current snapshot mismatch: %v", packets)
	}
}

func TestReviewCurrentBuffAvatarCount0OwnedMovementAndColdIdentity(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	reviewAvatar0Inventory(t, r)
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 3, 1, 2))
	state, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || !found || state.Slots[3].ItemID != 40734 || state.Slots[3].Instance == "" || state.WireProfile != buffswap.CurrentWireProfile {
		t.Fatalf("owned Count0 avatar registration missing: state=%+v found=%v err=%v", state, found, err)
	}
	reviewAvatar0Snapshot(t, drainUpperPackets(t, conn), 2, true)
	inv, _, _ := r.Inventory.Load(context.Background(), "1")
	avatar := inv.Slots["1:2"]
	if avatar.Count != 0 || avatar.ItemID != 40734 || avatar.Extra["quality_seed"] != "78750866" || avatar.Extra["native_buff_instance"] != state.Slots[3].Instance {
		t.Fatalf("Count0 avatar physical metadata changed: %+v", avatar)
	}
	delete(inv.Slots, "1:2")
	inv.Slots["1:9"] = avatar
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	for cold := 0; cold < 2; cold++ {
		fresh, cs, cc, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
		cs.postFinishLoadingPlayerStateSent = true
		if err := fresh.sendCurrentFinishLoadingCharacterState(cs, "avatar0_cold_login"); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, cc)
		if len(packets) < 3 || packets[0].msgID != 37 || packets[1].msgID != 30 {
			t.Fatalf("Count0 cold packet order=%v", packets)
		}
		reviewAvatar0Snapshot(t, packets, 9, true)
	}
	// Same template without physical identity cannot take over the old slot.
	inv.Slots["1:2"] = repo.ItemStack{ItemID: 40734, Count: 0}
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapply(session, "Count0_template_substitute"); err != nil {
		t.Fatal(err)
	}
	reviewAvatar0Snapshot(t, drainUpperPackets(t, conn), 9, true)
	// When the registered instance vanishes, the substitute remains omitted.
	delete(inv.Slots, "1:9")
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapply(session, "Count0_instance_missing"); err != nil {
		t.Fatal(err)
	}
	reviewAvatar0Snapshot(t, drainUpperPackets(t, conn), 0, false)
	kept, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || !found || kept.Slots[3].Instance != state.Slots[3].Instance {
		t.Fatalf("missing Count0 projection erased registration: %+v %v", kept, err)
	}
}

func TestReviewCurrentBuffAvatarCount0OwnerAndNonStackGuards(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, mode := range []string{"source0_zero", "source7_zero", "avatar_two", "avatar_stackable_zero", "foreign_owner", "missing_avatar", "settings_failure_zero"} {
		t.Run(mode, func(t *testing.T) {
			r := newSkillSwitchInventoryRepos(t)
			reviewAvatar0Inventory(t, r)
			inv, _, _ := r.Inventory.Load(context.Background(), "1")
			list, slot, index := byte(1), byte(3), uint16(2)
			switch mode {
			case "source0_zero":
				inv.Slots["0:2"] = repo.ItemStack{ItemID: 101040493, Count: 0, Extra: map[string]string{"item_kind": "equipment"}}
				list, slot = 0, 12
			case "source7_zero":
				inv.Slots["7:2"] = repo.ItemStack{ItemID: 63000, Count: 0, Extra: map[string]string{"item_kind": "equipment", "creature_serial_or_handle": "123"}}
				list, slot = 7, 26
			case "avatar_two":
				a := inv.Slots["1:2"]
				a.Count = 2
				inv.Slots["1:2"] = a
			case "avatar_stackable_zero":
				a := inv.Slots["1:2"]
				a.Extra["item_kind"] = "stackable"
				inv.Slots["1:2"] = a
			case "missing_avatar":
				delete(inv.Slots, "1:2")
			case "settings_failure_zero":
				r.CeraShopAssets = reviewList7FailUOW{r.CeraShopAssets}
			}
			if err := r.Inventory.Save(context.Background(), inv); err != nil {
				t.Fatal(err)
			}
			// Compare the exact durable representation after fixture persistence.
			// CloneInventory canonically represents an empty Slots map as nil.
			// This is fixture preparation, before the command under test.
			before, beforeFound, beforeErr := r.Inventory.Load(context.Background(), "1")
			if beforeErr != nil || !beforeFound {
				t.Fatalf("persisted fixture before missing: found=%v err=%v", beforeFound, beforeErr)
			}
			s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			session.sceneBootstrapTailSent = false
			if mode == "foreign_owner" {
				session.accountID = "other-account"
			}
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, slot, list, index))
			after, _, _ := r.Inventory.Load(context.Background(), "1")
			if !reflect.DeepEqual(before, after) || conn.write.Len() != 0 {
				_, settingsFound, settingsErr := r.Settings.Load(context.Background(), buffswap.Scope("1"))
				t.Fatalf("Count0 rejection altered inventory/loading frames: before=%+v after=%+v connLen=%d settingsFound=%v settingsErr=%v", before, after, conn.write.Len(), settingsFound, settingsErr)
			}
			if _, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1")); err != nil || found {
				t.Fatalf("Count0 rejection persisted registration: found=%v err=%v", found, err)
			}
		})
	}
}
