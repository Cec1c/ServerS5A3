package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Five owned characters clear the real final-phase volcano/heart program in
// two legal parties. One qualified member leaves; the other party's member
// takes that live seat. Combining the four live seats with the departed
// owner's old seat counts five, although each actual snapshot is valid.
func frozenAntonRegroupFixture(t *testing.T) (*raidStartGuardFixture, map[uint16]*gameSession) {
	t.Helper()
	catalog, rewards := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 5)
	warmAntonRewardQuestFixture(t, f)
	f.service.options.currentClientVersion = "official-24.9"
	sessions := map[uint16]*gameSession{}
	accounts := map[uint16]string{}
	groups := map[uint16]byte{}
	for i := range f.state.Members {
		id := uint16(19 + i)
		p := f.session()
		p.conn = &bufferConn{}
		p.selectedCharacterID = id
		if id != 19 {
			p.accountID = "account-" + strconv.Itoa(int(id))
		}
		p.loginPacketSeen, p.accountFromToken = true, true
		f.service.bindGameSessionCharacter(p, id)
		sessions[id], accounts[id] = p, p.accountID
		group := byte(1)
		if id == 23 {
			group = 2
		}
		groups[id] = group
		f.state.Members[i].GroupIndex = group
		f.state.Members[i].SlotOrder = byte(i % 4)
	}
	base := time.Now().Add(-2 * time.Minute)
	progress, err := program.BeginPhase(1, base, 91321)
	if err != nil {
		t.Fatal(err)
	}
	b := &runtimeRaidBattle{InstanceID: "102132435465768798a9bacbdcedfe0f", Mode: 1, Phase: 1,
		ChannelID: sessions[19].channel.ID, ChannelType: sessions[19].channel.Type,
		StartedAt: base, EnterAfter: base, Deadline: base.Add(time.Duration(config.PhaseLimitSeconds[1]) * time.Second),
		Configuration: config, Anton: progress, AntonRewards: &rewards, Dungeons: progress.Dungeons(),
		MemberAccounts: accounts, MemberGroups: groups}
	f.state.Started, f.state.Battle = true, b
	clear := func(n int, dungeon uint32, ids ...uint16) {
		leader := sessions[ids[0]]
		runtime := raidCompletedRuntimeFixture(t, leader, dungeon)
		var peers []*runtimePartyPreparedDungeonEntry
		for _, id := range ids[1:] {
			peers = append(peers, &runtimePartyPreparedDungeonEntry{session: sessions[id], runtime: raidCompletedRuntimeFixture(t, sessions[id], dungeon)})
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, runtime, peers); err != nil {
			t.Fatal(n, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, runtime, base.Add(time.Duration(n)*time.Second)); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
	}
	clear(1, 219, 19, 20, 21, 22)
	clear(2, 220, 23)
	clear(3, 220, 23)
	if _, removed, _ := f.service.removeRuntimeRaidMember(22); !removed {
		t.Fatal("qualified departure failed")
	}
	if _, moved, _ := f.service.moveRuntimeRaidMember(19, 23, 1); !moved {
		t.Fatal("legal regroup failed")
	}
	clear(4, 220, 19, 20, 21, 23)
	clear(5, 220, 19, 20, 21, 23)
	clear(6, 220, 19, 20, 21, 23)
	if !b.PhaseCompleted || runtimeRaidHasOversizedGroup(*f.state) || len(f.state.Members) != 4 {
		t.Fatal("invalid completed-regroup fixture")
	}
	flowConfig, err := raid.LoadAntonFlowConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	b.AntonFlowConfig = flowConfig
	return f, sessions
}

func TestRealPVFAntonFrozenRewardGroupsSurviveQualifiedDepartureAndRegroup(t *testing.T) {
	f, _ := frozenAntonRegroupFixture(t)
	want := map[uint16]byte{19: 1, 20: 1, 21: 1, 22: 1, 23: 2}
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal("legal snapshots prevented reward flow:", err)
	}
	flow := f.state.Battle.RewardFlow[1]
	got := map[uint16]byte{}
	for _, owner := range flow.Recipients {
		got[owner.CharacterID] = owner.Group
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.state.Battle.MemberGroups, want) {
		t.Fatal("frozen ownership changed", got)
	}
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil || f.state.Battle.RewardFlow[1] != flow {
		t.Fatal("clock retry replaced ownership", err)
	}
}

