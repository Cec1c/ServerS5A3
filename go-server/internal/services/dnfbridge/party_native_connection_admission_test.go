package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func nativeAdmissionBody() []byte {
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, 700)
	return body
}

func nativeAdmissionReply(p *partyNativeConnectionProof, session *gameSession, status byte) []byte {
	query := p.queries[session]
	body := append(append([]byte(nil), query[:]...), make([]byte, 8)...)
	copy(body, "PCR1")
	body[32] = status
	return body
}

// Synthetic clients supply the same fixed reply envelope as the bounded x86
// tests. This drives the real server admission/resume path; it is not evidence
// of a native UDP exchange or a rendered model.
func completeNativePartyAdmissionForTest(t *testing.T, s *Service, session *gameSession) {
	t.Helper()
	a := &s.partyNativeAdmission
	a.mu.Lock()
	p := a.bySession[session]
	a.mu.Unlock()
	if p == nil {
		return
	}
	for _, peer := range p.peers {
		if err := s.handlePartyNativeConnectionReply(peer.session, nativeAdmissionReply(p, peer.session, 0)); err != nil {
			t.Fatal(err)
		}
	}
	waitNativePartyAdmissionForTest(t, s, p)
}

func waitNativePartyAdmissionForTest(t *testing.T, s *Service, p *partyNativeConnectionProof) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		s.partyNativeAdmission.mu.Lock()
		active := s.partyNativeAdmission.byNonce[p.nonce] == p
		s.partyNativeAdmission.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("native admission worker did not retire")
}

func nativeAdmissionProofFixture(t *testing.T) (*inviteAcceptDungeonFixture, *partyNativeConnectionProof) {
	t.Helper()
	f := newInviteAcceptDungeonFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	f.formPartyThroughClientAccept(t)
	p, err := f.service.newPartyNativeConnectionProof(f.leader, f.service.runtimePartyStateForSession(f.leader), nativeAdmissionBody())
	if err != nil {
		t.Fatal(err)
	}
	f.service.partyNativeAdmission.byNonce = map[uint64]*partyNativeConnectionProof{p.nonce: p}
	f.service.partyNativeAdmission.bySession = map[*gameSession]*partyNativeConnectionProof{f.leader: p, f.follower: p}
	return f, p
}

