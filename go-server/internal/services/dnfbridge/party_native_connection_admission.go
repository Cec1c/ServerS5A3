package dnfbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

const partyNativeConnectionOpcode uint16 = 0x7f13
const partyNativeConnectionTimeout = 3 * time.Second

// A reply proves only the freshly challenged roster/connection snapshot, not
// future network availability. It cannot be reused after a handoff, reconnect,
// leave, different selector request or a second attempt at the same dungeon.
type partyNativeConnectionAdmission struct {
	mu        sync.Mutex
	byNonce   map[uint64]*partyNativeConnectionProof
	bySession map[*gameSession]*partyNativeConnectionProof
}

type partyNativeConnectionProof struct {
	nonce             uint64
	leader            boundGameSessionCharacter
	peers             []boundGameSessionCharacter
	queries           map[*gameSession][32]byte
	state             alignedcmd.PartyState
	currentMembership func() bool
	deadline          time.Time
	results           chan bool
	received          map[*gameSession]bool // admission.mu
	used              atomic.Bool
	ready             atomic.Bool
	body              []byte
	resume            func() error
	refuse            func(error) error // Must also be safe when the leader queue cannot run.
}

func (s *Service) nativePartyConnectionAdmissionEnabled() bool {
	return s.options.currentClientVersion == "official-24.9" && os.Getenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION") != "off"
}

func (s *Service) partyNativeProofCurrent(p *partyNativeConnectionProof) bool {
	if p == nil || !time.Now().Before(p.deadline) || !p.currentMembership() {
		return false
	}
	for _, peer := range p.peers {
		if !s.boundGameSessionCharacterCurrent(peer) {
			return false
		}
	}
	return true
}

