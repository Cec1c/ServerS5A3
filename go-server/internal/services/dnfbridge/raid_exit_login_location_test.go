package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestOrdinaryChannelLoginDoesNotRetainRaidTownOrExitOrigin(t *testing.T) {
	assertOrdinaryLoginAfterRaid(t, false)
}

func TestRealPVFOrdinaryChannelLoginDoesNotRetainRaidTownOrExitOrigin(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("real PVF required")
	}
	assertOrdinaryLoginAfterRaid(t, true)
}

func assertOrdinaryLoginAfterRaid(t *testing.T, realPVF bool) {
	t.Helper()
	var realTable *dnftown.Table
	if realPVF {
		archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")})
		if err != nil {
			t.Fatal(err)
		}
		realTable, err = dnftown.Load(context.Background(), archive, dnftown.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, raidTown := range []int64{19, 32} {
		for _, preserve := range []string{"", "preserve"} {
			service, session, repos := newTownMoveTest(t)
			loadTownSeriaFixtureService(t, service, townMoveTestSource{
				"town/town.lst":     "19 `anton.twn` 32 `luke.twn` 38 `ordinary.twn`",
				"town/anton.twn":    "[area]\n0 `Town/BattleShip/Gate_BattleShip.map` `[gate]` 474 234\n1 `Town/BattleShip/BattleShip_Main.map` `[gate]` 819 172\n[/area]\n",
				"town/luke.twn":     "[area]\n0 `Town/Jelva_raid/Gate_Jelva.map` `[gate]` 474 234\n1 `Town/Jelva_raid/Main.map` `[gate]` 819 172\n[/area]\n",
				"town/ordinary.twn": townSeriaFixtureElvengard,
			})
			ctx := context.Background()
			if realPVF {
				service.townCatalog = realTable
			}
			character, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			character.AccountID = "dnf:1"
			character.Stats["town_id"] = raidTown
			character.Stats["area_id"] = 1
			origin := currentTownPositionSnapshot{CharacterID: 29, TownID: byte(raidTown), AreaID: 1, PositionX: 819, PositionY: 172, MovementCode: 5, PositionValid: true}
			currentTownPrevVillageApplyOriginStats(character.Stats, origin)
			before := dnfrepo.CloneCharacter(character)
			aggregate := dnfrepomemory.NewMemoryGroup()
			if err := aggregate.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			repos.Character, repos.CharacterAssets, repos.CharacterSettlement = aggregate.Character, aggregate.CharacterAssets, aggregate.CharacterSettlement
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			session.channel = channelcatalog.Channel{ID: 11, Type: 11, Group: "cain"}
			session.residentChannel = session.channel
			session.townPositionSnapshot = origin
			session.townPrevVillageSnapshot = origin
			session.townSeriaRoomEntrySnapshot = origin
			t.Setenv(currentLoginPersistedTownEnv, "keep")
			t.Setenv(currentLoginFallbackKeepsTownEnv, preserve)
			projected, town, _, _, _, err := service.currentCharacterListLoginTransition(ctx, session, 29, character)
			if err != nil {
				t.Fatal(err)
			}
			if town != 38 || projected.Stats["town_id"] != 38 {
				t.Fatalf("raid town retained: %d %+v", town, projected.Stats)
			}
			stored, found, err := aggregate.Character.Load(ctx, "29")
			if err != nil || !found || stored.Stats["town_id"] != 38 {
				t.Fatalf("landing not committed: found=%v err=%v stats=%v", found, err, stored.Stats)
			}
			if _, present := stored.Stats[currentTownPrevVillageTownStat]; present {
				t.Fatal("raid exit origin retained in repository")
			}
			if session.townPrevVillageSnapshot.PositionValid || session.townSeriaRoomEntrySnapshot.PositionValid {
				t.Fatal("room door can still return to raid town")
			}
			if !reflect.DeepEqual(before, character) {
				t.Fatal("input character mutated")
			}
			location, _, _, _, err := service.currentCharacterListLoginLocationForSession(session, 29, stored)
			if err != nil || location.TownID != 38 {
				t.Fatalf("relogin returned to raid: %+v %v", location, err)
			}
		}
	}
}
