package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	"sync"
	"testing"
	"time"
)

// Public master already supplies these accepted-run real-PVF fixtures.
// 659/660 use the complete upper dispatcher. This test does not claim client
// combat or the cumulative six-map public-entry coverage.
func TestPublicP14AntonManualToFlowDoesNotReplay(t *testing.T) {
	for _, single := range []bool{true, false} {
		name := "team"
		if single {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			var f *raidStartGuardFixture
			var p *gameSession
			var at time.Time
			if single {
				f, p, at = singleAntonCompletedRewardFixture(t)
			} else {
				f, p, _, at = onlineAntonTerminalFixture(t)
				// This older terminal fixture predates reward-flow setup. Load the
				// actual flow config and run its normal entitlement freeze, rather
				// than manufacturing a card/reward map or treating nil as a defect.
				config, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
				if err != nil {
					t.Fatal(err)
				}
				f.state.Battle.AntonFlowConfig = config
				if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
					t.Fatal(err)
				}
			}
			if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
				t.Fatal(done, errs)
			}
			if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{0})
			if len(antonNativeCardBodies(t, f)) != 2 {
				t.Fatal("manual movie completion omitted masks")
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			publicP14NoCardRestart(t, f.conn.write.Bytes(), "unchanged manual mask")
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 2})
			want := int64(45)
			if single {
				want = 60
			}
			if got := antonGrantedItemCount(t, f, "19", 10304529); got != want {
				t.Fatalf("PVF party reward got %d want %d", got, want)
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			if single {
				publicP14NoCardRestart(t, f.conn.write.Bytes(), "unchanged committed party card")
			} else {
				// Manual660 sends one card; the full team projection legitimately
				// includes the still-unselected peer and must retain that update.
				for stream := f.conn.write.Bytes(); len(stream) > 0; {
					packet, rest := splitGameServerUpperPacket(t, stream)
					stream = rest
					if packet.Header.MsgID == 587 && len(packet.Body) == 2 && packet.Body[0] == 4 {
						t.Fatal("updated team party list restarted screen")
					}
				}
				if _, ok := antonNativeCardBodies(t, f)[1]; !ok {
					t.Fatal("changed team party list suppressed")
				}
			}
			flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			if _, ok := antonNativeCardBodies(t, f)[3]; !ok {
				t.Fatal("new squad display incorrectly suppressed")
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 3})
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			publicP14NoCardRestart(t, f.conn.write.Bytes(), "late manual requests after squad")
			if got := antonGrantedItemCount(t, f, "19", 10304529); got != want {
				t.Fatal("duplicate party grant", got)
			}
		})
	}
}

func publicP14NoCardRestart(t *testing.T, stream []byte, where string) {
	t.Helper()
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if p.Header.Classification == 0 && (p.Header.MsgID == 600 || p.Header.MsgID == 587 && len(p.Body) == 2 && p.Body[0] == 4) {
			t.Fatalf("%s replayed reward presentation opcode=%d body=%x", where, p.Header.MsgID, p.Body)
		}
	}
}

func TestPublicP14AntonManualMaskReconnectRestoresWithoutGrant(t *testing.T) {
	f, old, at := singleAntonCompletedRewardFixture(t)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(old, state); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, old, 659, []byte{0})
	replacement := f.session()
	conn := &bufferConn{}
	replacement.conn, replacement.loginPacketSeen, replacement.accountFromToken = conn, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonRewardFlowProjection(old, state); err == nil {
		t.Fatal("old owner projected after replacement")
	}
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	screens, lists := 0, 0
	for data := conn.write.Bytes(); len(data) > 0; {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 587 && len(packet.Body) == 2 && packet.Body[0] == 4 {
			screens++
		}
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 1 {
			lists++
		}
	}
	if screens != 1 || lists != 1 {
		t.Fatalf("fresh owned connection restore screens=%d lists=%d", screens, lists)
	}
	conn.write.Reset()
	if err := f.service.handleCurrentAntonMovieCompletion(replacement, 659, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	publicP14NoCardRestart(t, conn.write.Bytes(), "restored connection late movie")
	if got := antonGrantedItemCount(t, f, "19", 10304529); got != 0 {
		t.Fatal("restore minted reward", got)
	}
}

