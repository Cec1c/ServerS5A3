package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// Current native keyboard input exits when the active actor has zero HP.
// The death response must therefore hand control to the survivor without
// inventing a subsequent 1465 request that this client cannot send.
func TestTagDeathHandoffSurvivorAndDuplicate(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			s, p, g := tagSecondaryReturnFixture(t, tagtournament.Normal, reverse)
			s.options.channelAdvertiseID = 1
			b := p.tagTournamentBattle
			before := b.run.Snapshot()
			deadRole, liveRole := before.Active, 1-before.Active
			// Death during the ordinary tag cooldown must not strand a corpse.
			instant := b.now()
			if !instant.Before(before.NextTag) {
				t.Fatal("fixture did not enter tag cooldown")
			}
			b.now = func() time.Time { return instant }
			accountBefore, _, _ := g.Character.Load(context.Background(), "29")
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[deadRole]}); err != nil {
				t.Fatal(err)
			}
			after := b.run.Snapshot()
			if after.Active != liveRole || after.Vitals[deadRole].HP != 0 || after.Vitals[liveRole] != before.Vitals[liveRole] || after.Phase != tagtournament.Fighting {
				t.Fatalf("single death did not transfer to survivor: before=%+v after=%+v", before, after)
			}
			seenTag := 0
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				rest = tail
				if packet.Header.MsgID == 1466 {
					t.Fatal("single death armed native defeat countdown", packet)
				}
				if packet.Header.MsgID == 1402 {
					seenTag++
					if !bytes.Equal(packet.Body, []byte{b.nativeRoleIDs[liveRole]}) || packet.Header.Classification != 0 {
						t.Fatal("death handoff ordering/role", packet)
					}
				}
			}
			if seenTag != 1 || b.tagProjectionPending {
				t.Fatal("missing native handoff", seenTag)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[deadRole]}); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 || b.run.Snapshot() != after {
				t.Fatal("duplicate death mutated state or repeated handoff/countdown")
			}
			accountAfter, _, _ := g.Character.Load(context.Background(), "29")
			if !reflect.DeepEqual(accountBefore, accountAfter) {
				t.Fatal("death changed durable actor assets")
			}
			if count, _ := tagBattleMailTotals(t, g); count != 0 {
				t.Fatal("death granted unearned reward")
			}
		})
	}
}

func TestTagDeathHandoffFailedWritesCanRetryWithoutAnotherDeath(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p, _ := tagSecondaryReturnFixture(t, tagtournament.Normal, false)
			b := p.tagTournamentBattle
			dead := b.run.Snapshot().Active
			failure := errors.New("death handoff write fixture")
			p.conn = &failNthDungeonWriteConn{failAt: failAt, err: failure}
			if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[dead]}); !errors.Is(err, failure) {
				t.Fatal("missing failure", err)
			}
			snapshot := b.run.Snapshot()
			if snapshot.Active == dead || snapshot.Vitals[dead].HP != 0 || !b.tagProjectionPending {
				t.Fatal("lost committed handoff", snapshot)
			}
			p.conn = &bufferConn{}
			if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[dead]}); err != nil {
				t.Fatal(err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if counts[1466] != 0 || counts[1402] != 1 || b.tagProjectionPending || b.run.Snapshot() != snapshot {
				t.Fatal("retry did not repair only projection", counts)
			}
		})
	}
}

func TestTagDeathHandoffThenTownAndImmediateReentry(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, allDead := range []bool{false, true} {
			t.Run(fmt.Sprint(reverse, allDead), func(t *testing.T) {
				s, p, g := tagSecondaryReturnFixture(t, tagtournament.Normal, reverse)
				b := p.tagTournamentBattle
				for deaths := 0; deaths < 1+boolInt(allDead); deaths++ {
					role := b.run.Snapshot().Active
					if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[role]}); err != nil {
						t.Fatal(err)
					}
				}
				if allDead && b.run.Snapshot().Phase != tagtournament.Defeated {
					t.Fatal("all dead still fighting")
				}
				before, _, _ := g.Character.Load(context.Background(), "29")
				request := b.prepared.runtime.Request
				p.conn.(*bufferConn).write.Reset()
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
				if counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[24] != 1 || counts[1388] != 0 {
					t.Fatal("premature destructive roster", counts)
				}
				// Direct entry intent, no logout, team setup request or fabricated movement.
				if err := s.confirmCurrentTagTournamentEnterSelectReturn(p); err != nil {
					t.Fatal(err)
				}
				if p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
					t.Fatal("return still owns old run")
				}
				p.conn.(*bufferConn).write.Reset()
				if err := s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil {
					t.Fatal(err)
				}
				next := p.tagTournamentBattle
				if next == nil || next == b || !next.prepared.creationSent {
					t.Fatal("no replacement run")
				}
				assertTagCreationRoster(t, p, next)
				if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
					t.Fatal(err)
				}
				tagPlayMoveNext(t, s, p)
				if next.run.Snapshot().Vitals[0].HP <= 0 || next.run.Snapshot().Vitals[1].HP <= 0 {
					t.Fatal("fresh run kept previous deaths")
				}
				after, _, _ := g.Character.Load(context.Background(), "29")
				if before.Stats["gold"] != after.Stats["gold"] {
					t.Fatal("reentry charged again")
				}
				if n, _ := tagBattleMailTotals(t, g); n != 0 {
					t.Fatal("death/reentry awarded mail")
				}
			})
		}
	}
}

func TestTagDeathHandoffPendingProjectionRetiresOnReturn(t *testing.T) {
	s, p, _ := tagSecondaryReturnFixture(t, tagtournament.Normal, false)
	b := p.tagTournamentBattle
	role := b.run.Snapshot().Active
	failure := errors.New("death notification write fixture")
	p.conn = &failNthDungeonWriteConn{failAt: 2, err: failure}
	if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[role]}); !errors.Is(err, failure) || !b.tagProjectionPending {
		t.Fatal(err)
	}
	p.conn = &bufferConn{}
	if err := s.handleDungeonGiveupGame(p, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.confirmCurrentTagTournamentEnterSelectReturn(p); err != nil {
		t.Fatal(err)
	}
	if b.tagProjectionPending || p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
		t.Fatal("old handoff blocked return")
	}
	if err := s.refreshCurrentTagTournamentLobby(p, b.now()); err != nil {
		t.Fatal(err)
	}
}

func assertTagCreationRoster(t *testing.T, p *gameSession, b *currentTagTournamentBattle) {
	t.Helper()
	var roster []byte
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		rest = tail
		if packet.Header.MsgID == 1388 {
			roster = append([]byte(nil), packet.Body...)
		}
		if packet.Header.MsgID == 1389 {
			if !bytes.Equal(roster, b.prepared.creationRosterBody) || len(roster) != 4 || roster[0] != 0 || roster[2] != 0 {
				t.Fatal("creation without local identity vector", roster)
			}
			if p.currentRenameRoster[roster[1]].CharacterID != "29" || p.currentRenameRoster[roster[3]].CharacterID != "60" {
				t.Fatal("root/secondary identity swapped", roster)
			}
			return
		}
	}
	t.Fatal("no native creation")
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
