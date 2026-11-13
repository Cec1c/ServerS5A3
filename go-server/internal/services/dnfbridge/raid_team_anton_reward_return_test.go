package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func teamAntonRewardReturnFixture(t *testing.T, phase byte) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	var f *raidStartGuardFixture
	var leader *gameSession
	if phase == 0 {
		f, leader, _ = firstAntonFlowFixture(t)
	} else {
		var now time.Time
		f, _, now = actualFinalAntonWeeklyFixture(t)
		leader, _ = f.service.onlineGameSession(19)
		warmAntonRewardQuestFixture(t, f)
		// This fixture drives recipient callbacks explicitly. Do not race it
		// with automatic subscribers queued by the genuine weekly commit.
		f.service.mu.Lock()
		online := f.service.gameSessions
		f.service.gameSessions = nil
		f.service.mu.Unlock()
		done, errs := f.service.commitCurrentRaidWeeklyIntents(now)
		f.service.mu.Lock()
		f.service.gameSessions = online
		f.service.mu.Unlock()
		if len(done) != 1 || len(errs) != 0 {
			t.Fatal(done, errs)
		}
	}
	peer, found := f.service.onlineGameSession(20)
	if leader == nil || !found {
		t.Fatal("admitted sessions missing")
	}
	sessions := []*gameSession{leader, peer}
	r195RewardParty(t, f.service, 19, 20)
	for _, p := range sessions {
		p.conn = &bufferConn{}
		if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
	}
	f.conn = leader.conn.(*bufferConn)
	b := f.state.Battle
	if b.RewardFlow[phase] == nil {
		cfg, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
		if err != nil {
			t.Fatal(err)
		}
		b.AntonFlowConfig = cfg
		if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
			t.Fatal(err)
		}
	}
	b.Participation = map[uint16]currentRaidParticipation{}
	for _, p := range sessions {
		var r *runtimeDungeonState
		for runID, run := range b.ClearedRuns {
			if run.Phase != phase {
				continue
			}
			for _, id := range run.Participants {
				if id != p.selectedCharacterID {
					continue
				}
				r = raidCompletedRuntimeFixture(t, p, run.DungeonID)
				r.raidEntry = &currentRaidDungeonEntry{RaidKey: f.state.RaidKey, InstanceID: b.InstanceID,
					Phase: phase, Attempt: run.Attempt, RunID: runID, Participants: append([]uint16(nil), run.Participants...),
					ChannelID: p.channel.ID, ChannelType: p.channel.Type, teamMode: true}
				break
			}
			if r != nil {
				break
			}
		}
		if r == nil {
			t.Fatal("accepted run missing")
		}
		e := r.raidEntry
		b.Participation[p.selectedCharacterID] = currentRaidParticipation{RunID: e.RunID, DungeonID: uint32(r.Dungeon.ID), Phase: e.Phase,
			Attempt: e.Attempt, Generation: p.characterGeneration, Runtime: r, Session: p}
		ch, found, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil || !found {
			t.Fatal(err, found)
		}
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		for k, v := range map[string]int64{"town_id": 19, "area_id": 1, "pos_x": 819, "pos_y": 172, "direction": 0, "area_state": 0} {
			ch.Stats[k] = v
		}
		if err := f.repos.Character.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
		r.Character = ch
		r.townReturnOrigin = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 19, AreaID: 1, PositionX: 819, PositionY: 172, PositionValid: true}
	}
	var err error
	f.service.townCatalog, err = dnftown.Load(context.Background(), f.service.initialEquipmentArchive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	flow := b.RewardFlow[phase]
	for _, at := range []time.Time{flow.AutoSelectAt, flow.AutoSelectAt.Add(20 * time.Second), flow.AutoSelectAt.Add(40 * time.Second)} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	for _, p := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		elapseSingleAntonDisplayForTest(p)
		p.raidProjection.rewardSquadDisplayShownAt = time.Now().Add(-time.Minute)
		p.conn.(*bufferConn).write.Reset()
	}
	return f, sessions
}

