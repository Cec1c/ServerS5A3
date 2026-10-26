package dnfbridge

import (
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestR170RealPVFFiendWarDoorUsesOnlySourceMap(t *testing.T) {
	for _, peerAtSource := range []bool{true, false} {
		name := "destination_player_excluded"
		if peerAtSource {
			name = "source_player_included"
		}
		t.Run(name, func(t *testing.T) {
			f, peers := r134N1Fixture(t)
			s, b := f.service, f.state.Battle
			from, target := 0, 0
			for id, area := range b.FiendWar.Config.Areas {
				if area.Dungeon <= 0 || area.MemberMax < 2 || !b.FiendWar.AreaOpen(id) {
					continue
				}
				for origin, sourceArea := range b.FiendWar.Config.Areas {
					for _, next := range sourceArea.Next {
						if next == id && origin != id && (target == 0 || id < target || id == target && origin < from) {
							from, target = origin, id
						}
					}
				}
			}
			if from == 0 || target == 0 {
				t.Fatal("real PVF has no open battle door")
			}
			// Use the second personal raid seat as the trigger, to cover ordering.
			leader, other := peers[1], peers[0]
			r134N1Presence(t, f, leader, target)
			leader.fiendWarPendingMove = &currentFiendWarMovePlan{raid: f.state.RaidKey, instance: b.InstanceID, character: leader.selectedCharacterID, from: from, to: target, applied: true, confirmed: true}
			peerArea := target
			if peerAtSource {
				peerArea = from
			}
			r134N1Presence(t, f, other, peerArea)
			beforeNumbers := cloneRuntimeRaidBattle(b).MemberGroups
			request := dungeoncmd.SelectDungeonRequest{DungeonID: uint32(b.FiendWar.Config.Areas[target].Dungeon)}
			r134N1Send(t, s, leader, dnfenum.CmdPacketSelectDungeon, encodeCurrentDungeonSelectRequest(request))
			if leader.dungeon.runtime == nil || leader.dungeon.runtime.raidEntry == nil {
				t.Fatal("automatic entry missing", f.logText(t))
			}
			want := 1
			if peerAtSource {
				want = 2
			}
			entry := leader.dungeon.runtime.raidEntry
			if len(entry.Participants) != want || (other.dungeon.runtime != nil) != peerAtSource {
				t.Fatalf("source roster=%v other runtime=%t", entry.Participants, other.dungeon.runtime != nil)
			}
			state := s.runtimePartyStateForSession(leader)
			if state.UserID != leader.selectedCharacterID {
				t.Fatal("triggering player was not entry leader", state.UserID)
			}
			if !reflect.DeepEqual(beforeNumbers, b.MemberGroups) {
				t.Fatal("personal raid numbers changed")
			}
			if b.FiendWarAdmissionClaim.active() {
				t.Fatal("entry leaked admission claim")
			}
		})
	}
}
