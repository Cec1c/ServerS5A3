package dnfbridge

import (
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func r195MovieStageFixture(t *testing.T, phase string) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	var f *raidStartGuardFixture
	var session, peer *gameSession
	switch phase {
	case "luke_phase0":
		f, session, _ = r195LukeCompletedPartyFixture(t)
	case "luke_phase1":
		var first *currentAntonRewardFlow
		f, session, peer, first = partyOnlyLukeSettlementFixture(t)
		if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		f.conn.write.Reset()
		dispatchAntonCardRequest(t, f, session, 659, []byte{0})
		r195MovieWantPromptWire(t, f.conn.write.Bytes(), true)
		dispatchAntonCardRequest(t, f, session, 659, []byte{1})
		if !session.raidProjection.moviePromptSent || !session.raidProjection.movieCompleted || session.raidProjection.movieIdentity.Phase != 0 {
			t.Fatal("phase-one fixture did not establish prior-phase prompt/completion state")
		}
		enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
		if states, errs := f.service.advanceCurrentRaidClocks(first.BreakUntil); len(errs) != 0 || len(states) != 1 || f.state.Battle.Phase != 1 {
			t.Fatal("Luke did not enter phase two through its settlement boundary", errs)
		}
		peer.conn = &bufferConn{}
		at := f.state.Battle.EnterAfter.Add(time.Second)
		for _, id := range []uint32{5311, 5313, 5057, 5058, 5059, 5060, 5064, 5065, 5061, 5062} {
			owner := session
			if id == 5313 || id == 5058 || id == 5060 || id == 5065 || id == 5062 {
				owner = peer
			}
			clearOwnedLuke(t, f, owner, id, at)
			at = at.Add(time.Second)
		}
	case "fiendwar":
		var peers []*gameSession
		f, peers = actualCurrentFiendWar(t, 2)
		session, peer = peers[0], peers[1]
		b := f.state.Battle
		f.state.Started = true
		b.EnterAfter, b.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
		b.PhaseCompleted, b.FiendWar.Complete, b.PhaseCompletedAt = true, true, time.Now()
		b.ClearedRuns = map[string]currentRaidClearedRun{}
		for i := uint32(0); i < b.AntonRewards.Phases[0].MinimumClears; i++ {
			b.ClearedRuns[strconv.Itoa(int(i))] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{19, 20}}
		}
		if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown movie fixture", phase)
	}
	if peer == nil {
		peer = r195MoviePeer(f, session)
	}
	if b := f.state.Battle; b.RewardFlow[b.Phase] == nil {
		cfg, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
		if err != nil {
			t.Fatal(err)
		}
		b.AntonFlowConfig = cfg
		if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(time.Now()); len(errs) != 0 {
		t.Fatal(errs)
	}
	if f.state.Battle.Mode != 3 {
		r195RewardParty(t, f.service, 19, 20)
	}
	for _, owner := range []*gameSession{session, peer} {
		if err := f.service.sendCurrentAntonTerminalResult(owner, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
	}
	f.conn = session.conn.(*bufferConn)
	return f, []*gameSession{session, peer}
}

func r195MoviePeer(f *raidStartGuardFixture, leader *gameSession) *gameSession {
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	peer.conn = &bufferConn{}
	f.service.bindGameSessionCharacter(peer, 20)
	return peer
}

// Fixed v157 023FB4B3 -> 018FBF20 emits zero on input while the movie
// is still active. Only 023FAC8C -> 018FBED0(true) emits completion one.
func TestR195RealMovieSkipRequestDoesNotConfirmCompletion(t *testing.T) {
	for _, phase := range []string{"luke_phase0", "luke_phase1"} {
		t.Run(phase, func(t *testing.T) {
			f, sessions := r195MovieStageFixture(t, phase)
			session := sessions[0]
			b := f.state.Battle
			f.conn.write.Reset()
			for i := 0; i < 2; i++ {
				f.conn.write.Reset()
				dispatchAntonCardRequest(t, f, session, 659, []byte{0})
				r195MovieWantPromptWire(t, f.conn.write.Bytes(), i == 0)
				if b.RewardCardReady[b.Phase][session.selectedCharacterID] {
					t.Fatal("input skip request confirmed a movie that is still running")
				}
				if len(antonNativeCardBodies(t, f)) != 0 {
					t.Fatal("input skip request prematurely opened reward cards")
				}
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
			if len(b.RewardCards[b.Phase]) != 0 {
				t.Fatal("card choice after skip input but before completion reserved a reward")
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 659, []byte{1})
			if !b.RewardCardReady[b.Phase][session.selectedCharacterID] {
				t.Fatal("actual movie completion did not ready the member")
			}
			cards := antonNativeCardBodies(t, f)
			if len(cards) != 2 || len(cards[0]) == 0 || len(cards[1]) == 0 {
				t.Fatal("actual movie completion did not open party card selection")
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 659, []byte{1})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), false)
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 659, []byte{0})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), false)
			// A genuinely new connection gets its own movie even though this
			// character already finished it on the retired connection.
			f.service.shutdownGameSessionEvents(session, false)
			if f.service.currentRaidAuthenticated(session) {
				t.Fatal("retired movie connection retained canonical ownership")
			}
			replacement := f.session()
			replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = session.channel, true, true
			replacement.conn = &bufferConn{}
			f.service.bindGameSessionCharacter(replacement, 19)
			if err := f.service.sendCurrentAntonTerminalResult(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			f.conn = replacement.conn.(*bufferConn)
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, replacement, 659, []byte{0})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), true)
		})
	}
}

