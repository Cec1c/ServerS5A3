package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestFiendWarCompletedLifeClosesWaitingAndLateDeathWithoutCoin(t *testing.T) {
	for _, when := range []string{"accepted_before_clear", "reported_after_clear", "reported_after_return"} {
		t.Run(when, func(t *testing.T) {
			s, p, r := newBackToVillageRuntime(t)
			authenticateRound100RetrySession(s, p)
			p.channel = channelcatalog.Channel{ID: 244, Type: 42}
			r.lifecycleToken, p.dungeon.runToken = 17, 17
			r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.tutorialCompletionPersisted, r.settlementEntrySent = true, true, true, true
			if err := r.Session.CompleteCurrentRoom(); err != nil {
				t.Fatal(err)
			}
			e := &currentRaidDungeonEntry{RaidKey: 1, InstanceID: "life-fiend", RunID: "cleared-run", Phase: 0, Attempt: 1, ChannelID: 244, ChannelType: 42, Participants: []uint16{99}}
			r.raidEntry = e
			s.raids = map[uint32]*runtimeRaidState{1: {RaidKey: 1, Started: true, Members: []runtimeRaidMemberState{{CharID: 99}}, Battle: &runtimeRaidBattle{Mode: 3, InstanceID: e.InstanceID, ChannelID: 244, ChannelType: 42, MemberAccounts: map[uint16]string{99: r.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Phase: 0, Attempt: 1, Participants: []uint16{99}}}}}}
			conn := p.conn.(*bufferConn)
			conn.write.Reset()
			g, _ := s.repositoryGroup()
			before, _, err := g.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if when == "accepted_before_clear" {
				r.deathStateSent, r.deathReturnWaiting = true, true
				if err := s.sendCurrentDungeonSettlementEntryLocked(p, r, "fiend_completed_life_test"); err != nil {
					t.Fatal(err)
				}
			} else {
				if when == "reported_after_return" {
					r.townReturnPending = true
				}
				if err := s.handleDungeonDieCharacter(p, make([]byte, 4)); err != nil {
					t.Fatal(err)
				}
			}
			if when == "reported_after_return" {
				if conn.write.Len() != 0 {
					t.Fatal("old map death projected after town return")
				}
				return
			}
			if r.deathStateSent || r.deathReturnWaiting || conn.write.Len() == 0 {
				t.Fatal("completed raid left native actor waiting for revive", r.deathStateSent, r.deathReturnWaiting, conn.write.Len())
			}
			packet, _ := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
			if packet.Header.MsgID != 32 || !bytes.Equal(packet.Body, buildCurrentDungeonDeathStateBody(99, 1, 0)) {
				t.Fatal("missing free native clear-life restoration", packet)
			}
			after, _, err := g.Inventory.Load(context.Background(), "99")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("completion spent or awarded an item", err)
			}
		})
	}
}
