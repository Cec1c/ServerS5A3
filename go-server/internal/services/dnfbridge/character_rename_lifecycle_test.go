package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentRenameSelectorReservationBlocksCanonicalPromotion(t *testing.T) {
	s, a, _, _ := renameShopFixture(t)
	s.resetGameSessionForCharacterSelect(a)
	r, err := s.reserveCurrentSelectorRename(context.Background(), a, 19, "owner")
	if err != nil {
		t.Fatal(err)
	}
	replacement := &gameSession{accountID: "owner", selectedCharacterID: 19, characterGeneration: 12}
	done := make(chan struct{})
	go func() { s.promoteResidentGameSession(replacement, 19); close(done) }()
	select {
	case <-done:
		t.Fatal("promotion bypassed rename transaction")
	case <-time.After(25 * time.Millisecond):
	}
	s.releaseCurrentCharacterAssets(r)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("promotion did not resume")
	}
	if _, err := s.reserveCurrentSelectorRename(context.Background(), a, 19, "owner"); err == nil {
		t.Fatal("online target allowed rename")
	}
}

func TestCurrentRenameSelectionRechecksAfterPaidMutation(t *testing.T) {
	s, a, g, _ := renameShopFixture(t)
	ctx := context.Background()
	selected, _, _ := g.Character.Load(ctx, "19")
	if !s.currentRenameSelectionStillCurrent(ctx, a, selected) {
		t.Fatal("unchanged row rejected")
	}
	current := dnfrepo.CloneCharacter(selected)
	current.Stats[characterdata.RenameEntitlementStat] = 1
	_ = g.Character.Save(ctx, current)
	if s.currentRenameSelectionStillCurrent(ctx, a, selected) {
		t.Fatal("stale pending-bit snapshot allowed")
	}
	current.Stats[characterdata.RenameEntitlementStat] = 0
	current.Name = "新名字"
	_ = g.Character.Save(ctx, current)
	if s.currentRenameSelectionStillCurrent(ctx, a, selected) {
		t.Fatal("stale name snapshot allowed")
	}
	// The ordinary selector's already-resolved account remains authoritative,
	// including the existing local compatibility profile without a token.
	a.accountID = ""
	if !s.currentRenameSelectionStillCurrent(ctx, a, current) {
		t.Fatal("ordinary local selection fallback changed")
	}
}

func renamePhysicalFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, a, g, _ := renameShopFixture(t)
	source := s.ceraShopCatalog.source.(bridgePVFSource)
	source["stackable/stackable.lst"] += "2660698 `cash/charcter_rename_card.stk`\n"
	source["stackable/cash/charcter_rename_card.stk"] = "[stackable type]\n`[waste]` 0\n[minimum level]\n1\n[usable job]\n`[all]`\n[action type]\n`[character name change]`\n[action usable place]\n`[village]`\n`[dont use assult]`\n"
	catalog, err := newPVFCeraShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.ceraShopCatalog = catalog
	if err := g.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 2660698, Count: 2}, "0:121": {ItemID: 10015553, Count: 15}}}); err != nil {
		t.Fatal(err)
	}
	return s, a, g
}

func TestCurrentRenameCardWriteFailuresKeepOneDurableRight(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			s, a, g := renamePhysicalFixture(t)
			failure := errors.New("rename write failed")
			a.conn = &failNthDungeonWriteConn{failAt: failAt, err: failure}
			if err := s.handleCurrentRenameCard(a, []byte{65, 0}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "19")
			bag, _, _ := g.Inventory.Load(ctx, "19")
			if c.Stats[characterdata.RenameEntitlementStat] != 1 || bag.Slots["0:65"].Count != 1 || bag.Slots["0:121"].Count != 15 || len(s.characterAssetReservations) != 0 {
				t.Fatal(c, bag)
			}
			if failAt <= 2 {
				a.conn = &bufferConn{}
				if err := s.handleCurrentRenameCard(a, []byte{65, 0}); err != nil {
					t.Fatal(err)
				}
				after, _, _ := g.Inventory.Load(ctx, "19")
				if after.Slots["0:65"].Count != 1 || a.selectedCharacterID != 0 {
					t.Fatal("retry spent card or failed return", after)
				}
			} else if a.selectedCharacterID != 0 {
				t.Fatal("op7 write failed before lifecycle reset")
			}
		})
	}
}

func TestCurrentRenameShopFailedACKDoesNotChargeAgain(t *testing.T) {
	s, a, g, _ := renameShopFixture(t)
	failure := errors.New("shop ACK write failed")
	a.conn = &failNthDungeonWriteConn{failAt: 1, err: failure}
	if err := s.handleCurrentCeraShopPurchase(a, currentCeraShopTestRequestBody(12345)); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	a.conn = &bufferConn{}
	if err := s.handleCurrentCeraShopPurchase(a, currentCeraShopTestRequestBody(12345)); err != nil {
		t.Fatal(err)
	}
	if balance := loadCeraShopAccountCera(t, context.Background(), g, "owner"); balance != 100 {
		t.Fatal("retry charged twice", balance)
	}
	c, _, _ := g.Character.Load(context.Background(), "19")
	if c.Stats[characterdata.RenameEntitlementStat] != 1 || len(s.characterAssetReservations) != 0 {
		t.Fatal(c)
	}
}
