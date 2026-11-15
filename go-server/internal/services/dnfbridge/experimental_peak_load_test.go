package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/platform/timequeue"
)

const (
	experimentalPeakHealthySessions   = 70
	experimentalPeakRepeatedUpdates   = 20
	experimentalPeakReadDeadline      = 5 * time.Second
	experimentalPeakOffHeldDeadline   = 500 * time.Millisecond
	experimentalPeakRequesterSessions = 1
)

// These sessions call the real bridge entry against in-memory repositories.
// They establish lock-release ordering only; they are neither 70 live players
// nor an SQL throughput benchmark.

// experimentalPeakNoWorkerClock records requests without starting a timer
// goroutine. These tests exercise the auction request path, not maintenance.
type experimentalPeakNoWorkerClock struct {
	mu            sync.Mutex
	scheduleCount int
	startCount    int
}

func (*experimentalPeakNoWorkerClock) Now() time.Time { return time.Now() }

func (q *experimentalPeakNoWorkerClock) ScheduleAfter(string, time.Duration, timequeue.Callback) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.scheduleCount++
	return nil
}

func (*experimentalPeakNoWorkerClock) Cancel(string) bool { return false }

func (q *experimentalPeakNoWorkerClock) Start(context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.startCount++
}

func (q *experimentalPeakNoWorkerClock) counts() (scheduled, started int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.scheduleCount, q.startCount
}

type experimentalPeakReadObservation struct {
	index       int
	session     *gameSession
	conn        *bufferConn
	err         error
	completedAt time.Time
}

type experimentalPeakScenarioObservation struct {
	readFrames          map[int][]byte
	mutationFrames      []byte
	completedBeforeGate int
	releasedAt          time.Duration
	firstReadAt         time.Duration
	lastReadAt          time.Duration
	readWindow          time.Duration
	elapsed             time.Duration
}

