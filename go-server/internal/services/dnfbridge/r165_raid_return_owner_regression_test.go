package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func prepareR165LukeSpreadSanctumTownReturnFixture(t *testing.T) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, *currentRaidDungeonEntry, *runtimeRaidBattle) {
	t.Helper()
	s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	s.options.currentClientVersion = "official-24.9"
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repositories unavailable")
	}
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	session.accountID = character.AccountID
	session.loginPacketSeen, session.accountFromToken = true, true
	session.channel = channelcatalog.Channel{ID: 32, Type: 32}
	s.bindGameSessionCharacter(session, 99)
	r.Dungeon.ID = currentLukeSpreadSanctumDungeonID
	r.AccountID = character.AccountID
	e := &currentRaidDungeonEntry{RaidKey: 32<<16 | 99, InstanceID: "luke-spread-owned-map", RunID: "luke-spread-run", Phase: 0, Attempt: 1, ChannelID: 32, ChannelType: 32, Participants: []uint16{99}, teamMode: true}
	b := &runtimeRaidBattle{InstanceID: e.InstanceID, Mode: byte(currentRaidDailyLukeMode), Phase: 0, ChannelID: 32, ChannelType: 32, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(currentLukeSpreadSanctumDungeonID), Phase: 0, Attempt: 1, Participants: []uint16{99}}}}
	r.raidEntry = e
	s.raids = map[uint32]*runtimeRaidState{e.RaidKey: {RaidKey: e.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1}}}}
	if r.Session == nil {
		t.Fatal("fixture completed run missing")
	}
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("fixture run is not completed")
	}
	if r.settlementClearRewardSent {
		t.Fatal("fixture unexpectedly sent ordinary clear reward")
	}
	return s, r, session, conn, e, b
}

// Live R165 evidence: a completed Luke SPREAD SANCTUM (5056) result page sent
// op72 after reconnect and was rejected with settlement_clear_reward_not_sent.
// The accepted raid clear journal owns this one town route; ordinary op34/35
// presentation is not required. Anton and every other raid map stay on the
// shared gate below.
func TestR165LukeSpreadSanctumCompletedMapTownReturnUsesRaidClearJournal(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mutate       func(*currentRaidDungeonEntry, *runtimeRaidBattle, *runtimeDungeonState)
		wantDetached bool
	}{
		{"accepted_missing_ordinary_reward", func(*currentRaidDungeonEntry, *runtimeRaidBattle, *runtimeDungeonState) {}, true},
		{"other_luke_sanctum", func(_ *currentRaidDungeonEntry, _ *runtimeRaidBattle, r *runtimeDungeonState) { r.Dungeon.ID = 5054 }, false},
		{"wrong_raid_mode", func(_ *currentRaidDungeonEntry, b *runtimeRaidBattle, _ *runtimeDungeonState) {
			b.Mode = byte(currentRaidDailyAntonMode)
		}, false},
		{"wrong_phase", func(e *currentRaidDungeonEntry, _ *runtimeRaidBattle, _ *runtimeDungeonState) { e.Phase = 1 }, false},
		{"wrong_attempt", func(e *currentRaidDungeonEntry, _ *runtimeRaidBattle, _ *runtimeDungeonState) { e.Attempt = 2 }, false},
		{"foreign_participant", func(e *currentRaidDungeonEntry, _ *runtimeRaidBattle, _ *runtimeDungeonState) {
			e.Participants = []uint16{100}
		}, false},
		{"missing_clear", func(e *currentRaidDungeonEntry, b *runtimeRaidBattle, _ *runtimeDungeonState) {
			delete(b.ClearedRuns, e.RunID)
		}, false},
		{"single_raid_flag", func(e *currentRaidDungeonEntry, _ *runtimeRaidBattle, _ *runtimeDungeonState) { e.teamMode = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, session, conn, e, b := prepareR165LukeSpreadSanctumTownReturnFixture(t)
			tc.mutate(e, b, r)
			conn.write.Reset()
			if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			detached := session.dungeon.runtime == nil
			if detached != tc.wantDetached {
				t.Fatalf("detached=%t want=%t ack=%t op24=%t bytes=%x", detached, tc.wantDetached, r.settlementCardExitAckSent, r.townReturnOp24Sent, conn.write.Bytes())
			}
			if tc.wantDetached {
				if !r.townReturnOp24Sent {
					t.Fatal("accepted spread-sanctum town return emitted no op24")
				}
				if r.settlementClearRewardSent {
					t.Fatal("accepted town return manufactured an ordinary op35 reward")
				}
				return
			}
			if session.dungeon.runtime != r || r.settlementCardExitAckSent || r.townReturnOp24Sent || conn.write.Len() != 0 {
				t.Fatalf("rejected spread-sanctum action consumed exit gate ack=%t op24=%t bytes=%x", r.settlementCardExitAckSent, r.townReturnOp24Sent, conn.write.Bytes())
			}
		})
	}
}

