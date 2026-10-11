package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// This is the existing pet module's63000/serial123 fixture, not an observed
// q7a pet or proof of an actual-PVF/GUI pet registration.
func reviewList7Inventory(t *testing.T, r repo.Group) {
	t.Helper()
	if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{
		"7:5": {ItemID: 63000, Count: 1, Extra: map[string]string{"creature_serial_or_handle": "123", "item_kind": "equipment"}},
	}}); err != nil {
		t.Fatal(err)
	}
}

func reviewList7Snapshot(t *testing.T, packets []struct {
	msgID uint16
	body  []byte
}, skill uint16, index uint16, bound bool) {
	t.Helper()
	// Independent wire expectation: current1368 skill:u16,count:u8,33*5B rows.
	want := []byte{byte(skill), byte(skill >> 8), 33}
	for slot := byte(0); slot < 33; slot++ {
		row := []byte{0, slot, 45, 255, 255}
		if slot == 26 && bound {
			row = []byte{1, 26, 7, byte(index), byte(index >> 8)}
		}
		want = append(want, row...)
	}
	if len(packets) < 1 || packets[len(packets)-1].msgID != 1368 || !bytes.Equal(packets[len(packets)-1].body, want) {
		t.Fatalf("current list7/empty snapshot mismatch: %v", packets)
	}
}

func TestReviewCurrentBuffList7OwnedInstanceAndColdMovement(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	reviewList7Inventory(t, r)
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 26, 7, 5))
	state, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || !found || state.Slots[26].ItemID != 63000 || state.Slots[26].Instance == "" || state.WireProfile != buffswap.CurrentWireProfile {
		t.Fatalf("owned pet registration missing: state=%+v found=%v err=%v", state, found, err)
	}
	reviewList7Snapshot(t, drainUpperPackets(t, conn), 38, 5, true)
	// A historical skill command cannot demote a saved current-only pet binding
	// into1369 state. Reject atomically; do not poison future Load.
	if err := s.handleGameCommand(session, 1, 1428, reviewCurrentBuffCommand(39, 33, 44, 0xffff)); err != nil {
		t.Fatal(err)
	}
	kept, _, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || kept.Skill != 38 || kept.WireProfile != buffswap.CurrentWireProfile || conn.write.Len() != 0 {
		t.Fatalf("historical command corrupted current-only binding: %+v %v", kept, err)
	}
	inv, _, err := r.Inventory.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	pet := inv.Slots["7:5"]
	if pet.ItemID != 63000 || pet.Count != 1 || pet.Extra["creature_serial_or_handle"] != "123" || pet.Extra["native_buff_instance"] != state.Slots[26].Instance {
		t.Fatalf("pet physical metadata altered: %+v", pet)
	}
	// Move the actual same instance, retaining source list7 and its real key.
	delete(inv.Slots, "7:5")
	inv.Slots["7:6"] = pet
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	for cold := 0; cold < 2; cold++ {
		fresh, cs, cc, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
		cs.postFinishLoadingPlayerStateSent = true
		if err := fresh.sendCurrentFinishLoadingCharacterState(cs, "pet_cold_login"); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, cc)
		if len(packets) < 3 || packets[0].msgID != 37 || packets[1].msgID != 30 {
			t.Fatalf("cold packet order=%v", packets)
		}
		reviewList7Snapshot(t, packets, 38, 6, true)
	}
	// Identical template at the old slot, without identity, must not replace it.
	inv.Slots["7:5"] = repo.ItemStack{ItemID: 63000, Count: 1}
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapply(session, "same_template_control"); err != nil {
		t.Fatal(err)
	}
	reviewList7Snapshot(t, drainUpperPackets(t, conn), 38, 6, true)
	// Copied physical identity is ambiguous; retain registration, omit its row.
	inv.Slots["7:7"] = pet
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapply(session, "copied_identity"); err != nil {
		t.Fatal(err)
	}
	reviewList7Snapshot(t, drainUpperPackets(t, conn), 38, 0, false)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 26, 45, 0xffff))
	reviewList7Snapshot(t, drainUpperPackets(t, conn), 38, 0, false)
	after, _, _ := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if len(after.Slots) != 0 || after.WireProfile != buffswap.CurrentWireProfile {
		t.Fatalf("clear lost current provenance: %+v", after)
	}
}

type reviewList7Legacy struct{ called int }

