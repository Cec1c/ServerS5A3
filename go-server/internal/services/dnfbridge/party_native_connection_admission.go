package dnfbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

const partyNativeConnectionOpcode uint16 = 0x7f13
const partyNativeConnectionTimeout = 3 * time.Second
const partyNativeConnectionRoundTimeout = time.Second
const partyNativeConnectionRetryInterval = 250 * time.Millisecond
const partyNativeConnectionCommitGrace = time.Second

func partyNativeConnectionCommitGraceForMembers(memberCount int) time.Duration {
	grace := partyDungeonEntryPreflightTimeout(memberCount) + 2*time.Second
	if grace < partyNativeConnectionCommitGrace {
		grace = partyNativeConnectionCommitGrace
	}
	if grace > 10*time.Second {
		return 10 * time.Second
	}
	return grace
}

// A reply proves only the freshly challenged roster/connection snapshot, not
// future network availability. It cannot be reused after a handoff, reconnect,
// leave, different selector request or a second attempt at the same dungeon.
type partyNativeConnectionAdmission struct {
	mu        sync.Mutex
	byNonce   map[uint64]*partyNativeConnectionProof
	bySession map[*gameSession]*partyNativeConnectionProof
}

type partyNativeConnectionRound struct {
	nonce    uint64
	queries  map[*gameSession][32]byte
	results  chan bool
	received map[*gameSession]bool // admission.mu
}

type partyNativeConnectionProof struct {
	nonce                  uint64
	leader                 boundGameSessionCharacter
	peers                  []boundGameSessionCharacter
	queries                map[*gameSession][32]byte
	queryBase              [32]byte
	memberSlots            []byte
	state                  alignedcmd.PartyState
	currentMembership      func() bool
	deadline               time.Time
	results                chan bool
	received               map[*gameSession]bool       // admission.mu
	activeRound            *partyNativeConnectionRound // admission.mu
	retryUntilReady        bool
	commitDeadlineUnixNano atomic.Int64
	used                   atomic.Bool
	ready                  atomic.Bool
	body                   []byte
	resume                 func() error
	refuse                 func(error) error // Must also be safe when the leader queue cannot run.
}

func (s *Service) nativePartyConnectionAdmissionEnabled() bool {
	return s.options.currentClientVersion == "official-24.9" && os.Getenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION") != "off"
}

func partyNativeProofDeadline(p *partyNativeConnectionProof) time.Time {
	if p == nil {
		return time.Time{}
	}
	deadline := p.deadline
	if p.ready.Load() {
		if ns := p.commitDeadlineUnixNano.Load(); ns > 0 {
			if commit := time.Unix(0, ns); commit.After(deadline) {
				deadline = commit
			}
		}
	}
	return deadline
}

func (s *Service) partyNativeProofCurrent(p *partyNativeConnectionProof) bool {
	if p == nil || !time.Now().Before(partyNativeProofDeadline(p)) || !p.currentMembership() {
		return false
	}
	for _, peer := range p.peers {
		if !s.boundGameSessionCharacterCurrent(peer) {
			return false
		}
	}
	return true
}

func randomPartyNativeNonce() (uint64, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, err
	}
	nonce := binary.LittleEndian.Uint64(random[:])
	if nonce == 0 {
		return 0, errors.New("zero native connection nonce")
	}
	return nonce, nil
}

func newPartyNativeConnectionRound(base [32]byte, peers []boundGameSessionCharacter, slots []byte, nonce uint64) *partyNativeConnectionRound {
	roundBase := base
	binary.LittleEndian.PutUint64(roundBase[4:12], nonce)
	queries := make(map[*gameSession][32]byte, len(peers))
	for i, peer := range peers {
		query := roundBase
		query[28] = slots[i]
		queries[peer.session] = query
	}
	return &partyNativeConnectionRound{
		nonce:    nonce,
		queries:  queries,
		results:  make(chan bool, 8),
		received: make(map[*gameSession]bool, len(peers)),
	}
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
		deadline: time.Now().Add(partyNativeConnectionTimeout), currentMembership: func() bool {
			now, found := manager.SnapshotByUser(leader.character, leader.generation)
			return found && now.MembershipRevision == snapshot.MembershipRevision && membership()
		}}
	nonce, err := randomPartyNativeNonce()
	if err != nil {
		return nil, err
	}
	var base [32]byte
	copy(base[:], "PCQ1")
	var occupied [8]bool
	var leaderFound bool
	members := runtimePartyMembers(state)
	if len(members) < 2 || len(members) > 8 {
		return nil, errors.New("invalid connection query party size")
	}
	p.memberSlots = make([]byte, 0, len(members))
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
		p.memberSlots = append(p.memberSlots, member.Slot)
	}
	if !leaderFound {
		return nil, errors.New("connection query leader absent")
	}
	p.queryBase = base
	round := newPartyNativeConnectionRound(base, p.peers, p.memberSlots, nonce)
	p.nonce = round.nonce
	p.queries = round.queries
	p.results = round.results
	p.received = round.received
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
	if p == nil || !time.Now().Before(p.deadline) {
		return nil
	}
	var queries map[*gameSession][32]byte
	var received map[*gameSession]bool
	var results chan bool
	if p.activeRound != nil {
		if p.activeRound.nonce != nonce {
			return nil
		}
		queries, received, results = p.activeRound.queries, p.activeRound.received, p.activeRound.results
	} else {
		if p.nonce != nonce {
			return nil
		}
		queries, received, results = p.queries, p.received, p.results
	}
	if received[session] {
		return nil
	}
	query, expected := queries[session]
	if !expected || !bytes.Equal(body[4:32], query[4:]) {
		return nil
	}
	valid := s.partyNativeProofCurrent(p)
	for _, status := range body[32:] {
		valid = valid && status == 0
	}
	received[session] = true
	results <- valid // bounded once per expected member; channel capacity eight
	return nil
}

