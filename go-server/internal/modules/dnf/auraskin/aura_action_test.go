package auraskin

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerOpenRequiresTrustedAuraAction(t *testing.T) {
	for _, tc := range []struct {
		name, kind, action string
		ok                 bool
	}{
		{"guild_ticket", ItemStackable, "[open aura skin slot]", true},
		{"wrong_action", ItemStackable, "[open weapon skin slot]", false},
		{"missing_action", ItemStackable, "", false},
		{"wrong_kind", "equipment", "[open aura skin slot]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 490003364, Count: 2, Extra: map[string]string{"action_type": "[open aura skin slot]"}}}}); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(repos, catalogStub{490003364: {Kind: tc.kind, PVFPath: "stackable/490003001/chn_490003364.stk", ActionType: tc.action}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.Open(ctx, Command{AccountID: "account-1", SelectedCharacterID: 19, SourceSlot: 65})
			wantFlag, wantCount := int64(0), int64(2)
			if tc.ok {
				wantFlag, wantCount = 1, 1
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrTicketInvalid) {
				t.Fatalf("error=%v", err)
			}
			char, _, _ := repos.Character.Load(ctx, "19")
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			if char.Stats[FlagStat] != wantFlag || int64(inv.Slots["0:65"].Count) != wantCount {
				t.Fatalf("flag=%d count=%d", char.Stats[FlagStat], inv.Slots["0:65"].Count)
			}
		})
	}
}
