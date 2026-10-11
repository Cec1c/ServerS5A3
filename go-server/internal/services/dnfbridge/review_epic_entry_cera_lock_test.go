package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

type epicAuditNotifyAssets struct {
	repo.CharacterAssetUnitOfWork
	entered chan struct{}
}

func (u *epicAuditNotifyAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	close(u.entered)
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, fn)
}
func prepareAuditActual6063Owner(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, repo.Group, boundGameSessionCharacter) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	actual, _, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	table, e := worldmap.LoadSource(ctx, actual.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := newPVFDungeonAICharacterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	s, a, old := newBackToVillageRuntime(t)
	if e = old.Session.Abandon(); e != nil {
		t.Fatal(e)
	}
	g, _ := s.repositoryGroup()
	c, _, e := g.Character.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	c.Level = 90
	c.Stats["town_id"] = 50
	c.Stats["area_id"] = 0
	c.Stats["pos_x"] = 441
	c.Stats["pos_y"] = 207
	if e = g.Character.Save(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: 30}}}); e != nil {
		t.Fatal(e)
	}
	s.options.currentClientVersion = "official-24.9"
	s.worldMapTable = table
	s.worldMapResolver = resolver
	s.dungeonMonsterTable = monsters
	s.dungeonAICharacterTable = ai
	s.initialEquipmentArchive = actual.initialEquipmentArchive
	a.accountID = c.AccountID
	a.loginPacketSeen = true
	a.accountFromToken = true
	if e = g.Account.Save(ctx, repo.AccountRecord{AccountID: c.AccountID}); e != nil {
		t.Fatal(e)
	}
	s.bindGameSessionCharacter(a, 99)
	bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
	r, _, e := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: 6063})
	if e != nil {
		t.Fatal("actual6063 prepare", e)
	}
	if r.epicEntry == nil || r.Dungeon.ID != 6063 || r.Request.DungeonID != 6063 {
		t.Fatal("not real prepared entry")
	}
	if e = freezeCurrentDungeonTownReturnOrigin(a, r); e != nil {
		t.Fatal(e)
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(a)
	if !ok {
		t.Fatal("missing actual binding")
	}
	return s, a, r, g, identity
}

func TestReviewActual6063EntryDoesNotInvertMemoryCeraBindingLock(t *testing.T) {
	s, a, r, g, identity := prepareAuditActual6063Owner(t)
	ctx := context.Background()
	txEntered := make(chan struct{})
	validateBinding := make(chan struct{})
	ceraDone := make(chan error, 1)
	go func() {
		ceraDone <- g.CeraShopAssets.WithinCeraShopAssets(ctx, a.accountID, "99", oe.EpicCampaignScope, func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error {
			close(txEntered)
			<-validateBinding
			if !s.boundGameSessionCharacterCurrent(identity) {
				return errors.New("binding changed")
			}
			return nil
		})
	}()
	select {
	case <-txEntered:
	case e := <-ceraDone:
		t.Fatal("real Cera rejected owner before callback", e)
	case <-time.After(time.Second):
		t.Fatal("real Cera transaction not reached")
	}
	entryEntered := make(chan struct{})
	modified := g
	modified.CharacterAssets = &epicAuditNotifyAssets{g.CharacterAssets, entryEntered}
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	entryDone := make(chan error, 1)
	go func() { entryDone <- s.commitDungeonRuntime(a, r) }()
	select {
	case <-entryEntered:
	case <-time.After(time.Second):
		t.Fatal("actual6063 did not reach CharacterAssets boundary")
	}
	close(validateBinding)
	select {
	case e := <-ceraDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		bag, _, err := g.Inventory.Load(ctx, "99")
		if err != nil || bag.Slots["0:1"].Count != 30 || a.dungeon.runtime == r {
			t.Fatal("unexpected pre-deadlock mutation")
		}
		t.Fatal("actual6063 lock inversion: Cera holds shared aggregateTxMu waits boundCurrent service.mu; entry holds service.mu waits same CharacterAssets aggregateTxMu; tickets30/runtime unchanged")
	}
	select {
	case e := <-entryDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("entry did not finish after Cera binding validation")
	}
}

func TestReviewActual6063SerialCeraBindingAndAdmissionSucceed(t *testing.T) {
	s, a, r, g, identity := prepareAuditActual6063Owner(t)
	ctx := context.Background()
	e := g.CeraShopAssets.WithinCeraShopAssets(ctx, a.accountID, "99", oe.EpicCampaignScope, func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return errors.New("binding changed")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.commitDungeonRuntime(a, r); e != nil {
		t.Fatal("actual6063 serial admission", e)
	}
	bag, _, e := g.Inventory.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	c, _, e := g.Character.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	if bag.Slots["0:1"].Count != 0 || c.Stats["epic_entry_sequence"] != 1 || a.dungeon.runtime != r {
		t.Fatal("serial control did not reach paid admission")
	}
}
