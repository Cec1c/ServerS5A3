package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRaidDungeonBindingGroupAtomicityAndStaleCommit(t *testing.T) {
	for _, name := range []string{"ready", "foreign_account", "foreign_group", "closed_map", "expired", "missing_binding", "replaced_instance", "next_phase", "replaced_login", "single_followers", "duplicate_member", "changed_channel"} {
		t.Run(name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 2)
			f.service.options.currentClientVersion = "official-24.9"
			leader := f.session()
			leader.loginPacketSeen, leader.accountFromToken = true, true
			peer := f.session()
			peer.selectedCharacterID, peer.accountID = 20, "account-20"
			peer.loginPacketSeen, peer.accountFromToken = true, true
			f.service.bindGameSessionCharacter(leader, 19)
			f.service.bindGameSessionCharacter(peer, 20)
			for i := range f.state.Members {
				f.state.Members[i].GroupIndex = 1
			}
			f.state.Started = true
			f.state.Battle = &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, EnterAfter: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Hour),
				ChannelID: leader.channel.ID, ChannelType: leader.channel.Type,
				Dungeons: []raid.DungeonState{{ID: 210, State: 0}}, MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}}
			runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 210}, AccountID: "account-1", Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
			follower := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 210}, AccountID: "account-20", Character: dnfrepo.CharacterRecord{CharacterID: "20"}}
			followers := []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: follower}}
			badPreflight := false
			switch name {
			case "foreign_account":
				follower.AccountID = "someone-else"
				badPreflight = true
			case "foreign_group":
				f.state.Members[1].GroupIndex = 2
				badPreflight = true
			case "closed_map":
				f.state.Battle.Dungeons[0].State = 1
				badPreflight = true
			case "expired":
				f.state.Battle.Deadline = time.Now().Add(-time.Second)
				badPreflight = true
			case "single_followers":
				f.state.Battle.Single = true
				badPreflight = true
			case "duplicate_member":
				followers = append(followers, followers[0])
				badPreflight = true
			}
			err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, runtime, followers)
			if badPreflight {
				if err == nil || runtime.raidEntry != nil || follower.raidEntry != nil {
					t.Fatal("invalid preflight partially froze entry", err)
				}
				return
			}
			if err != nil || runtime.raidEntry == nil || follower.raidEntry == nil || len(runtime.raidEntry.RunID) != 32 || runtime.raidEntry.RunID != follower.raidEntry.RunID {
				t.Fatal("group entry did not freeze common run", err)
			}
			if err := f.service.validateCurrentRaidDungeonEntry(peer, follower); err != nil {
				t.Fatal("follower entry invalid", err)
			}
			if name == "ready" {
				original := follower.raidEntry.RunID
				follower.raidEntry.RunID = "ffffffffffffffffffffffffffffffff"
				if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, runtime, followers); err == nil ||
					runtime.raidEntry.RunID != original || follower.raidEntry.RunID != "ffffffffffffffffffffffffffffffff" {
					t.Fatal("mixed prepared runs silently rebound", err)
				}
				follower.raidEntry.RunID = original
			}
			copy := cloneRuntimeRaidBattle(f.state.Battle)
			copy.MemberAccounts[19] = "modified"
			if f.state.Battle.MemberAccounts[19] != "account-1" {
				t.Fatal("snapshot changed frozen owner")
			}
			switch name {
			case "missing_binding":
				runtime.raidEntry = nil
			case "replaced_instance":
				f.state.Battle.InstanceID = "ffffffffffffffffffffffffffffffff"
			case "next_phase":
				f.state.Battle.Phase = 1
			case "changed_channel":
				leader.channel.ID = 24
			case "replaced_login":
				replacement := f.session()
				replacement.loginPacketSeen, replacement.accountFromToken = true, true
				f.service.bindGameSessionCharacter(replacement, 19)
			}
			err = f.service.validateCurrentRaidDungeonEntry(leader, runtime)
			if (err == nil) != (name == "ready") {
				t.Fatal("commit binding guard", name, err)
			}
		})
	}
}