func TestExperimentalPeak70SyntheticSessionsAndBoundedUpdates(t *testing.T) {
	var off, on experimentalPeakScenarioObservation
	if !t.Run("off", func(t *testing.T) {
		var err error
		off, err = runExperimentalPeakSyntheticScenario(t, false)
		if err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	if !t.Run("on", func(t *testing.T) {
		var err error
		on, err = runExperimentalPeakSyntheticScenario(t, true)
		if err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	if off.completedBeforeGate != 0 || on.completedBeforeGate != experimentalPeakHealthySessions {
		t.Fatalf("release-boundary completions off=%d on=%d", off.completedBeforeGate, on.completedBeforeGate)
	}
	if len(off.readFrames) != experimentalPeakHealthySessions || len(on.readFrames) != experimentalPeakHealthySessions {
		t.Fatalf("read completion count off=%d on=%d", len(off.readFrames), len(on.readFrames))
	}
	if !bytes.Equal(off.mutationFrames, on.mutationFrames) {
		t.Fatalf("experiment changed requester frames: off=%x on=%x", off.mutationFrames, on.mutationFrames)
	}

	t.Run("20_repeated_search_updates_stay_bounded", testExperimentalPeakRepeatedSearchUpdatesStayBounded)
}

func runExperimentalPeakSyntheticScenario(t *testing.T, enabled bool) (experimentalPeakScenarioObservation, error) {
	t.Helper()
	enableAuctionMutationExperiment(t, enabled)
	repositories, s, _, _ := auctionReadFixture(t)
	s.auctionItemEligibility = func(itemID uint32) bool { return itemID == 6001 || itemID == 6002 }
	clock := &experimentalPeakNoWorkerClock{}
	s.gameplayTimers = clock
	if s.auctionMaintenanceContext != nil {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("fixture unexpectedly started auction maintenance")
	}

	const listingItemID = 6002
	listingID := seedAuctionMutationBiddingListing(t, repositories, listingItemID)
	search := auctionSearchItemsBody(listingItemID)
	bidderConn := &bufferConn{}
	bidder := &gameSession{
		conn:                bidderConn,
		connID:              "experimental-peak-requester",
		selectedCharacterID: 11,
		accountID:           "a-11",
	}
	if err := s.handleCurrentAuctionCommand(bidder, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), search); err != nil {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("prime requester search: %w", err)
	}
	bidderConn.write.Reset()

	mutationBody := auctionBuyoutBody(listingID, listingItemID, 100)
	gate, mutationDone := startAuctionMutationGate(
		t, s, bidder, uint16(dnfenum.CmdPacketAuctionBidding), mutationBody, nil,
	)
	if enabled {
		if !s.auctionMu.TryLock() {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("experiment on still holds auctionMu at the blocked ACK")
		}
		s.auctionMu.Unlock()
	} else {
		if s.auctionMu.TryLock() {
			s.auctionMu.Unlock()
			return experimentalPeakScenarioObservation{}, fmt.Errorf("experiment off released auctionMu before the blocked ACK")
		}
	}

	sessions := make([]*gameSession, experimentalPeakHealthySessions)
	connections := make([]*bufferConn, experimentalPeakHealthySessions)
	for index := range sessions {
		characterID := uint16(10)
		accountID := "a-10"
		if index%2 == 1 {
			characterID = 11
			accountID = "a-11"
		}
		connections[index] = &bufferConn{}
		sessions[index] = &gameSession{
			conn:                connections[index],
			connID:              fmt.Sprintf("experimental-peak-healthy-%02d", index),
			selectedCharacterID: characterID,
			accountID:           accountID,
		}
	}

	// The start channel is the only release for the 70 request goroutines.
	// Waiting on a bounded result deadline then proves whether they can finish
	// while the requester gate still owns the lock or blocked first write.
	ready := make(chan struct{}, experimentalPeakHealthySessions)
	attempting := make(chan struct{}, experimentalPeakHealthySessions)
	start := make(chan struct{})
	results := make(chan experimentalPeakReadObservation, experimentalPeakHealthySessions)
	var startOnce sync.Once
	var reads sync.WaitGroup
	reads.Add(experimentalPeakHealthySessions)
	for index, session := range sessions {
		index, session := index, session
		go func() {
			defer reads.Done()
			ready <- struct{}{}
			<-start
			attempting <- struct{}{}
			err := s.handleCurrentAuctionCommand(session, uint16(dnfenum.CmdPacketAuctionMyBiddingInfo), []byte{currentAuctionPayTypeGold})
			results <- experimentalPeakReadObservation{
				index:       index,
				session:     session,
				conn:        connections[index],
				err:         err,
				completedAt: time.Now(),
			}
		}()
	}

	mutationSettled := false
	defer func() {
		startOnce.Do(func() { close(start) })
		gate.Release()
		if !mutationSettled {
			timer := time.NewTimer(experimentalPeakReadDeadline)
			defer timer.Stop()
			select {
			case <-mutationDone:
			case <-timer.C:
			}
		}
		reads.Wait()
	}()

	readyTimer := time.NewTimer(experimentalPeakReadDeadline)
	defer readyTimer.Stop()
	for index := 0; index < experimentalPeakHealthySessions; index++ {
		select {
		case <-ready:
		case <-readyTimer.C:
			return experimentalPeakScenarioObservation{}, fmt.Errorf("only %d/%d synthetic readers reached the start barrier", index, experimentalPeakHealthySessions)
		}
	}
	startedAt := time.Now()
	startOnce.Do(func() { close(start) })
	for index := 0; index < experimentalPeakHealthySessions; index++ {
		select {
		case <-attempting:
		case <-readyTimer.C:
			return experimentalPeakScenarioObservation{}, fmt.Errorf("only %d/%d synthetic readers entered the auction command", index, experimentalPeakHealthySessions)
		}
	}

	collected := make([]experimentalPeakReadObservation, 0, experimentalPeakHealthySessions)
	completedBeforeGate := 0
	if enabled {
		var err error
		collected, err = collectExperimentalPeakReads(results, experimentalPeakHealthySessions, experimentalPeakReadDeadline)
		if err != nil {
			return experimentalPeakScenarioObservation{}, err
		}
		completedBeforeGate = len(collected)
		if gate.write.Len() != 0 {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("experiment on requester wrote before its gate was released")
		}
	} else {
		heldTimer := time.NewTimer(experimentalPeakOffHeldDeadline)
		select {
		case result := <-results:
			heldTimer.Stop()
			return experimentalPeakScenarioObservation{}, fmt.Errorf(
				"experiment off reader %d completed while the requester held auctionMu (err=%v)", result.index, result.err,
			)
		case <-heldTimer.C:
		}
	}

	gate.Release()
	releasedAt := time.Now()
	remaining := experimentalPeakHealthySessions - len(collected)
	if remaining > 0 {
		more, err := collectExperimentalPeakReads(results, remaining, experimentalPeakReadDeadline)
		if err != nil {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("after requester gate release: %w", err)
		}
		collected = append(collected, more...)
	}

	mutationTimer := time.NewTimer(experimentalPeakReadDeadline)
	select {
	case err := <-mutationDone:
		mutationTimer.Stop()
		mutationSettled = true
		if err != nil {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("requester mutation: %w", err)
		}
	case <-mutationTimer.C:
		return experimentalPeakScenarioObservation{}, fmt.Errorf("requester mutation did not finish after gate release")
	}
	reads.Wait()

	if s.auctionMaintenanceContext != nil {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("auction maintenance worker was started by the synthetic request path")
	}
	if scheduled, started := clock.counts(); scheduled != 0 || started != 0 {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("no-worker clock scheduled=%d started=%d", scheduled, started)
	}

	observation := experimentalPeakScenarioObservation{
		readFrames:          make(map[int][]byte, experimentalPeakHealthySessions),
		mutationFrames:      append([]byte(nil), gate.write.Bytes()...),
		completedBeforeGate: completedBeforeGate,
		releasedAt:          releasedAt.Sub(startedAt),
	}
	seenSessions := make(map[*gameSession]struct{}, experimentalPeakHealthySessions)
	seenConnections := make(map[*bufferConn]struct{}, experimentalPeakHealthySessions)
	firstReadSet := false
	for _, result := range collected {
		if result.err != nil {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("healthy session %d: %w", result.index, result.err)
		}
		if _, duplicate := seenSessions[result.session]; duplicate {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("healthy session %d reused a gameSession", result.index)
		}
		if _, duplicate := seenConnections[result.conn]; duplicate {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("healthy session %d reused a bufferConn", result.index)
		}
		seenSessions[result.session] = struct{}{}
		seenConnections[result.conn] = struct{}{}
		packet, rest := splitGameServerUpperPacketWithHeader(t, result.conn.write.Bytes(), 16)
		if len(rest) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionMyBiddingInfo) ||
			len(packet.Body) == 0 || packet.Body[0] != 1 {
			return experimentalPeakScenarioObservation{}, fmt.Errorf(
				"healthy session %d response header=%+v body=%x rest=%x",
				result.index, packet.Header, packet.Body, rest,
			)
		}
		observation.readFrames[result.index] = append([]byte(nil), result.conn.write.Bytes()...)
		completed := result.completedAt.Sub(startedAt)
		if !firstReadSet || completed < observation.firstReadAt {
			observation.firstReadAt = completed
			firstReadSet = true
		}
		if completed > observation.lastReadAt {
			observation.lastReadAt = completed
		}
	}
	if !firstReadSet {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("no healthy read completion timestamps were collected")
	}
	observation.readWindow = observation.lastReadAt - observation.firstReadAt
	observation.elapsed = observation.lastReadAt
	mutationFrames := splitAuctionMutationOpcodes(t, observation.mutationFrames)
	wantMutationFrames := []uint16{
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
	}
	if len(mutationFrames) != len(wantMutationFrames) {
		return experimentalPeakScenarioObservation{}, fmt.Errorf("requester frame count=%d want=%d", len(mutationFrames), len(wantMutationFrames))
	}
	for index := range wantMutationFrames {
		if mutationFrames[index] != wantMutationFrames[index] {
			return experimentalPeakScenarioObservation{}, fmt.Errorf("requester frame %d=%d want=%d", index, mutationFrames[index], wantMutationFrames[index])
		}
	}

	t.Logf(
		"synthetic_in_memory=true player_benchmark=false sql_benchmark=false enabled=%t sessions=%d healthy=%d completed_before_release=%d completed_after_release=%d release_at=%s first_read=%s last_read=%s read_window=%s elapsed=%s requester_opcodes=%v",
		enabled,
		experimentalPeakRequesterSessions+experimentalPeakHealthySessions,
		experimentalPeakHealthySessions,
		observation.completedBeforeGate,
		experimentalPeakHealthySessions-observation.completedBeforeGate,
		observation.releasedAt.Round(time.Microsecond),
		observation.firstReadAt.Round(time.Microsecond),
		observation.lastReadAt.Round(time.Microsecond),
		observation.readWindow.Round(time.Microsecond),
		observation.elapsed.Round(time.Microsecond),
		mutationFrames,
	)
	return observation, nil
}

func collectExperimentalPeakReads(
	results <-chan experimentalPeakReadObservation,
	want int,
	deadline time.Duration,
) ([]experimentalPeakReadObservation, error) {
	collected := make([]experimentalPeakReadObservation, 0, want)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(collected) < want {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-timer.C:
			return collected, fmt.Errorf("synthetic readers completed %d/%d before deadline", len(collected), want)
		}
	}
	return collected, nil
}

func testExperimentalPeakRepeatedSearchUpdatesStayBounded(t *testing.T) {
	enableAuctionMutationExperiment(t, true)
	_, s, session, conn := auctionReadFixture(t)
	clock := &experimentalPeakNoWorkerClock{}
	s.gameplayTimers = clock
	session.connID = "experimental-peak-repeated-updates"
	body := auctionSearchItemsBody(6001)

	initialCapacity := 0
	for update := 0; update < experimentalPeakRepeatedUpdates; update++ {
		conn.write.Reset()
		if err := s.handleCurrentAuctionCommand(session, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), body); err != nil {
			t.Fatalf("update %d: %v", update+1, err)
		}
		packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
		if len(rest) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) ||
			len(packet.Body) == 0 || packet.Body[0] != 1 {
			t.Fatalf("update %d response header=%+v body=%x rest=%x", update+1, packet.Header, packet.Body, rest)
		}
		if !bytes.Equal(session.auctionLastSearchBody, body) {
			t.Fatalf("update %d retained body=%x want=%x", update+1, session.auctionLastSearchBody, body)
		}
		if update == 0 {
			initialCapacity = cap(session.auctionLastSearchBody)
		} else if cap(session.auctionLastSearchBody) > initialCapacity {
			t.Fatalf("update %d grew retained capacity from %d to %d", update+1, initialCapacity, cap(session.auctionLastSearchBody))
		}
	}
	if s.auctionMaintenanceContext != nil {
		t.Fatal("repeated updates started an auction maintenance worker")
	}
	if scheduled, started := clock.counts(); scheduled != 0 || started != 0 {
		t.Fatalf("repeated updates scheduled=%d started=%d timer workers", scheduled, started)
	}
	t.Logf(
		"synthetic_in_memory repeated_updates=%d retained_body_bytes=%d retained_capacity=%d maintenance_workers=0",
		experimentalPeakRepeatedUpdates,
		len(session.auctionLastSearchBody),
		cap(session.auctionLastSearchBody),
	)
}