func r195MovieWantPromptWire(t *testing.T, stream []byte, prompt bool) {
	t.Helper()
	p, rest := splitGameServerUpperPacket(t, stream)
	if p.Header.MsgID != 659 || p.Header.Classification != 1 || len(p.Body) != 1 || p.Body[0] != 1 {
		t.Fatal("movie request missing exact successful ACK")
	}
	if prompt {
		p, rest = splitGameServerUpperPacket(t, rest)
		if p.Header.MsgID != 599 || p.Header.Classification != 0 || len(p.Body) != 0 {
			t.Fatal("movie skip prompt must be native NOTI599 with no body")
		}
	}
	if len(rest) != 0 {
		t.Fatal("movie skip request emitted extra reward/state/prompt packets")
	}
}

func TestR195RealMoviePromptPartialWriteRestoresOnlyOnNewConnection(t *testing.T) {
	f, sessions := r195MovieStageFixture(t, "luke_phase0")
	session := sessions[0]
	failed := &antonPartialWriteConn{Conn: session.conn}
	session.conn = failed
	if err := f.service.handleCurrentAntonMovieCompletion(session, 659, []byte{0}); err == nil {
		t.Fatal("partial prompt write was hidden")
	}
	if !failed.closed || !session.raidProjection.failed || session.raidProjection.moviePromptSent || session.raidProjection.movieCompleted {
		t.Fatal("failed prompt was recorded as sent/completed or stream remained reusable")
	}
	if err := f.service.handleCurrentAntonMovieCompletion(session, 659, []byte{0}); err == nil || failed.calls != 2 {
		t.Fatal("partial frame was retried on the old connection")
	}
	replacement := f.session()
	replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = session.channel, true, true
	replacement.conn = &bufferConn{}
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn = replacement.conn.(*bufferConn)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, replacement, 659, []byte{0})
	r195MovieWantPromptWire(t, f.conn.write.Bytes(), true)
	if len(f.state.Battle.RewardCardReady[0]) != 0 || len(f.state.Battle.RewardCards[0]) != 0 {
		t.Fatal("prompt transport recovery completed or granted rewards")
	}
}

func TestR195RealMovieMixedSkipAndNaturalCompletion(t *testing.T) {
	for _, phase := range []string{"luke_phase0", "luke_phase1"} {
		for _, naturalFirst := range []bool{false, true} {
			name := "skip_closes_first"
			if naturalFirst {
				name = "natural_closes_first"
			}
			t.Run(phase+"/"+name, func(t *testing.T) {
				f, sessions := r195MovieStageFixture(t, phase)
				b := f.state.Battle
				flow := b.RewardFlow[b.Phase]
				if flow == nil {
					t.Fatal("completed raid has no frozen reward flow")
				}
				flow.AutoSelectAt = time.Now().Add(time.Minute)
				for _, session := range sessions {
					if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
						t.Fatal(err)
					}
				}
				for _, session := range sessions {
					session.conn.(*bufferConn).write.Reset()
				}
				dispatchAntonCardRequest(t, f, sessions[0], 659, []byte{0})
				r195MovieWantPromptWire(t, f.conn.write.Bytes(), true)
				if sessions[1].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("one member's skip prompt was sent to another member")
				}
				if len(b.RewardCardReady[b.Phase]) != 0 || len(flow.Party) != 0 {
					t.Fatal("skip input changed readiness or rewards")
				}
				order := []int{0, 1}
				if naturalFirst {
					order = []int{1, 0}
				}
				for i, index := range order {
					f.conn = sessions[index].conn.(*bufferConn)
					f.conn.write.Reset()
					dispatchAntonCardRequest(t, f, sessions[index], 659, []byte{1})
					if len(b.RewardCardReady[b.Phase]) != i+1 || !b.RewardCardReady[b.Phase][sessions[index].selectedCharacterID] {
						t.Fatal("one member's completion changed the other member's readiness")
					}
					if len(antonNativeCardBodies(t, f)) != 2 {
						t.Fatal("completed member did not receive the party card lists")
					}
				}
				if len(flow.Party) != 0 || len(flow.Squad) != 0 {
					t.Fatal("movie completion granted or reserved rewards")
				}
			})
		}
	}
}
