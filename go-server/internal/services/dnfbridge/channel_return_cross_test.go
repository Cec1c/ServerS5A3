package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func assertCombinedOrdinaryRoomPreservesOrigin(t *testing.T, character dnfrepo.CharacterRecord, origin currentReconnectLocation) {
	t.Helper()
	stats := character.Stats
	if stats["town_id"] != 38 || stats["area_id"] != 1 {
		t.Fatalf("ordinary return did not enter the shared Seria room: %v", stats)
	}
	if stats[currentTownPrevVillageTownStat] != origin.townID || stats[currentTownPrevVillageAreaStat] != origin.areaID || stats[currentTownPrevVillagePosXStat] != origin.positionX || stats[currentTownPrevVillagePosYStat] != origin.positionY {
		t.Fatalf("ordinary door source lost: stats=%v origin=%+v", stats, origin)
	}
	if stats[currentPVPReturnOriginMarker] != 0 || stats[currentRaidReturnOriginMarker] != 0 {
		t.Fatalf("return left a stale dedicated-channel origin: %v", stats)
	}
}

// Crossing one dedicated channel through another must retain the original
// ordinary position; neither dedicated town is a valid replacement origin.
func TestPVPFiendWarCrossChannelReturnKeepsOrdinaryOrigin(t *testing.T) {
	for _, pvpFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "pvp-then-fiendwar", false: "fiendwar-then-pvp"}[pvpFirst], func(t *testing.T) {
			table, err := dnftown.Load(context.Background(), townMoveTestSource{
				"town/town.lst":     "10 `pvp.twn` 38 `ordinary.twn` 39 `hendon.twn` 98 `fiend.twn`",
				"town/pvp.twn":      "[area]\n0 `Fair_PVP/Gate_Hendon.map` `[gate]` 474 234\n[/area]\n",
				"town/ordinary.twn": townSeriaFixtureElvengard,
				"town/hendon.twn":   "[area]\n0 `new_HendonMyre.map` `[normal]`\n[/area]\n",
				"town/fiend.twn":    "[area]\n0 `GContents/180621_Fiendwar/Town/Gate_Fiend.map` `[gate]` 474 234\n[/area]\n",
			}, dnftown.Options{})
			if err != nil {
				t.Fatal(err)
			}
			s, session, repos := pvpReturnTownFixture(t, table)
			s.fiendWarConfig = &fiendwar.FiendWarConfig{Battle: &fiendwar.FiendWarBattleConfig{WaitingTown: 98, WaitingArea: 2}}
			ctx := context.Background()
			before, found, err := repos.Character.Load(ctx, "29")
			if err != nil || !found {
				t.Fatalf("load origin: %t %v", found, err)
			}
			origin := currentReconnectLocationSnapshot(before)
			pvp := channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}
			fw := channelcatalog.Channel{ID: 184, Type: 42, Group: "fiendwar"}
			channels := []channelcatalog.Channel{pvp, fw}
			if !pvpFirst {
				channels[0], channels[1] = channels[1], channels[0]
			}
			channels = append(channels, channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"})
			c := before
			for _, channel := range channels {
				session.channel = channel
				var projected dnfrepo.CharacterRecord
				projected, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, session, 29, c)
				if err != nil {
					t.Fatal(err)
				}
				c, err = s.commitCurrentReconnectLocation(ctx, 29, projected)
				if err != nil {
					t.Fatal(err)
				}
			}
			stored, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			assertCombinedOrdinaryRoomPreservesOrigin(t, c, origin)
			assertCombinedOrdinaryRoomPreservesOrigin(t, stored, origin)
			if c.Stats["gold"] != before.Stats["gold"] {
				t.Fatal("channel return changed assets")
			}
		})
	}
}
