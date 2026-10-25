package dnfbridge

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRealPVFR195LukePhaseTwoRequiresCurrentWaitingMembers(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		mode := "manual"
		if automatic {
			mode = "clock"
		}
		for _, fault := range []string{"absent", "other_area", "other_raid", "other_battle", "other_channel", "old_session", "wrong_account"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				f, leader, peer, flow := partyOnlyLukeSettlementFixture(t)
				enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
				row, found := f.service.onlinePlayers.PlayerForCharacter(peer.selectedCharacterID)
				if !found {
					t.Fatal("waiting fixture missing peer")
				}
				switch fault {
				case "absent":
					f.service.onlinePlayers.LeaveArea(peer.selectedCharacterID)
				case "other_area":
					row.AreaID++
				case "other_raid":
					row.Instance++
				case "other_battle":
					row.RaidBattleInstance += "-old"
				case "other_channel":
					row.Session = &gameSession{accountID: peer.accountID, selectedCharacterID: peer.selectedCharacterID, channel: peer.channel}
					row.Session.channel.ID++
					row.ChannelID++
				case "old_session":
					row.Session = &gameSession{accountID: peer.accountID, selectedCharacterID: peer.selectedCharacterID, channel: peer.channel}
				case "wrong_account":
					row.AccountID += "-old"
				}
				if fault != "absent" {
					f.service.onlinePlayers.EnterArea(&row)
				}
				before := cloneRuntimeRaidState(*f.state)
				bags := raidWaitingInventories(t, f)
				if automatic {
					states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil)
					if len(states) != 0 || len(errs) != 0 {
						t.Fatal("waiting clock advanced or reported retry as failure", states, errs)
					}
				} else if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); ok || !errors.Is(err, errCurrentRaidMembersOutsideWaitingArea) {
					t.Fatal("manual start admitted stale or absent waiting member", ok, err)
				}
				if !reflect.DeepEqual(before, cloneRuntimeRaidState(*f.state)) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
					t.Fatal("rejected next-phase start changed phase or assets")
				}
				enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
				if automatic {
					states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil.Add(time.Second))
					if len(states) != 1 || len(errs) != 0 {
						t.Fatal("confirmed members could not resume clock", states, errs)
					}
				} else if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); !ok || err != nil {
					t.Fatal("confirmed members could not start phase two", ok, err)
				}
				if f.state.Battle.Phase != 1 || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
					t.Fatal("next phase failed or charged again")
				}
			})
		}
	}
}