// Only the result page's 返回城镇 action crosses this narrow gate. Retry and
// selector remain behind the ordinary reward/card boundary.
func TestR165LukeSpreadSanctumRetryAndSelectorStayBehindOrdinaryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"retry", []byte{1, 0}},
		{"other_selector", []byte{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, session, conn, _, _ := prepareR165LukeSpreadSanctumTownReturnFixture(t)
			conn.write.Reset()
			if err := s.handleDungeonEplpCommand(session, tc.body); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != r || r.settlementCardExitAckSent || r.townReturnOp24Sent || conn.write.Len() != 0 {
				t.Fatalf("spread-sanctum %s crossed the ordinary boundary ack=%t op24=%t bytes=%x", tc.name, r.settlementCardExitAckSent, r.townReturnOp24Sent, conn.write.Bytes())
			}
		})
	}
}

// The narrow op72 branch must not change Anton's accepted return behavior.
// Anton completes without the new Luke clear-journal shortcut.
func TestR165AntonCompletedMapStillRequiresOrdinarySettlement(t *testing.T) {
	s, r, session, conn, e, b := prepareR165LukeSpreadSanctumTownReturnFixture(t)
	r.Dungeon.ID = 213
	b.Mode = byte(currentRaidDailyAntonMode)
	b.ClearedRuns[e.RunID] = currentRaidClearedRun{DungeonID: 213, Phase: 0, Attempt: e.Attempt, Participants: []uint16{99}}
	conn.write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != r || r.settlementCardExitAckSent || r.townReturnOp24Sent || conn.write.Len() != 0 {
		t.Fatalf("Anton crossed the ordinary settlement boundary ack=%t op24=%t bytes=%x", r.settlementCardExitAckSent, r.townReturnOp24Sent, conn.write.Bytes())
	}
}

// Keep the shared town-return receipt strict. Only the new one-map helper may
// ignore the ordinary presentation chain for the completed 5056 result page.
func TestR165LukeSpreadSanctumSharedTownReturnGateStaysStrict(t *testing.T) {
	s, r, session, _, _, _ := prepareR165LukeSpreadSanctumTownReturnFixture(t)
	if s.currentRaidCompletedMapTownReturnReady(session, r) {
		t.Fatal("shared raid town-return gate was widened to ignore ordinary settlement")
	}
	if !s.currentLukeSpreadSanctumCompletedMapTownReturnReady(session, r) {
		t.Fatal("narrow spread-sanctum owner did not accept the accepted clear journal")
	}
}

// The menu path already worked in the live capture. Preserve it as a control:
// ordinary maps still require the committed clear reward.
func TestR165OrdinaryCompletedMapStillRequiresClearReward(t *testing.T) {
	s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	s.options.currentClientVersion = "official-24.9"
	if r.settlementClearRewardSent {
		t.Fatal("fixture unexpectedly sent ordinary clear reward")
	}
	conn.write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != r || r.settlementCardExitAckSent || len(conn.write.Bytes()) != 0 {
		t.Fatal("ordinary map crossed the unsent clear-reward gate")
	}
}
