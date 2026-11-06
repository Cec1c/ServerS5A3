package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// Original objective + two actually admitted recipients: this regression is
// independent of private quest replacements and uses only memory repositories.
func TestRealPVFNormalAntonFinishedRewardDoesNotReplayBossDeath(t *testing.T) {
	for _, phase := range []byte{0, 1} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			f, sessions := r195MovieStageFixture(t, fmt.Sprintf("anton_phase%d", phase))
			f.service.questCatalog = antonPrerequisiteOverrideCatalog(t, "[type]\n`[raid phase clear]`\n[sub type]\n0\n[int data]\n0 0 5 -1 1 0 5 -1\n[/int data]\n")
			b := f.state.Battle
			for _, p := range sessions {
				id := strconv.Itoa(int(p.selectedCharacterID))
				seedOriginalAntonPrerequisite(t, f, id, b.PhaseCompletedAt)
				// The fixture already projected a result before the original quest was
				// installed. Reset its isolated projection owner to exercise
				// that original quest hook and retain all admitted-run data.
				p.raidProjection = currentRaidProjectionOwner{}
				if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal("original objective blocked result", err)
				}
				if !p.raidProjection.resultSent {
					t.Fatal("result not delivered")
				}
				q, _, err := f.repos.Quest.Load(context.Background(), id)
				want := int64(4 | 5<<9)
				if phase == 1 {
					want = 5 | 4<<9
				}
				if err != nil || q.States[12830].ProgressValue != want {
					t.Fatal("original counter", q, err)
				}
				// Restore a real accepted run's binding for this recipient, as it remains
				// attached until native town return. Do not use a foreign/ordinary map.
				found := false
				for runID, run := range b.ClearedRuns {
					if run.Phase != phase {
						continue
					}
					for _, participant := range run.Participants {
						if participant != p.selectedCharacterID {
							continue
						}
						r := raidCompletedRuntimeFixture(t, p, run.DungeonID)
						r.raidEntry = &currentRaidDungeonEntry{RaidKey: f.state.RaidKey, InstanceID: b.InstanceID,
							Phase: phase, Attempt: run.Attempt, RunID: runID, Participants: append([]uint16(nil), run.Participants...),
							ChannelID: p.channel.ID, ChannelType: p.channel.Type, teamMode: true}
						found = true
						break
					}
					if found {
						break
					}
				}
				if !found {
					t.Fatal("no accepted run for recipient")
				}
			}
			flow := b.RewardFlow[phase]
			for _, at := range []time.Time{flow.AutoSelectAt, flow.AutoSelectAt.Add(20 * time.Second), flow.AutoSelectAt.Add(40 * time.Second)} {
				if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
					t.Fatal(errs)
				}
			}
			if flow.FinishedAt.IsZero() {
				t.Fatal("reward display did not finish")
			}
			for _, p := range sessions {
				id := strconv.Itoa(int(p.selectedCharacterID))
				before, _, err := f.repos.Inventory.Load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				conn := p.conn.(*bufferConn)
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				sawCards := false
				for _, packet := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
					if packet.Header.MsgID == 115 {
						t.Errorf("normal phase%d recipient%d replayed boss-death packet after rewards: %x", phase, p.selectedCharacterID, packet.Body)
					}
					if packet.Header.MsgID == 600 {
						sawCards = true
					}
				}
				if !sawCards {
					t.Fatal("committed cards were lost")
				}
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil || conn.write.Len() != 0 {
					t.Fatal("reward projection replay", err)
				}
				after, _, err := f.repos.Inventory.Load(context.Background(), id)
				if err != nil || len(before.Slots) != len(after.Slots) {
					t.Fatal("projection changed asset slots", err)
				}
				for slot, item := range before.Slots {
					got := after.Slots[slot]
					if item.ItemID != got.ItemID || item.Count != got.Count || item.Bind != got.Bind {
						t.Fatal("projection changed earned asset", slot, item.ItemID, item.Count, got.ItemID, got.Count)
					}
				}
			}
		})
	}
}
