package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

// Actual369d phase VM plus existing completed-settlement runtime fixture.
// This is a public op72 model, not an actual213 map entry or native client run.
func TestRealPVFExpiredCompletedRaidMapCanReturnWithoutEarningClear(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual production PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ expired, op42, advanced bool }{{false, false, false}, {true, false, false}, {false, true, false}, {true, true, false}, {true, false, true}, {true, true, true}} {
		expired := tc.expired
		t.Run(fmt.Sprintf("expired=%v/op42=%v/advanced=%v", expired, tc.op42, tc.advanced), func(t *testing.T) {
			s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
			completeSettlementExitBarriersForTest(t, s, r, session)
			s.options.currentClientVersion = "official-24.9"
			repos, ok := s.repositoryGroup()
			if !ok {
				t.Fatal("repos")
			}
			character, found, err := repos.Character.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatal(err, found)
			}
			session.accountID = character.AccountID
			session.loginPacketSeen = true
			session.accountFromToken = true
			session.channel = channelcatalog.Channel{ID: 23, Type: 23}
			s.bindGameSessionCharacter(session, 99)
			r.AccountID = character.AccountID
			r.Dungeon.ID = 213
			now := time.Now()
			anton, err := program.BeginPhase(0, now.Add(-time.Second), 87152)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				next, replay, err := anton.ClearDungeon(fmt.Sprintf("fog-%d", i), 210, anton.Epoch(), now)
				if err != nil || replay {
					t.Fatal(err, replay)
				}
				anton = next
			}
			e := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "expired-completed-owned", RunID: "leg-213", Phase: 0, Attempt: anton.Epoch(), ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
			r.raidEntry = nil
			b := &runtimeRaidBattle{InstanceID: e.InstanceID, Mode: 1, EnterAfter: now.Add(-time.Second), Deadline: now.Add(time.Hour), Anton: anton, Dungeons: anton.Dungeons(), ChannelID: 23, ChannelType: 23, MemberAccounts: map[uint16]string{99: character.AccountID}}
			s.raids = map[uint32]*runtimeRaidState{e.RaidKey: {RaidKey: e.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1}}}}
			if err := s.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
				t.Fatal("real entry binding", err)
			}
			reportAt := now
			if expired {
				reportAt = now.Add(301 * time.Second)
			}
			_, changed, clearErr := s.reduceCurrentRaidDungeonClearLocked(session, r, reportAt)
			if expired {
				if clearErr == nil || !changed || b.Anton.Epoch() != 2 || len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 {
					t.Fatalf("expiry failed: changed=%v err=%v epoch=%d", changed, clearErr, b.Anton.Epoch())
				}
			} else {
				if clearErr != nil || !changed || len(b.ClearedRuns) != 1 {
					t.Fatal(changed, clearErr)
				}
			}
			if expired {
				if !s.currentRaidCompletedMapTownReturnReady(session, r) || s.currentRaidCompletedMapExitReady(session, r) || s.currentCompletedDungeonActionReady(session, r) {
					t.Fatal("return receipt escaped town-only boundary")
				}
				for _, action := range []byte{0, 1} {
					conn.write.Reset()
					if err := s.handleDungeonEplpCommand(session, []byte{1, action}); err != nil {
						t.Fatal(err)
					}
					if conn.write.Len() != 0 || session.dungeon.runtime != r || r.townReturnOp24Sent {
						t.Fatalf("expired run authorized action %d", action)
					}
				}
				receipt := b.ExpiredCompletedRuns[r.raidEntry.RunID]
				if len(receipt.Participants) != 1 {
					t.Fatal("missing immutable owner receipt")
				}
				copy := cloneRuntimeRaidBattle(b)
				copied := copy.ExpiredCompletedRuns[r.raidEntry.RunID]
				copied.Participants[0] = 88
				if receipt.Participants[0] != 99 {
					t.Fatal("receipt clone aliases participants")
				}
				for _, field := range []string{"absent", "stale-redelivery", "dungeon", "phase", "attempt", "participant", "channel", "account", "instance", "runtime"} {
					t.Run("reject-"+field, func(t *testing.T) {
						modified := receipt
						modified.Participants = append([]uint16(nil), receipt.Participants...)
						switch field {
						case "absent":
							delete(b.ExpiredCompletedRuns, r.raidEntry.RunID)
						case "stale-redelivery":
							delete(b.ExpiredCompletedRuns, r.raidEntry.RunID)
							_, _, err := s.reduceCurrentRaidDungeonClearLocked(session, r, reportAt)
							if err == nil || len(b.ExpiredCompletedRuns) != 0 {
								t.Fatal("already stale run minted new receipt")
							}
						case "dungeon":
							modified.DungeonID++
							b.ExpiredCompletedRuns[r.raidEntry.RunID] = modified
						case "phase":
							modified.Phase++
							b.ExpiredCompletedRuns[r.raidEntry.RunID] = modified
						case "attempt":
							modified.Attempt++
							b.ExpiredCompletedRuns[r.raidEntry.RunID] = modified
						case "participant":
							modified.Participants[0]++
							b.ExpiredCompletedRuns[r.raidEntry.RunID] = modified
						case "channel":
							session.channel.ID++
						case "account":
							r.AccountID += "-foreign"
						case "instance":
							b.InstanceID += "-replaced"
						case "runtime":
							session.dungeon.runtime = nil
						}
						ready := s.currentRaidCompletedMapTownReturnReady(session, r)
						b.ExpiredCompletedRuns[r.raidEntry.RunID] = receipt
						session.channel.ID = 23
						r.AccountID = character.AccountID
						b.InstanceID = e.InstanceID
						session.dungeon.runtime = r
						if ready {
							t.Fatal("foreign or absent receipt authorized return")
						}
					})
				}
			}
			if tc.advanced {
				// Explicit in-memory other-squad completion through the actual
				// PVF phase VM. The old expired run earns no bridge clear/reward.
				for n, dungeon := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
					next, replay, err := b.Anton.ClearDungeon(fmt.Sprintf("other-squad-%d", n), dungeon, b.Anton.Epoch(), reportAt.Add(time.Duration(n+1)*time.Second))
					if err != nil || replay {
						t.Fatal("actual phase completion", n, err, replay)
					}
					b.Anton = next
				}
				if !b.Anton.PhaseCleared() {
					t.Fatal("actual first phase not complete")
				}
				next, err := b.Anton.BeginNextPhase(reportAt.Add(10 * time.Second))
				if err != nil {
					t.Fatal(err)
				}
				b.Phase, b.Anton, b.Dungeons = 1, next, next.Dungeons()
				if s.currentRaidCompletedMapExitReady(session, r) {
					t.Fatal("old expired map authorized retry after phase advance")
				}
				// The preserved receipt still rejects another instance/owner and
				// cannot be inferred from the new phase's reset attempt number.
				instance := b.InstanceID
				b.InstanceID += "-foreign"
				if s.currentRaidCompletedMapTownReturnReady(session, r) {
					t.Fatal("cross-instance expired return authorized")
				}
				b.InstanceID = instance
			}
			conn.write.Reset()
			var exitErr error
			if tc.op42 {
				exitErr = s.handleDungeonGiveupGame(session, nil)
			} else {
				exitErr = s.handleDungeonEplpCommand(session, []byte{1, 2})
			}
			if exitErr != nil {
				t.Fatal(exitErr)
			}
			if !r.townReturnOp24Sent || session.dungeon.runtime != nil {
				t.Fatalf("owned completed map cannot return after expiry: expired=%v op24=%v runtime=%p", expired, r.townReturnOp24Sent, session.dungeon.runtime)
			}
			if expired && (len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 || b.PhaseCompleted) {
				t.Fatal("return earned invalid clear")
			}
		})
	}
}
