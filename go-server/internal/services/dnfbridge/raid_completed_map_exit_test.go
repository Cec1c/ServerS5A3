package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestCompletedRaidMapActualExitDoesNotWaitForOrdinaryCards(t *testing.T) {
	for _, op := range []uint16{72, 42} {
		for _, fault := range []string{"ready", "phase_completed", "phase_advanced", "missing_clear", "wrong_phase", "wrong_attempt", "wrong_instance", "wrong_channel", "wrong_account", "foreign_participant", "ordinary"} {
			t.Run(fmt.Sprintf("op%d/%s", op, fault), func(t *testing.T) {
				s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
				completeSettlementExitBarriersForTest(t, s, r, session)
				s.options.currentClientVersion = "official-24.9"
				repos, ok := s.repositoryGroup()
				if !ok {
					t.Fatal("repos missing")
				}
				character, found, err := repos.Character.Load(context.Background(), "99")
				if err != nil || !found {
					t.Fatal(err, found)
				}
				session.accountID = character.AccountID
				session.loginPacketSeen, session.accountFromToken = true, true
				session.channel = channelcatalog.Channel{ID: 23, Type: 23}
				s.bindGameSessionCharacter(session, 99)
				r.AccountID = character.AccountID
				entry := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "cleared-raid-map", RunID: "owned-run", Phase: 0, Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
				r.raidEntry = entry
				b := &runtimeRaidBattle{InstanceID: entry.InstanceID, Mode: 1, ChannelID: 23, ChannelType: 23, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{entry.RunID: {DungeonID: uint32(r.Dungeon.ID), Phase: entry.Phase, Attempt: entry.Attempt, Participants: []uint16{99}}}}
				s.raids = map[uint32]*runtimeRaidState{entry.RaidKey: {RaidKey: entry.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99}}}}
				switch fault {
				case "phase_completed":
					b.PhaseCompleted = true
				case "phase_advanced":
					b.Phase = 1
				case "wrong_phase":
					entry.Phase = 1
				case "wrong_attempt":
					entry.Attempt = 2
				case "missing_clear":
					delete(b.ClearedRuns, entry.RunID)
				case "wrong_instance":
					b.InstanceID = "other"
				case "wrong_channel":
					session.channel.ID = 24
				case "wrong_account":
					b.MemberAccounts[99] = "other"
				case "foreign_participant":
					entry.Participants = []uint16{100}
				case "ordinary":
					r.raidEntry = nil
					s.raids = nil
					session.channel.Type = 0
				}
				conn.write.Reset()
				if op == 72 {
					err = s.handleDungeonEplpCommand(session, []byte{1, 2})
				} else {
					err = s.handleDungeonGiveupGame(session, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if fault == "ready" || fault == "phase_completed" || fault == "phase_advanced" {
					if !r.townReturnOp24Sent || session.dungeon.runtime != nil {
						t.Fatalf("raid exit still waiting for ordinary cards: op24=%v runtime=%p", r.townReturnOp24Sent, session.dungeon.runtime)
					}
				} else if r.townReturnOp24Sent {
					t.Fatal("foreign/uncommitted/ordinary exit bypassed card gate")
				}
				if r.settlementCardLayoutSent || r.settlementCardSelectionSent || r.settlementCardRewardCommitted {
					t.Fatal("raid return fabricated ordinary card state")
				}
			})
		}
	}
}
