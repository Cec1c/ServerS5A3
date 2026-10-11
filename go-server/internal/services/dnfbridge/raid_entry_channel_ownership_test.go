package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRaidDungeonEntryRequiresBattleAdmissionChannel(t *testing.T) {
	for _, stage := range []string{"preflight", "commit"} {
		for _, fault := range []string{"admitted", "other_channel", "other_type", "missing_channel", "missing_type", "ordinary_channel"} {
			t.Run(stage+"/"+fault, func(t *testing.T) {
				f := raidSafetyFixture(t, nil, 2)
				f.service.options.currentClientVersion = "official-24.9"
				leader, peer := f.session(), f.session()
				leader.loginPacketSeen, leader.accountFromToken = true, true
				peer.selectedCharacterID, peer.accountID = 20, "account-20"
				peer.loginPacketSeen, peer.accountFromToken = true, true
				f.service.bindGameSessionCharacter(leader, 19)
				f.service.bindGameSessionCharacter(peer, 20)
				for i := range f.state.Members {
					f.state.Members[i].GroupIndex = 1
				}
				f.state.Started = true
				f.state.Battle = &runtimeRaidBattle{InstanceID: "admitted-channel", Mode: 1,
					ChannelID: leader.channel.ID, ChannelType: leader.channel.Type,
					EnterAfter: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Hour),
					Dungeons: []raid.DungeonState{{ID: 210, State: 0}}, MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}}
				l := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 210}, AccountID: "account-1", Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
				p := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 210}, AccountID: "account-20", Character: dnfrepo.CharacterRecord{CharacterID: "20"}}
				followers := []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: p}}
				if stage == "commit" {
					if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, l, followers); err != nil {
						t.Fatal(err)
					}
				}
				switch fault {
				case "other_channel":
					f.state.Battle.ChannelID++
				case "other_type":
					f.state.Battle.ChannelType = dnfenum.HiddenRaidType
				case "missing_channel":
					f.state.Battle.ChannelID = 0
				case "missing_type":
					f.state.Battle.ChannelType = 0
				case "ordinary_channel":
					leader.channel.ID, leader.channel.Type = 11, 0
					peer.channel.ID, peer.channel.Type = 11, 0
				}
				if stage == "preflight" {
					err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, l, followers)
					if (err == nil) != (fault == "admitted") {
						t.Fatalf("preflight admitted=%v fault=%s", err == nil, fault)
					}
					if fault != "admitted" && (l.raidEntry != nil || p.raidEntry != nil) {
						t.Fatal("rejected preflight froze a participant")
					}
				} else {
					for _, target := range []*runtimePartyPreparedDungeonEntry{{session: leader, runtime: l}, {session: peer, runtime: p}} {
						err := f.service.validateCurrentRaidDungeonEntry(target.session, target.runtime)
						if (err == nil) != (fault == "admitted") {
							t.Fatalf("commit owner=%d admitted=%v fault=%s", target.session.selectedCharacterID, err == nil, fault)
						}
					}
				}
			})
		}
	}
}

func TestRaidEntryGuardPreservesOrdinaryUnboundDungeon(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.channel.ID, session.channel.Type = 11, 0
	runtime := &runtimeDungeonState{}
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, runtime, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.validateCurrentRaidDungeonEntry(session, runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.raidEntry != nil {
		t.Fatal("ordinary dungeon acquired raid binding")
	}
}
