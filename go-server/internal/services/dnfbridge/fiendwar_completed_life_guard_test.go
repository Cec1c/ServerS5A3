package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func fiendWarCompletedLifeGuardFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *runtimeRaidBattle) {
	t.Helper()
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
	b := &runtimeRaidBattle{Mode: 3, InstanceID: e.InstanceID, ChannelID: 244, ChannelType: 42, MemberAccounts: map[uint16]string{99: r.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Phase: 0, Attempt: 1, Participants: []uint16{99}}}, MemberLives: map[currentRaidMemberRun]currentRaidMemberLife{{RunID: e.RunID, CharacterID: 99}: {Phase: 0, Attempt: 1, Dead: true, Deaths: 2}}}
	b.PhaseDeaths[0] = 2
	s.raids = map[uint32]*runtimeRaidState{1: {RaidKey: 1, Started: true, Members: []runtimeRaidMemberState{{CharID: 99}}, Battle: b}}
	r.deathStateSent = true
	p.conn.(*bufferConn).write.Reset()
	return s, p, r, b
}

func TestFiendWarClearLifeProjectionClosesPeerHUD(t *testing.T) {
	s, p, r, _ := fiendWarCompletedLifeGuardFixture(t)
	peer := &gameSession{selectedCharacterID: 100, conn: &bufferConn{}}
	state := alignedcmd.PartyState{PartyID: 7, UserID: 99, Members: []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}}}
	storeRuntimePartyState(p, state)
	state.UserID = 100
	storeRuntimePartyState(peer, state)
	r.partyInstance = &dungeonPartyInstance{id: 7, members: map[uint16]time.Time{99: {}, 100: {}}, sessions: map[uint16]*gameSession{99: p, 100: peer}, deadMembers: map[uint16]bool{99: true}}
	if err := s.restoreCurrentFiendWarCompletedLifeLocked(p, r, false); err != nil {
		t.Fatal(err)
	}
	s.flushCurrentFiendWarClearLife(p)
	frames := splitUpperFrames(t, peer.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || frames[0].msgID != 32 || !reflect.DeepEqual(frames[0].body, []byte{99, 0, 1, 0}) || frames[1].msgID != 153 || frames[1].body[3] != 100 || r.partyInstance.deadMembers[99] {
		t.Fatal("completed peer stayed dead", frames)
	}
	s.flushCurrentFiendWarClearLife(p)
	if len(splitUpperFrames(t, peer.conn.(*bufferConn).write.Bytes())) != 2 {
		t.Fatal("owner replay repeated peer restore")
	}
}

func TestFiendWarCompletedLifeRejectsStaleOrUncommittedOwners(t *testing.T) {
	for _, boundary := range []string{"owner", "runtime", "token", "channel", "account", "instance", "phase", "attempt", "participants", "journal", "return", "timeout", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			s, p, r, b := fiendWarCompletedLifeGuardFixture(t)
			if !s.currentFiendWarCompletedLifeOwner(p, r) {
				t.Fatal("fixture has no committed owner")
			}
			switch boundary {
			case "owner":
				p.selectedCharacterID++
			case "runtime":
				p.dungeon.runtime = nil
			case "token":
				p.dungeon.runToken++
			case "channel":
				p.channel.ID++
			case "account":
				b.MemberAccounts[99] = "another-account"
			case "instance":
				b.InstanceID += "new"
			case "phase":
				b.Phase++
			case "attempt":
				r.raidEntry.Attempt++
			case "participants":
				r.raidEntry.Participants = []uint16{100}
			case "journal":
				delete(b.ClearedRuns, r.raidEntry.RunID)
			case "return":
				r.townReturnPending = true
			case "timeout":
				b.TimedOut = true
			case "completion":
				r.bossDieCheckResponseSent = false
			}
			if err := s.restoreCurrentFiendWarCompletedLifeLocked(p, r, true); err != nil {
				t.Fatal(err)
			}
			if !r.deathStateSent || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("stale owner restored native life")
			}
		})
	}
}

func TestFiendWarCompletedLifeKeepsDeathJournalAndDoesNotRepeat(t *testing.T) {
	s, p, r, b := fiendWarCompletedLifeGuardFixture(t)
	lives := make(map[currentRaidMemberRun]currentRaidMemberLife)
	for key, life := range b.MemberLives {
		lives[key] = life
	}
	deaths := b.PhaseDeaths
	if err := s.restoreCurrentFiendWarCompletedLifeLocked(p, r, false); err != nil {
		t.Fatal(err)
	}
	writes := p.conn.(*bufferConn).write.Len()
	if writes == 0 || r.deathStateSent {
		t.Fatal("first restoration missing")
	}
	if err := s.restoreCurrentFiendWarCompletedLifeLocked(p, r, false); err != nil {
		t.Fatal(err)
	}
	if p.conn.(*bufferConn).write.Len() != writes || !reflect.DeepEqual(lives, b.MemberLives) || deaths != b.PhaseDeaths {
		t.Fatal("replay or death accounting changed")
	}
}