func (s *Service) newPartyNativeConnectionProof(session *gameSession, state alignedcmd.PartyState, body []byte) (*partyNativeConnectionProof, error) {
	leader, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || state.UserID != leader.character {
		return nil, errors.New("native connection query lost leader binding")
	}
	manager := s.runtimePartyManagerForService()
	snapshot, managed := manager.SnapshotByUser(leader.character, leader.generation)
	if !managed || !sameCurrentPartyProjectionState(state, snapshot.StateFor(leader.character)) {
		return nil, errors.New("native query requires authoritative roster")
	}
	membership := s.currentPartyProjectionMembershipGuard(leader, &state)
	p := &partyNativeConnectionProof{leader: leader, state: cloneRuntimePartyState(state), body: append([]byte(nil), body...),
		queries: make(map[*gameSession][32]byte), received: make(map[*gameSession]bool), results: make(chan bool, 8),
		deadline: time.Now().Add(partyNativeConnectionTimeout), currentMembership: func() bool {
			now, found := manager.SnapshotByUser(leader.character, leader.generation)
			return found && now.MembershipRevision == snapshot.MembershipRevision && membership()
		}}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	p.nonce = binary.LittleEndian.Uint64(random[:])
	if p.nonce == 0 {
		return nil, errors.New("zero native connection nonce")
	}
	var base [32]byte
	copy(base[:], "PCQ1")
	binary.LittleEndian.PutUint64(base[4:12], p.nonce)
	var occupied [8]bool
	var leaderFound bool
	members := runtimePartyMembers(state)
	if len(members) < 2 || len(members) > 8 {
		return nil, errors.New("invalid connection query party size")
	}
	for _, member := range members {
		if member.Slot >= 8 || occupied[member.Slot] || member.UserID == 0 {
			return nil, errors.New("invalid connection query roster")
		}
		occupied[member.Slot] = true
		binary.LittleEndian.PutUint16(base[12+int(member.Slot)*2:], member.UserID)
		if member.UserID == state.UserID {
			base[29] = member.Slot
			leaderFound = true
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online {
			return nil, errors.New("connection query member offline")
		}
		matched := false
		for _, original := range snapshot.Members {
			if original.UserID == peer.character && original.SessionGeneration == peer.generation && original.Slot == member.Slot {
				matched = true
			}
		}
		if !matched {
			return nil, errors.New("connection query membership generation changed")
		}
		p.peers = append(p.peers, peer)
	}
	if !leaderFound {
		return nil, errors.New("connection query leader absent")
	}
	for i, member := range members {
		query := base
		query[28] = member.Slot
		p.queries[p.peers[i].session] = query
	}
	if !s.partyNativeProofCurrent(p) {
		return nil, errors.New("connection query roster changed")
	}
	return p, nil
}

// Called from each recipient's own event loop. No waiting, network calls or
// game state mutation here: duplicate/stale/foreign replies are discarded.
func (s *Service) handlePartyNativeConnectionReply(session *gameSession, body []byte) error {
	if len(body) != 40 || string(body[:4]) != "PCR1" {
		return nil
	}
	nonce := binary.LittleEndian.Uint64(body[4:12])
	a := &s.partyNativeAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.byNonce[nonce]
	if p == nil || p.received[session] || !time.Now().Before(p.deadline) {
		return nil
	}
	query, expected := p.queries[session]
	if !expected || !bytes.Equal(body[4:32], query[4:]) {
		return nil
	}
	valid := s.partyNativeProofCurrent(p)
	for _, status := range body[32:] {
		valid = valid && status == 0
	}
	p.received[session] = true
	p.results <- valid // bounded once per expected member; channel capacity eight
	return nil
}

func (s *Service) forgetPartyNativeProof(p *partyNativeConnectionProof) {
	a := &s.partyNativeAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byNonce[p.nonce] == p {
		delete(a.byNonce, p.nonce)
	}
	for _, peer := range p.peers {
		if a.bySession[peer.session] == p {
			delete(a.bySession, peer.session)
		}
	}
}

// The caller must suppress its routed-refusal defer when deferred is true:
// this worker now owns that exact token and answers it on refusal. Waiting is
// outside every session event loop so recipients can process their queries.
func (s *Service) deferPartyNativeConnectionAdmission(session *gameSession, body []byte, state alignedcmd.PartyState,
	routed *partyDungeonEntryRoutedRequest, admitted bool, proof *partyNativeConnectionProof) (deferred bool, err error) {
	return s.deferPartyNativeConnectionAdmissionWithContinuation(session, body, state, routed, admitted, proof, nil, nil)
}

func (s *Service) deferPartyNativeConnectionAdmissionWithContinuation(session *gameSession, body []byte, state alignedcmd.PartyState,
	routed *partyDungeonEntryRoutedRequest, admitted bool, proof *partyNativeConnectionProof,
	resume func(*partyNativeConnectionProof) error, refuse func(error) error) (deferred bool, err error) {
	if proof != nil {
		if !proof.ready.Load() || proof.leader.session != session || !bytes.Equal(proof.body, body) || !s.partyNativeProofCurrent(proof) || !proof.used.CompareAndSwap(false, true) {
			return false, errors.New("native party connection proof expired or changed")
		}
		return false, nil
	}
	// FiendWar's automatic encounter owns a synchronous temporary-party/raid
	// reservation transaction whose defers retire it if entry has not committed.
	// Suspending that transaction here would destroy its just-created party.
	// This gate covers ordinary selectors (including Anton/Luke); automatic
	// encounter admission retains its existing dedicated transaction.
	if admitted {
		return false, nil
	}
	if !s.nativePartyConnectionAdmissionEnabled() || len(runtimePartyMembers(state)) < 2 {
		return false, nil
	}
	p, err := s.newPartyNativeConnectionProof(session, state, body)
	if err != nil {
		return false, err
	}
	if resume != nil {
		p.resume = func() error { return resume(p) }
		p.refuse = refuse
	}
	a := &s.partyNativeAdmission
	a.mu.Lock()
	if len(a.byNonce) >= 64 {
		a.mu.Unlock()
		return false, errors.New("native party admission busy")
	}
	for _, peer := range p.peers {
		if a.bySession[peer.session] != nil {
			a.mu.Unlock()
			if routed != nil {
				return false, errors.New("routed selector already has a native connection query")
			}
			// Repeated clicks on the same pending selector are one attempt.
			// A second response must not cancel the first attempt's native wait.
			return true, nil
		}
	}
	if a.byNonce == nil {
		a.byNonce = make(map[uint64]*partyNativeConnectionProof)
		a.bySession = make(map[*gameSession]*partyNativeConnectionProof)
	}
	if a.byNonce[p.nonce] != nil {
		a.mu.Unlock()
		return false, errors.New("native party nonce collision")
	}
	a.byNonce[p.nonce] = p
	for _, peer := range p.peers {
		a.bySession[peer.session] = p
	}
	a.mu.Unlock()
	go s.runPartyNativeConnectionAdmission(p, routed, admitted)
	return true, nil
}

func (s *Service) refusePartyNativeConnectionAdmission(session *gameSession, body []byte, state alignedcmd.PartyState, routed *partyDungeonEntryRoutedRequest, cause error) error {
	request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.answerRoutedPartyEntryOwnerOnRefusal(ctx, session, state, routed, request, "party_native_connection_not_ready", "entry_connection_not_ready", currentSelectDungeonInvalidSelectionStateReason, cause)
	return s.refuseDungeonSelectEntry(session, request, "party_native_connection_not_ready", "entry_connection_not_ready", currentSelectDungeonInvalidSelectionStateReason, cause)
}

func (s *Service) runPartyNativeConnectionAdmission(p *partyNativeConnectionProof, routed *partyDungeonEntryRoutedRequest, admitted bool) {
	defer s.forgetPartyNativeProof(p)
	ctx, cancel := context.WithDeadline(context.Background(), p.deadline)
	defer cancel()
	var failure error
	for _, peer := range p.peers {
		query := p.queries[peer.session]
		failure = s.callGameSession(ctx, peer.session, "party-native-connection-query", func() error {
			if !s.partyNativeProofCurrent(p) {
				return errors.New("party changed before native query")
			}
			return s.sendGameUpperFixed16Transport(peer.session, partyNativeConnectionOpcode, query[:], 1, 0, false, "party_native_connection_query_v1")
		})
		if failure != nil {
			break
		}
	}
	for count := 0; failure == nil && count < len(p.peers); count++ {
		select {
		case ready := <-p.results:
			if !ready {
				failure = errors.New("client native roster or UDP connection is not ready")
			}
		case <-ctx.Done():
			failure = errors.New("client native connection query timed out")
		}
	}
	if failure == nil {
		p.ready.Store(true)
	}
	// A new deadline is only for scheduling the refusal; readiness still expires
	// at the original deadline and is checked again inside the leader event.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer finishCancel()
	var finishClaim atomic.Uint32 // queued / started / cancelled before start
	finished := make(chan struct{})
	err := s.callGameSession(finishCtx, p.leader.session, "party-native-connection-result", func() error {
		if !finishClaim.CompareAndSwap(0, 1) {
			return nil
		}
		defer close(finished)
		if !s.boundGameSessionCharacterCurrent(p.leader) {
			return nil
		}
		if failure == nil && !s.partyNativeProofCurrent(p) {
			failure = errors.New("party changed or native readiness expired")
		}
		if failure != nil {
			if p.refuse != nil {
				return p.refuse(failure)
			}
			return s.refusePartyNativeConnectionAdmission(p.leader.session, p.body, p.state, routed, failure)
		}
		if p.resume != nil {
			return p.resume()
		}
		return s.handleDungeonSelectUpperPlannedWithNativeAdmission(p.leader.session, p.body, nil, true, routed, admitted, p)
	})
	if err != nil {
		if !finishClaim.CompareAndSwap(0, 2) {
			// Keep the reservation while an already-started callback commits.
			<-finished
		} else if s.boundGameSessionCharacterCurrent(p.leader) {
			// The leader's mailbox could not start the callback. This exact
			// attempt cannot commit now; answer without scheduling a new entry.
			if p.refuse != nil {
				// Retry owns a completed runtime and its exit gate. Do not leave
				// that gate consumed when the result callback cannot be queued.
				_ = p.refuse(err)
				s.logGameEvent(p.leader.session, "game-party-native-admission-finish-failed", "error", err)
				return
			}
			request, _ := dungeoncmd.DecodeSelectDungeonRequest(p.body)
			fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), createWriteTimeout)
			s.answerRoutedPartyEntryOwnerOnRefusal(fallbackCtx, p.leader.session, p.state, routed, request,
				"party_native_admission_result_queue_unavailable", "entry_connection_not_ready", currentSelectDungeonInvalidSelectionStateReason, err)
			fallbackCancel()
			_ = s.withGameWire(p.leader.session, func() error {
				if !s.boundGameSessionCharacterCurrent(p.leader) {
					return nil
				}
				return s.sendGameUpperRawClassCodecLocked(p.leader.session, 16, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}, 1, true)
			})
		}
	}
	if err != nil {
		s.logGameEvent(p.leader.session, "game-party-native-admission-finish-failed", "error", err)
	}
}
