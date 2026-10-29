package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Current PVF and real handlers with an isolated account; no player DB or GUI.
func TestRealTagDeathSurvivorAndImmediateReentry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未提供真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, reverse), func(t *testing.T) {
				s, p, g := tagPlayFixture(t)
				s.options.channelAdvertiseID = 1
				s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = table, resolver, monsters
				s.tagTournamentCatalog, s.initialEquipmentArchive = catalog, archive
				enableTownMoveSkillProjection(t, s, g, "29")
				ch, _, err := g.Character.Load(context.Background(), "29")
				if err != nil {
					t.Fatal(err)
				}
				ch.Stats["direction"], ch.Stats["area_state"] = 0, 0
				if err = g.Character.Save(context.Background(), ch); err != nil {
					t.Fatal(err)
				}
				if reverse {
					o, err := s.currentTagTournamentOwner()
					if err != nil {
						t.Fatal(err)
					}
					if err = o.RegisterRoster(context.Background(), p.accountID, "29", [2]string{"60", "29"}); err != nil {
						t.Fatal(err)
					}
				}
				p.conn.(*bufferConn).write.Reset()
				if err = s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil {
					t.Fatal(err)
				}
				b := p.tagTournamentBattle
				if b == nil {
					t.Fatal("entry did not create run")
				}
				assertTagCreationRoster(t, p, b)
				request := b.prepared.runtime.Request
				if err = s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
					t.Fatal(err)
				}
				tagPlayMoveNext(t, s, p)
				secondary := 1 - b.run.Snapshot().Active
				if err = s.handleCurrentTagTournamentTag(p, []byte{b.nativeRoleIDs[secondary]}); err != nil {
					t.Fatal(err)
				}
				if b.characters[b.run.Snapshot().Active].CharacterID != "60" {
					t.Fatal("secondary not active")
				}
				if err = s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[secondary]}); err != nil {
					t.Fatal(err)
				}
				if b.run.Snapshot().Active == secondary || b.run.Snapshot().Vitals[secondary].HP != 0 {
					t.Fatal("survivor not active")
				}
				// The survivor must actually be accepted as the authoritative fighter.
				tagPlayKillBoss(t, s, p)
				if b.run.Snapshot().Cleared != 1 {
					t.Fatal("survivor could not finish room")
				}
				before, _, _ := g.Character.Load(context.Background(), "29")
				other, _, _ := g.Character.Load(context.Background(), "60")
				count, rewards := tagBattleMailTotals(t, g)
				if err = s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
				if err = s.confirmCurrentTagTournamentEnterSelectReturn(p); err != nil {
					t.Fatal(err)
				}
				if p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
					t.Fatal("old battle not released")
				}
				p.conn.(*bufferConn).write.Reset()
				if err = s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil {
					t.Fatal(err)
				}
				next := p.tagTournamentBattle
				if next == nil || next == b {
					t.Fatal("paid reentry did not create run")
				}
				assertTagCreationRoster(t, p, next)
				if err = s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
					t.Fatal(err)
				}
				tagPlayMoveNext(t, s, p)
				if next.run.Snapshot().Vitals[0].HP <= 0 || next.run.Snapshot().Vitals[1].HP <= 0 {
					t.Fatal("fresh run contains old death")
				}
				if err = s.handleCurrentTagTournamentTag(p, []byte{next.nativeRoleIDs[secondary]}); err != nil || next.run.Snapshot().Active != secondary {
					t.Fatal("reentry cannot tag", err)
				}
				after, _, _ := g.Character.Load(context.Background(), "29")
				actualOther, _, _ := g.Character.Load(context.Background(), "60")
				actualCount, actualRewards := tagBattleMailTotals(t, g)
				if before.Stats["gold"] != after.Stats["gold"] || !reflect.DeepEqual(other, actualOther) || count != actualCount || !reflect.DeepEqual(rewards, actualRewards) {
					t.Fatal("return/reentry changed assets or duplicated rewards")
				}
			})
		}
	}
}