func (s *Service) activePartyNativeRound(p *partyNativeConnectionProof) *partyNativeConnectionRound {
	a := &s.partyNativeAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.activeRound != nil {
		return p.activeRound
	}
	return &partyNativeConnectionRound{
		nonce:    p.nonce,
		queries:  p.queries,
		results:  p.results,
		received: p.received,
	}
}

func (s *Service) retirePartyNativeRound(p *partyNativeConnectionProof, round *partyNativeConnectionRound) {
	if p == nil || round == nil {
		return
	}
	a := &s.partyNativeAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byNonce[round.nonce] == p {
		delete(a.byNonce, round.nonce)
	}
	if p.activeRound == round {
		p.activeRound = nil
	}
}

func (s *Service) rotatePartyNativeRound(p *partyNativeConnectionProof) (*partyNativeConnectionRound, error) {
	if p == nil {
		return nil, errors.New("native party connection proof missing")
	}
	a := &s.partyNativeAdmission
	for attempt := 0; attempt < 8; attempt++ {
		nextNonce, err := randomPartyNativeNonce()
		if err != nil {
			continue
		}
		nextRound := newPartyNativeConnectionRound(p.queryBase, p.peers, p.memberSlots, nextNonce)
		a.mu.Lock()
		if a.bySession[p.leader.session] != p {
			a.mu.Unlock()
			return nil, errors.New("native party connection reservation lost")
		}
		if a.byNonce[nextNonce] != nil {
			a.mu.Unlock()
			continue
		}
		if old := p.activeRound; old != nil && a.byNonce[old.nonce] == p {
			delete(a.byNonce, old.nonce)
		}
		a.byNonce[nextNonce] = p
		p.activeRound = nextRound
		a.mu.Unlock()
		return nextRound, nil
	}
	return nil, errors.New("native party nonce collision")
}

func (s *Service) forgetPartyNativeProof(p *partyNativeConnectionProof) {
	a := &s.partyNativeAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byNonce[p.nonce] == p {
		delete(a.byNonce, p.nonce)
	}
	if p.activeRound != nil && a.byNonce[p.activeRound.nonce] == p {
		delete(a.byNonce, p.activeRound.nonce)
	}
	p.activeRound = nil
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
	// Automatic entry opens its query while it owns the temporary party.
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
	p.retryUntilReady = true
	if resume != nil {
		p.resume = func() error { return resume(p) }
		p.refuse = refuse
	}
	return s.startPartyNativeConnectionQuery(p, routed, admitted, nil)
}