func (r *reviewList7Legacy) SelectItems(context.Context, string, byte) ([]repo.LegacyInventoryItem, error) {
	r.called++
	return []repo.LegacyInventoryItem{{ListType: 7, SlotIndex: 5, ItemTemplateID: 63000, StackCount: 1, PetSerialOrHandle: 123}}, nil
}

type reviewList7FailSettings struct{ repo.SettingsRepository }

func (r reviewList7FailSettings) Save(context.Context, repo.SettingsRecord) error {
	return errors.New("injected current pet registration settings failure")
}

type reviewList7FailUOW struct{ repo.CeraShopAssetUnitOfWork }

func (u reviewList7FailUOW) WithinCeraShopAssets(ctx context.Context, a, c, scope string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, scope, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, sr repo.SettingsRepository) error {
		return apply(ar, cr, ir, er, reviewList7FailSettings{sr})
	})
}

func TestReviewCurrentBuffList7OwnerAndHistoricalGuards(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, mode := range []string{"foreign_owner", "missing_item", "stack_count_two", "stackable", "legacy_only", "settings_failure", "historical1428"} {
		t.Run(mode, func(t *testing.T) {
			r := newSkillSwitchInventoryRepos(t)
			reviewList7Inventory(t, r)
			legacy := &reviewList7Legacy{}
			s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			session.sceneBootstrapTailSent = false
			inv, _, _ := r.Inventory.Load(context.Background(), "1")
			switch mode {
			case "foreign_owner":
				session.accountID = "other-account"
			case "missing_item", "legacy_only":
				delete(inv.Slots, "7:5")
			case "stack_count_two":
				pet := inv.Slots["7:5"]
				pet.Count = 2
				inv.Slots["7:5"] = pet
			case "stackable":
				pet := inv.Slots["7:5"]
				pet.Extra["item_kind"] = "stackable"
				inv.Slots["7:5"] = pet
			case "settings_failure":
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
			if mode == "legacy_only" {
				r.LegacyInventory = legacy
			}
			// Rebuild Service after repository overrides; no prior session profile.
			s, session, conn, _, _ = newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			if mode == "foreign_owner" {
				session.accountID = "other-account"
			}
			if mode == "historical1428" {
				if err := s.handleGameCommand(session, 1, 1428, reviewCurrentBuffCommand(38, 26, 7, 5)); err != nil {
					t.Fatal(err)
				}
			} else {
				reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 26, 7, 5))
			}
			after, _, _ := r.Inventory.Load(context.Background(), "1")
			if !reflect.DeepEqual(before, after) || conn.write.Len() != 0 {
				_, settingsFound, settingsErr := r.Settings.Load(context.Background(), buffswap.Scope("1"))
				t.Fatalf("rejected pet registration altered assets/loading packets: before=%+v after=%+v connLen=%d settingsFound=%v settingsErr=%v", before, after, conn.write.Len(), settingsFound, settingsErr)
			}
			if _, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1")); err != nil || found {
				t.Fatalf("rejection wrote registration: found=%v err=%v", found, err)
			}
			if legacy.called != 0 {
				t.Fatal("unsupported legacy-only binding must not fabricate or copy an item")
			}
		})
	}
}

func TestReviewCurrentBuffInitialEmptyUsesCurrentWithoutSaving(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	// Historical record remains1369, then an unregistered owned character clears
	// the current client with1368/45. No synthetic registration is persisted.
	if err := buffswap.Apply(context.Background(), r, "1", "dnf:10001", buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "2", AccountID: "dnf:10001"}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	if err := s.sendBuffSwapReapply(session, "historical_control"); err != nil {
		t.Fatal(err)
	}
	old := drainUpperPackets(t, conn)
	if len(old) != 1 || old[0].msgID != 1369 {
		t.Fatalf("historical policy changed: %v", old)
	}
	for cold := 0; cold < 2; cold++ {
		fresh, cs, cc, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
		cs.selectedCharacterID = 2
		cs.postFinishLoadingPlayerStateSent = true
		if err := fresh.sendCurrentFinishLoadingCharacterState(cs, "unregistered_current_cold"); err != nil {
			t.Fatal(err)
		}
		reviewList7Snapshot(t, drainUpperPackets(t, cc), 0, 0, false)
		if _, found, err := r.Settings.Load(context.Background(), buffswap.Scope("2")); err != nil || found {
			t.Fatalf("initial snapshot saved fictional registration: found=%v err=%v", found, err)
		}
	}
	_ = session
}