func TestRealPVFTeamAntonFinishedCardsReturnBothMembersOnce(t *testing.T) {
	for _, phase := range []byte{0, 1} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			f, sessions := teamAntonRewardReturnFixture(t, phase)
			before := teamAntonReturnAssetCounts(t, f)
			for _, p := range sessions {
				r := p.dungeon.runtime
				conn := p.conn.(*bufferConn)
				// A late successful display still owns its full native window.
				shownAt := p.raidProjection.rewardSquadDisplayShownAt
				p.raidProjection.rewardSquadDisplayShownAt = time.Now()
				if err := f.service.sendCurrentAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil || p.dungeon.runtime != r || r.townReturnOp24Sent || p.raidProjection.singleAntonReturnTimer == nil {
					t.Fatal("late display returned early", err)
				}
				p.raidProjection.rewardSquadDisplayShownAt = shownAt
				// The former single-only dispatcher is a negative control for this bug.
				if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil || p.dungeon.runtime != r || r.townReturnOp24Sent {
					t.Fatal("single wrapper accepted team", err)
				}
				if err := f.service.sendCurrentAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				counts := map[uint16]int{}
				for _, packet := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
					counts[packet.Header.MsgID]++
				}
				if counts[24] != 1 || !r.townReturnOp24Sent || p.dungeon.runtime != nil {
					t.Fatal("team participant stranded", p.selectedCharacterID, counts)
				}
				for _, op := range []uint16{34, 35, 115, 587, 600} {
					if counts[op] != 0 {
						t.Fatal("return replayed result/cards", op)
					}
				}
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				for _, packet := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
					for _, op := range []uint16{24, 34, 35, 115, 587, 600} {
						if packet.Header.MsgID == op {
							t.Fatal("return replay", op)
						}
					}
				}
			}
			if !reflect.DeepEqual(before, teamAntonReturnAssetCounts(t, f)) {
				t.Fatal("return changed rewards")
			}
		})
	}
}

func TestRealPVFTeamAntonRewardReturnRejectsStaleAndUncommittedOwners(t *testing.T) {
	for _, fault := range []string{"weekly", "party", "squad", "display", "instance", "phase", "account", "run", "generation", "runtime", "luke"} {
		t.Run(fault, func(t *testing.T) {
			f, sessions := teamAntonRewardReturnFixture(t, 1)
			p := sessions[0]
			r := p.dungeon.runtime
			b := f.state.Battle
			flow := b.RewardFlow[1]
			captured := cloneRuntimeRaidState(*f.state)
			switch fault {
			case "weekly":
				f.service.raidWeeklyIntents[b.InstanceID].Committed = false
			case "party":
				flow.Party[p.selectedCharacterID].Committed = false
				captured = cloneRuntimeRaidState(*f.state)
			case "squad":
				flow.Squad[p.selectedCharacterID].Committed = false
				captured = cloneRuntimeRaidState(*f.state)
			case "display":
				p.raidProjection.rewardSquadDisplaySignature = [32]byte{}
			case "instance":
				b.InstanceID = "replaced"
			case "phase":
				b.Phase = 0
			case "account":
				b.MemberAccounts[p.selectedCharacterID] = "other"
			case "run":
				r.raidEntry.RunID = "other"
			case "generation":
				p.characterGeneration++
			case "runtime":
				delete(b.Participation, p.selectedCharacterID)
			case "luke":
				b.Mode = 2
				captured = cloneRuntimeRaidState(*f.state)
			}
			if err := f.service.sendCurrentAntonRewardReturn(p, captured); err != nil {
				t.Fatal(err)
			}
			if p.dungeon.runtime != r || r.townReturnOp24Sent || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("unowned/incomplete team return", fault)
			}
		})
	}
}

func teamAntonReturnAssetCounts(t *testing.T, f *raidStartGuardFixture) map[uint16]map[uint32]int64 {
	t.Helper()
	out := map[uint16]map[uint32]int64{}
	for _, id := range []uint16{19, 20} {
		out[id] = map[uint32]int64{}
		inv, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
		if err != nil || !found {
			t.Fatal(err, found)
		}
		for _, item := range inv.Slots {
			out[id][uint32(item.ItemID)] += int64(item.Count)
		}
	}
	return out
}