func (s *Service) startPartyNativeConnectionQuery(p *partyNativeConnectionProof, routed *partyDungeonEntryRoutedRequest, admitted bool, finish func()) (bool, error) {
	a := &s.partyNativeAdmission
	a.mu.Lock()
	if len(a.byNonce) >= 64 {
		a.mu.Unlock()
		return false, errors.New("native party admission busy")
	}
	for _, peer := range p.peers {
		if a.bySession[peer.session] != nil {
			a.mu.Unlock()
			if routed != nil || finish != nil {
				return false, errors.New("selector already has a native connection query")
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
	p.activeRound = &partyNativeConnectionRound{
		nonce:    p.nonce,
		queries:  p.queries,
		results:  p.results,
		received: p.received,
	}
	a.byNonce[p.nonce] = p
	for _, peer := range p.peers {
		a.bySession[peer.session] = p
	}
	a.mu.Unlock()
	go func() {
		defer s.forgetPartyNativeProof(p)
		if finish != nil {
			defer finish()
		}
		s.runPartyNativeConnectionAdmission(p, routed, admitted)
	}()
	return true, nil
}

func partyNativeConnectionErrorKind(cause error) string {
	if cause == nil {
		return "unknown"
	}
	msg := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(msg, "not ready"):
		return "peer_not_ready"
	case strings.Contains(msg, "timed out"), errors.Is(cause, context.DeadlineExceeded):
		return "query_timeout"
	case strings.Contains(msg, "changed"), strings.Contains(msg, "authoritative roster"), strings.Contains(msg, "reservation lost"):
		return "party_changed"
	case strings.Contains(msg, "offline"), strings.Contains(msg, "lost leader"), strings.Contains(msg, "leader absent"):
		return "member_offline"
	case strings.Contains(msg, "busy"), strings.Contains(msg, "already has"), strings.Contains(msg, "collision"):
		return "busy"
	case strings.Contains(msg, "expired"):
		return "proof_expired"
	default:
		return "other"
	}
}

func (s *Service) refusePartyNativeConnectionAdmission(session *gameSession, body []byte, state alignedcmd.PartyState, routed *partyDungeonEntryRoutedRequest, cause error) error {
	request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.answerRoutedPartyEntryOwnerOnRefusal(ctx, session, state, routed, request, "party_native_connection_not_ready", "entry_connection_not_ready", currentSelectDungeonInvalidSelectionStateReason, cause)
	return s.refuseDungeonSelectEntry(session, request, "party_native_connection_not_ready", "entry_connection_not_ready", currentSelectDungeonInvalidSelectionStateReason, cause,
		"error_kind", partyNativeConnectionErrorKind(cause))
}

func (s *Service) runPartyNativeConnectionSingleRound(ctx context.Context, p *partyNativeConnectionProof, round *partyNativeConnectionRound) (retryable bool, err error) {
	for _, peer := range p.peers {
		query := round.queries[peer.session]
		sendErr := s.callGameSession(ctx, peer.session, "party-native-connection-query", func() error {
			if !s.partyNativeProofCurrent(p) {
				return errors.New("party changed before native query")
			}
			return s.sendGameUpperFixed16Transport(peer.session, partyNativeConnectionOpcode, query[:], 1, 0, false, "party_native_connection_query_v1")
		})
		if sendErr != nil {
			// Only a per-round timeout while the roster/session is still valid is retryable.
			return errors.Is(sendErr, context.DeadlineExceeded) && s.partyNativeProofCurrent(p), sendErr
		}
	}
	for count := 0; count < len(p.peers); count++ {
		select {
		case ready := <-round.results:
			if !ready {
				return true, errors.New("client native roster or UDP connection is not ready")
			}
		case <-ctx.Done():
			return true, errors.New("client native connection query timed out")
		}
	}
	return false, nil
}

func (s *Service) runPartyNativeConnectionRetryLoop(ctx context.Context, p *partyNativeConnectionProof) error {
	round := s.activePartyNativeRound(p)
	var lastErr error
	var notReadyErr error
	finalErr := func() error {
		if notReadyErr != nil && lastErr != nil && partyNativeConnectionErrorKind(lastErr) == "query_timeout" {
			return notReadyErr
		}
		return lastErr
	}
	for {
		if !s.partyNativeProofCurrent(p) {
			if lastErr != nil && !time.Now().Before(p.deadline) {
				return finalErr()
			}
			return errors.New("party changed before native query")
		}
		roundDeadline := time.Now().Add(partyNativeConnectionRoundTimeout)
		if p.deadline.Before(roundDeadline) {
			roundDeadline = p.deadline
		}
		roundCtx, roundCancel := context.WithDeadline(ctx, roundDeadline)
		retryable, err := s.runPartyNativeConnectionSingleRound(roundCtx, p, round)
		roundCancel()
		if err == nil {
			if !s.partyNativeProofCurrent(p) {
				return errors.New("party changed or native readiness expired")
			}
			return nil
		}
		lastErr = err
		if partyNativeConnectionErrorKind(err) == "peer_not_ready" {
			notReadyErr = err
		}
		s.retirePartyNativeRound(p, round)
		if !retryable || ctx.Err() != nil {
			return finalErr()
		}
		if !s.partyNativeProofCurrent(p) {
			if !time.Now().Before(p.deadline) {
				return finalErr()
			}
			return errors.New("party changed during native connection query")
		}
		timer := time.NewTimer(partyNativeConnectionRetryInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return finalErr()
		}
		if !s.partyNativeProofCurrent(p) {
			if !time.Now().Before(p.deadline) {
				return lastErr
			}
			return errors.New("party changed during native connection query")
		}
		nextRound, rotErr := s.rotatePartyNativeRound(p)
		if rotErr != nil {
			return rotErr
		}
		round = nextRound
	}
}

func (s *Service) runPartyNativeConnectionAdmission(p *partyNativeConnectionProof, routed *partyDungeonEntryRoutedRequest, admitted bool) {
	ctx, cancel := context.WithDeadline(context.Background(), p.deadline)
	defer cancel()
	var failure error
	if p.retryUntilReady {
		failure = s.runPartyNativeConnectionRetryLoop(ctx, p)
	} else {
		_, failure = s.runPartyNativeConnectionSingleRound(ctx, p, s.activePartyNativeRound(p))
	}
	commitGrace := partyNativeConnectionCommitGraceForMembers(len(p.peers))
	if failure == nil {
		commitDeadline := time.Now().Add(commitGrace)
		if p.deadline.After(commitDeadline) {
			commitDeadline = p.deadline
		}
		p.commitDeadlineUnixNano.Store(commitDeadline.UnixNano())
		p.ready.Store(true)
	}
	finishTimeout := createWriteTimeout
	if failure == nil && commitGrace > finishTimeout {
		finishTimeout = commitGrace
	}
	// A new deadline is only for scheduling the refusal; readiness still expires
	// at the bounded commit deadline and membership is checked again inside the leader event.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), finishTimeout)
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
