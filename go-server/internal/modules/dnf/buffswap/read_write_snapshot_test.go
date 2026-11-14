package buffswap

import (
	"context"
	"encoding/json"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type snapshotModeProbe struct {
	repo.CeraShopAssetUnitOfWork
	reads, writes int
	between       func() error
}

func (p *snapshotModeProbe) WithinBuffOwnedSnapshot(ctx context.Context, a, c, s string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository, repo.BuffPetMetadataReader, repo.OwnedLegacyInventoryRepository) error) error {
	p.writes++
	return p.CeraShopAssetUnitOfWork.(repo.BuffOwnedSnapshotUnitOfWork).WithinBuffOwnedSnapshot(ctx, a, c, s, fn)
}
func (p *snapshotModeProbe) WithinBuffOwnedReadSnapshot(ctx context.Context, a, c, s string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository, repo.BuffPetMetadataReader, repo.OwnedLegacyInventoryRepository) error) error {
	p.reads++
	err := p.CeraShopAssetUnitOfWork.(repo.BuffOwnedSnapshotUnitOfWork).WithinBuffOwnedSnapshot(ctx, a, c, s, fn)
	if err == nil && p.between != nil {
		f := p.between
		p.between = nil
		return f()
	}
	return err
}
func TestBuffMigrationReloadsAfterReadSnapshot(t *testing.T) {
	for _, ownerChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_registration", true: "owner_changed"}[ownerChange], func(t *testing.T) {
			ctx := context.Background()
			g := fixture(t)
			raw, _ := json.Marshal(State{WireProfile: CurrentWireProfile, Skill: 38, Slots: map[byte]Binding{12: {List: 0, Index: 54, ItemID: 101040493}}})
			if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw)}}); err != nil {
				t.Fatal(err)
			}
			probe := &snapshotModeProbe{CeraShopAssetUnitOfWork: g.CeraShopAssets}
			probe.between = func() error {
				if ownerChange {
					ch, _, err := g.Character.Load(ctx, "6")
					if err != nil {
						return err
					}
					ch.AccountID = "new-owner"
					return g.Character.Save(ctx, ch)
				}
				raw, _ := json.Marshal(State{WireProfile: CurrentWireProfile, Skill: 39, Slots: map[byte]Binding{}})
				return g.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw)}})
			}
			g.CeraShopAssets = probe
			got, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, g, "6", "account-6")
			if ownerChange {
				if err == nil {
					t.Fatal("changed owner accepted")
				}
			} else if err != nil || got.State.Skill != 39 || len(got.State.Slots) != 0 {
				t.Fatal("stale registration overwritten", got, err)
			}
			if probe.reads != 1 || probe.writes != 1 {
				t.Fatal("migration did not use separate read and write", probe.reads, probe.writes)
			}
			inv, _, _ := g.Inventory.Load(ctx, "6")
			if inv.Slots["0:54"].Extra["native_buff_instance"] != "" {
				t.Fatal("stale item received identity")
			}
		})
	}
}
func TestBuffCurrentIdentityProjectionUsesOnlyReadSnapshot(t *testing.T) {
	ctx := context.Background()
	g := fixture(t)
	if err := ApplyCurrent(ctx, g, "6", "account-6", Command{38, 12, 0, 54}); err != nil {
		t.Fatal(err)
	}
	probe := &snapshotModeProbe{CeraShopAssetUnitOfWork: g.CeraShopAssets}
	g.CeraShopAssets = probe
	got, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, g, "6", "account-6")
	if err != nil || got.State.Slots[12].Instance == "" || probe.reads != 1 || probe.writes != 0 {
		t.Fatal("ordinary projection took a write transaction", got, err, probe.reads, probe.writes)
	}
}