func TestRealPVFAntonRegroupFinalGrantsBothPoolsAndReplaysWithoutDuplication(t *testing.T) {
	for _, mode := range []string{"unattended", "manual", "personal_committed_before_flow"} {
		t.Run(mode, func(t *testing.T) {
			f, sessions := frozenAntonRegroupFixture(t)
			p := sessions[19]
			if mode == "personal_committed_before_flow" {
				// This owner moved from frozen group2 to live group1. Its
				// already committed manual card must not consume a fifth
				// slot in the other four frozen owners' automatic group.
				p = sessions[23]
			}
			a02RewardPartyWithSessionGenerations(t, f.service, sessions[19], sessions[20], sessions[21], sessions[23])
			f.service.mu.Lock()
			online := f.service.gameSessions
			f.service.gameSessions = nil
			f.service.mu.Unlock()
			done, errs := f.service.commitCurrentRaidWeeklyIntents(time.Now())
			f.service.mu.Lock()
			f.service.gameSessions = online
			f.service.mu.Unlock()
			if len(done) != 1 || len(errs) != 0 {
				t.Fatal("weekly commit", done, errs)
			}
			if mode == "personal_committed_before_flow" {
				if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				p.conn.(*bufferConn).write.Reset()
				dispatchAntonCardRequest(t, f, p, 659, []byte{1})
				dispatchAntonCardRequest(t, f, p, 660, []byte{1, 0})
			}
			if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
				t.Fatal(err)
			}
			flow := f.state.Battle.RewardFlow[1]
			if mode == "manual" {
				if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				p.conn.(*bufferConn).write.Reset()
				dispatchAntonCardRequest(t, f, p, 659, []byte{1})
				wire := r195ReadFiendRewardWire(t, p.conn.(*bufferConn).write.Bytes())
				if !reflect.DeepEqual(wire.owners[1], []uint16{19, 20, 21, 23}) {
					t.Fatal("personal display is not the real four-person party", wire)
				}
				dispatchAntonCardRequest(t, f, p, 660, []byte{1, 0})
			}
			at := time.Now().Add(time.Minute)
			if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			for _, id := range []uint16{19, 20, 21, 22, 23} {
				key := strconv.Itoa(int(id))
				if !currentRaidOwnerRewardsCommitted(flow, id) || antonGrantedItemCount(t, f, key, 10304529) != 45 || antonGrantedItemCount(t, f, key, 10093974) != 52 {
					t.Fatal("qualified owner lost a pool", id)
				}
			}
			used := map[byte]bool{}
			for _, id := range []uint16{19, 20, 21, 23} {
				card := flow.Party[id].Card
				if card > 3 || used[card] {
					t.Fatal("actual four-person party reused a native card slot", id, card)
				}
				used[card] = true
			}
			if flow.Party[22].Card != 0 {
				t.Fatal("departed owner's independent card lost its first slot")
			}
			// A replacement owned connection restores the actual party and all
			// five committed team rows, including the departed qualified owner.
			replacement := f.session()
			replacement.conn = &bufferConn{}
			replacement.loginPacketSeen, replacement.accountFromToken = true, true
			f.service.bindGameSessionCharacter(replacement, 19)
			if err := f.service.sendCurrentAntonTerminalResult(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			replacement.conn.(*bufferConn).write.Reset()
			dispatchAntonCardRequest(t, f, replacement, 659, []byte{1})
			wire := r195ReadFiendRewardWire(t, replacement.conn.(*bufferConn).write.Bytes())
			if !reflect.DeepEqual(wire.owners[1], []uint16{19, 20, 21, 23}) || !reflect.DeepEqual(wire.owners[3], []uint16{19, 20, 21, 22, 23}) {
				t.Fatal("reconnect lost personal/team display ownership", wire)
			}
			dispatchAntonCardRequest(t, f, replacement, 659, []byte{1})
			dispatchAntonCardRequest(t, f, replacement, 660, []byte{1, 3})
			if _, errs := f.service.advanceCurrentAntonRewardFlows(at.Add(time.Minute), func(int64) (int64, error) { t.Fatal("committed reward rerolled"); return 0, nil }); len(errs) != 0 {
				t.Fatal(errs)
			}
			for _, id := range []uint16{19, 20, 21, 22, 23} {
				key := strconv.Itoa(int(id))
				if antonGrantedItemCount(t, f, key, 10304529) != 45 || antonGrantedItemCount(t, f, key, 10093974) != 52 {
					t.Fatal("duplicate grant", id)
				}
				ch, _, err := f.repos.Character.Load(context.Background(), key)
				policy, _ := currentRaidWeeklyPolicy()
				used, countErr := raid.WeeklyClears(ch, 1, time.Now(), policy)
				if err != nil || countErr != nil || used != 1 {
					t.Fatal("weekly completion duplicated", id, used, err, countErr)
				}
			}
		})
	}
}

func TestRealPVFAntonFrozenRewardGroupsRejectInvalidSnapshot(t *testing.T) {
	for _, mode := range []string{"missing_group", "zero_group", "oversized_frozen_group", "empty_account"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := frozenAntonRegroupFixture(t)
			b := f.state.Battle
			switch mode {
			case "missing_group":
				delete(b.MemberGroups, 22)
			case "zero_group":
				b.MemberGroups[23] = 0
			case "oversized_frozen_group":
				b.MemberGroups[23] = 1
			case "empty_account":
				b.MemberAccounts[23] = ""
			}
			if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err == nil || b.RewardFlow[1] != nil {
				t.Fatal("invalid frozen entitlement admitted", mode, err)
			}
		})
	}
}
