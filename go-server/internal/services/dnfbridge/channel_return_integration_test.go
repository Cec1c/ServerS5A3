package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func TestChannelTown48RequiresActualEmpireMap(t *testing.T) {
	for _, test := range []struct {
		name, profile, path string
		want                bool
	}{
		{"ordinary", "official-24.9", "Town/Ordinary.map", false},
		{"empire", "official-24.9", "Town/CollosseumofEmpire/CollosseumofEmpire.map", true},
		{"old-profile", "90cn", "Town/CollosseumofEmpire/CollosseumofEmpire.map", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			table, err := dnftown.Load(context.Background(), townMoveTestSource{
				"town/town.lst":     "48 `boundary.twn`",
				"town/boundary.twn": fmt.Sprintf("[area]\n0 `%s` `[gate]` 543 255\n[/area]\n", test.path),
			}, dnftown.Options{})
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{currentClientVersion: test.profile}, townCatalog: table}
			if s.currentDedicatedChannelTown(48) != test.want || currentRegisteredChannelTown(48) {
				t.Fatal("town48 classification ignored profile or actual map")
			}
		})
	}
}

// An older saved FiendWar character has the return position but no policy marker.
// Crossing another raid must retain that position, then clear the policy so the
// next ordinary channel switch still enters the ordinary town's Seria room.
func TestFiendWarLegacyOriginSurvivesRaidHopThenClears(t *testing.T) {
	for _, coldHop := range []bool{false, true} {
		for _, coldReturn := range []bool{false, true} {
			t.Run(fmt.Sprintf("hop_%t_return_%t", coldHop, coldReturn), func(t *testing.T) {
				s, session, repos := raidReturnFixture(t)
				ctx := context.Background()
				before, _, err := repos.Character.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				origin := currentReconnectLocationSnapshot(before)
				session.channel = channelcatalog.Channel{ID: 184, Type: 42, Group: "fiendwar"}
				c, _, _, _, _, err := s.currentCharacterListLoginTransition(ctx, session, 29, before)
				if err != nil {
					t.Fatal(err)
				}
				delete(c.Stats, currentFiendWarReturnOriginMarker)
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				session.channel = channelcatalog.Channel{ID: 23, Type: 23, Group: "anton_raid"}
				if coldHop {
					c, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, session, 29, c)
				} else {
					c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, session, 29, c)
					if err == nil {
						c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
					}
				}
				if err != nil || c.Stats[currentFiendWarReturnOriginMarker] != 1 {
					t.Fatalf("legacy FiendWar return policy lost during raid hop: %v", err)
				}
				session.channel = channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"}
				if coldReturn {
					c, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, session, 29, c)
				} else {
					c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, session, 29, c)
				}
				if err != nil || currentReconnectLocationSnapshot(c) != origin || c.Stats[currentFiendWarReturnOriginMarker] != 0 {
					t.Fatalf("FiendWar origin not restored and cleared: %v, %v", c.Stats, err)
				}
				stored, _, err := repos.Character.Load(ctx, "29")
				if err != nil || currentReconnectLocationSnapshot(stored) != origin || stored.Stats[currentFiendWarReturnOriginMarker] != 0 {
					t.Fatal("restored origin and cleared policy were not persisted")
				}
				c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, session, 29, stored)
				if err != nil || c.Stats["town_id"] != 38 || c.Stats["area_id"] != 1 {
					t.Fatalf("completed FiendWar return changed later ordinary channel policy: %v, %v", c.Stats, err)
				}
			})
		}
	}
}