// Unlike a changed full-list fixture alone, both owners here actually send
// 660. The production asynchronous peer reveal must finish before its following
// flow projection. Lock-protected writes and bounded signature waits avoid
// reading/resetting a buffer while the peer event is still being delivered.
func TestPublicP14AntonLaterPeerRevealDoesNotRestartScreen(t *testing.T) {
	f, leader, _, at := onlineAntonTerminalFixture(t)
	s := f.service
	config, err := raid.LoadAntonFlowConfiguration(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Battle.AntonFlowConfig = config
	if err := s.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	if done, errs := s.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	peer, ok := s.onlineGameSession(20)
	if !ok {
		t.Fatal("fixture cleared peer missing")
	}
	left, right := &publicP14ConcurrentConn{bufferConn: &bufferConn{}}, &publicP14ConcurrentConn{bufferConn: &bufferConn{}}
	leader.conn, peer.conn = left, right
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, p := range []*gameSession{leader, peer} {
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		owner := p
		t.Cleanup(func() {
			stopctx, stop := context.WithTimeout(context.Background(), createWriteTimeout)
			defer stop()
			if err := s.stopGameSessionEvents(stopctx, owner); err != nil {
				t.Error(err)
			}
		})
	}
	call := func(p *gameSession, fn func() error) {
		t.Helper()
		callctx, stop := context.WithTimeout(context.Background(), createWriteTimeout)
		defer stop()
		if err := s.callGameSession(callctx, p, "public-anton-animation-test", fn); err != nil {
			t.Fatal(err)
		}
	}
	state := cloneRuntimeRaidState(*f.state)
	dispatch := func(p *gameSession, op uint16, body []byte) error {
		frame, err := dnfproto.BuildChannelPacket(op, body, 1, 1)
		if err != nil {
			return err
		}
		return s.handleGameUpper(p, frame)
	}
	for _, p := range []*gameSession{leader, peer} {
		call(p, func() error { return s.sendCurrentAntonTerminalResult(p, state) })
		// Complete upper packet dispatch, inside the owning event loop.
		call(p, func() error { return dispatch(p, 659, []byte{0}) })
	}
	left.take()
	right.take()
	call(leader, func() error { return dispatch(leader, 660, []byte{1, 0}) })
	leaderCard := f.state.Battle.RewardCards[1][19]
	leaderBody, err := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: 19, CardType: leaderCard.Card, Items: currentAntonPartyDisplayItems(leaderCard.Plan)}})
	if err != nil {
		t.Fatal(err)
	}
	publicP14WaitDisplay(t, peer, leaderBody)
	publicP14AssertUpdate(t, right.take(), leaderBody, "first owner reveal to peer")
	left.take()
	call(leader, func() error { return s.sendCurrentAntonRewardFlowProjection(leader, cloneRuntimeRaidState(*f.state)) })
	publicP14AssertUpdate(t, left.take(), nil, "leader party full list")
	call(peer, func() error { return dispatch(peer, 660, []byte{1, 1}) })
	peerCard := f.state.Battle.RewardCards[1][20]
	peerBody, err := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: 20, CardType: peerCard.Card, Items: currentAntonPartyDisplayItems(peerCard.Plan)}})
	if err != nil {
		t.Fatal(err)
	}
	publicP14WaitDisplay(t, leader, peerBody)
	publicP14AssertUpdate(t, left.take(), peerBody, "later peer reveal to leader")
	call(leader, func() error { return s.sendCurrentAntonRewardFlowProjection(leader, cloneRuntimeRaidState(*f.state)) })
	publicP14AssertUpdate(t, left.take(), nil, "updated full group after later peer")
	for _, id := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, id, 10304529); got != 45 {
			t.Fatal("actual party amount", id, got)
		}
	}
	flow := f.state.Battle.RewardFlow[1]
	if _, errs := s.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	call(leader, func() error { return s.sendCurrentAntonRewardFlowProjection(leader, cloneRuntimeRaidState(*f.state)) })
	screens, squads := 0, 0
	for wire := left.take(); len(wire) > 0; {
		packet, rest := splitGameServerUpperPacket(t, wire)
		wire = rest
		if packet.Header.MsgID == 587 && bytes.Equal(packet.Body, []byte{4, 0}) {
			screens++
		}
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 3 {
			squads++
		}
	}
	if screens != 1 || squads != 1 {
		t.Fatalf("new squad screen suppressed/replayed: screens=%d lists=%d", screens, squads)
	}
	// Replay of the now delivered squad projection does not add assets or UI.
	call(leader, func() error { return s.sendCurrentAntonRewardFlowProjection(leader, cloneRuntimeRaidState(*f.state)) })
	publicP14NoCardRestart(t, left.take(), "repeat new squad")
}

type publicP14ConcurrentConn struct {
	*bufferConn
	mu sync.Mutex
}

func (c *publicP14ConcurrentConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bufferConn.Write(p)
}
func (c *publicP14ConcurrentConn) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]byte(nil), c.write.Bytes()...)
	c.write.Reset()
	return out
}

func publicP14WaitDisplay(t *testing.T, p *gameSession, body []byte) {
	t.Helper()
	want := sha256.Sum256(body)
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		p.raidProjection.mu.Lock()
		got := p.raidProjection.rewardDisplaySignature
		p.raidProjection.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("production peer reveal did not finish with the expected successful display signature")
}

func publicP14AssertUpdate(t *testing.T, wire, body []byte, where string) {
	t.Helper()
	found := false
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacket(t, wire)
		wire = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 587 && bytes.Equal(packet.Body, []byte{4, 0}) {
			t.Fatal(where, "restarted screen")
		}
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 1 && (body == nil || bytes.Equal(packet.Body, body)) {
			found = true
		}
	}
	if !found {
		t.Fatal(where, "lost actual party list")
	}
}