func TestNativePartyConnectionReplyBindsNonceRosterSessionAndSingleUse(t *testing.T) {
	f, p := nativeAdmissionProofFixture(t)
	s := f.service
	valid := nativeAdmissionReply(p, f.follower, 0)
	for _, mutate := range []func([]byte){
		func(b []byte) { b[4] ^= 1 }, func(b []byte) { b[12] ^= 1 }, func(b []byte) { b[28] ^= 1 }, func(b []byte) { b[29] ^= 1 },
	} {
		bad := append([]byte(nil), valid...)
		mutate(bad)
		if err := s.handlePartyNativeConnectionReply(f.follower, bad); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.handlePartyNativeConnectionReply(f.leader, valid)
	if len(p.results) != 0 {
		t.Fatal("foreign or altered reply accepted")
	}
	_ = s.handlePartyNativeConnectionReply(f.follower, valid)
	_ = s.handlePartyNativeConnectionReply(f.follower, valid)
	if len(p.results) != 1 || !<-p.results {
		t.Fatal("ready reply or duplicate handling failed")
	}
	_ = s.handlePartyNativeConnectionReply(f.leader, nativeAdmissionReply(p, f.leader, 3))
	if <-p.results {
		t.Fatal("native failure state accepted")
	}
	if _, err := s.deferPartyNativeConnectionAdmission(f.leader, p.body, p.state, nil, false, p); err == nil {
		t.Fatal("unapproved proof accepted")
	}
	p.ready.Store(true) // Isolate the one-use guard after testing reply validation.
	if deferred, err := s.deferPartyNativeConnectionAdmission(f.leader, p.body, p.state, nil, false, p); deferred || err != nil {
		t.Fatal(deferred, err)
	}
	if _, err := s.deferPartyNativeConnectionAdmission(f.leader, p.body, p.state, nil, false, p); err == nil {
		t.Fatal("proof reused")
	}
	s.forgetPartyNativeProof(p)
}

func TestNativePartyConnectionProofRejectsLeaveAndExpiredReplies(t *testing.T) {
	f, p := nativeAdmissionProofFixture(t)
	p.deadline = time.Now().Add(-time.Second)
	_ = f.service.handlePartyNativeConnectionReply(f.follower, nativeAdmissionReply(p, f.follower, 0))
	if len(p.results) != 0 || f.service.partyNativeProofCurrent(p) {
		t.Fatal("expired native reply accepted")
	}
	p.deadline = time.Now().Add(time.Second)
	if err := f.service.handleOnlineLeaveParty(f.follower, 13); err != nil {
		t.Fatal(err)
	}
	if f.service.partyNativeProofCurrent(p) {
		t.Fatal("old roster proof survived leave")
	}
	solo := f.service.runtimePartyStateForSession(f.leader)
	if _, err := f.service.deferPartyNativeConnectionAdmission(f.leader, p.body, solo, nil, false, p); err == nil {
		t.Fatal("old proof downgraded to solo entry")
	}
	f.service.forgetPartyNativeProof(p)
}

func TestNativePartyConnectionProofRejectsLeaderABAAndSessionRebind(t *testing.T) {
	for _, mode := range []string{"leader_away_and_back", "member_generation"} {
		t.Run(mode, func(t *testing.T) {
			f, p := nativeAdmissionProofFixture(t)
			m := f.service.runtimePartyManagerForService()
			leader, follower := p.peers[0], p.peers[1]
			if mode == "leader_away_and_back" {
				if r := m.TransferLeader(leader.character, leader.generation, follower.character, follower.generation); !r.OK {
					t.Fatal(r.Reason)
				}
				if r := m.TransferLeader(follower.character, follower.generation, leader.character, leader.generation); !r.OK {
					t.Fatal(r.Reason)
				}
			} else {
				snap, _ := m.SnapshotByUser(follower.character, follower.generation)
				for _, member := range snap.Members {
					if member.UserID == follower.character {
						member.SessionGeneration++
						if r := m.RebindSession(member.UserID, follower.generation, member); !r.OK {
							t.Fatal(r.Reason)
						}
					}
				}
			}
			if f.service.partyNativeProofCurrent(p) {
				t.Fatal("old query survived membership mutation", mode)
			}
			f.service.forgetPartyNativeProof(p)
		})
	}
}

func TestNativePartyConnectionEventLoopsRemainAvailableForReplies(t *testing.T) {
	f := newLeaderHandoffRoundtripFixture(t)
	s := f.service
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	f.formPartyThroughClientAccept(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, peer := range []*gameSession{f.leader, f.follower} {
		if err := s.startGameSessionEvents(ctx, peer); err != nil {
			t.Fatal(err)
		}
		defer s.stopGameSessionEvents(context.Background(), peer)
	}
	if err := s.callGameSession(ctx, f.leader, "test-native-entry", func() error { return s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()) }); err != nil {
		t.Fatal(err)
	}
	s.partyNativeAdmission.mu.Lock()
	p := s.partyNativeAdmission.bySession[f.leader]
	s.partyNativeAdmission.mu.Unlock()
	if p == nil {
		t.Fatal("missing pending native admission")
	}
	for _, peer := range p.peers {
		if err := s.callGameSession(ctx, peer.session, "test-native-reply", func() error {
			return s.handlePartyNativeConnectionReply(peer.session, nativeAdmissionReply(p, peer.session, 0))
		}); err != nil {
			t.Fatal("reply event blocked", err)
		}
	}
	waitNativePartyAdmissionForTest(t, s, p)
	for _, peer := range p.peers {
		peer.session.dungeon.mu.Lock()
		active := peer.session.dungeon.runtime != nil
		peer.session.dungeon.mu.Unlock()
		if !active {
			t.Fatal("all-ready event-loop admission failed", peer.character)
		}
	}
}

func TestNativePartyConnectionFailureAndTimeoutBeforeAnyEntryMutation(t *testing.T) {
	for _, mode := range []string{"failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			s := f.service
			s.options.currentClientVersion = "official-24.9"
			t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
			f.formPartyThroughClientAccept(t)
			f.leader.conn.(*bufferConn).write.Reset()
			f.follower.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
				t.Fatal(err)
			}
			s.partyNativeAdmission.mu.Lock()
			p := s.partyNativeAdmission.bySession[f.leader]
			s.partyNativeAdmission.mu.Unlock()
			if p == nil {
				t.Fatal("entry did not request native connection proof")
			}
			// A duplicate click cannot create another worker or commit a runtime.
			if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
				t.Fatal(err)
			}
			if mode == "failure" {
				_ = s.handlePartyNativeConnectionReply(f.follower, nativeAdmissionReply(p, f.follower, 3))
			}
			waitNativePartyAdmissionForTest(t, s, p)
			for _, session := range []*gameSession{f.leader, f.follower} {
				session.dungeon.mu.Lock()
				runtime := session.dungeon.runtime
				session.dungeon.mu.Unlock()
				if runtime != nil {
					t.Fatal("failed native admission committed dungeon")
				}
			}
			repos, _ := s.repositoryGroup()
			for _, id := range []string{"99", "100"} {
				c, ok, err := repos.Character.Load(context.Background(), id)
				if err != nil || !ok || c.Stats["fatigue"] != 100 {
					t.Fatal("failed admission changed fatigue", id, c.Stats, err)
				}
			}
			data := f.leader.conn.(*bufferConn).write.Bytes()
			refused := false
			for len(data) > 0 {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
				data = rest
				if packet.Header.Classification == 1 && packet.Header.MsgID == 16 {
					refused = bytes.Equal(packet.Body, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0})
				}
				if packet.Header.Classification == 0 && (packet.Header.MsgID == 28 || packet.Header.MsgID == 120) {
					t.Fatal("entry packets escaped before connection readiness")
				}
			}
			if !refused {
				t.Fatal("native admission failure did not return op16 refusal")
			}
		})
	}
}
