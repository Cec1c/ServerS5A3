package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// The worker itself is unbounded only in the sense that the test cannot join a
// goroutine it did not start. Wait on the production-owned running flag with a
// hard deadline, then wait for both session projections with another hard
// deadline. The wire buffer is read only while holding the same session wireMu
// used by every production write.
const r195ManualRewardClockWait = 10 * time.Second

func TestR195ManualRewardClockSendsBothKind3BeforeAutoSelect(t *testing.T) {
	for _, mode := range []string{"luke_phase1", "fiendwar"} {
		t.Run(mode, func(t *testing.T) {
			f, sessions := r195MovieStageFixture(t, mode)
			if len(sessions) != 2 {
				t.Fatalf("fixture owners=%d, want 2", len(sessions))
			}

			q := newFakeCurrentDungeonDeathTimerQueue()
			// Install the fake before either real 660 handler can queue any
			// unrelated gameplay work. No later write to this field is needed.
			f.service.gameplayTimers = q

			f.service.raidMu.Lock()
			b := f.state.Battle
			if b == nil || b.Phase > 1 {
				f.service.raidMu.Unlock()
				t.Fatal("fixture has no final reward phase")
			}
			flow := b.RewardFlow[b.Phase]
			if flow == nil || len(flow.Recipients) != 2 {
				f.service.raidMu.Unlock()
				t.Fatal("fixture has no two-owner reward flow")
			}
			if flow.Recipients[0].Group == flow.Recipients[1].Group {
				f.service.raidMu.Unlock()
				t.Fatal("fixture requires independently grouped owners")
			}
			flow.AutoSelectAt = time.Now().Add(2 * time.Minute)
			autoSelectAt := flow.AutoSelectAt
			completedAt := flow.CompletedAt
			f.service.raidMu.Unlock()

			// Container refresh reconciles [daily delete item] timestamps through
			// gameplayNow(). Start the fake queue on the same timeline as the frozen
			// raid before either owner's baseline refresh. Leaving the queue at its
			// generic July test date would persist a July ticket deadline, then the
			// later jump to completedAt would retroactively expire that valid ticket
			// during the post-kind3 refresh. Production keeps gameplayNow() and the
			// item acquisition on one wall clock; only this isolated clock needs it.
			q.mu.Lock()
			q.now = completedAt.Add(time.Second)
			q.mu.Unlock()

			before := map[uint16]map[int64]int64{}
			for _, session := range sessions {
				// Establish the same normalized inventory view as a logged-in
				// owner before measuring reward deltas. The shared fixture seeds
				// raw admission stacks and may retain phase-zero wire output.
				fixtureTotals := r195ManualRewardInventoryTotals(t, f, session)
				if err := f.service.sendSelectedCurrentContainerListsRefresh(session, "r195_reward_clock_fixture"); err != nil {
					t.Fatal(err)
				}
				session.wireMu.Lock()
				session.conn.(*bufferConn).write.Reset()
				session.wireMu.Unlock()
				id := session.selectedCharacterID
				before[id] = r195ManualRewardInventoryTotals(t, f, session)
				if !r195ManualRewardTotalsEqual(fixtureTotals, before[id]) {
					t.Fatalf("owner%d baseline fixture assets changed from %v to %v", id, fixtureTotals, before[id])
				}
			}

			// These are the actual 659 movie completion and 660 party-card
			// handlers. Each owner chooses card 0 from its own reward group.
			for _, session := range sessions {
				dispatchAntonCardRequest(t, f, session, 659, []byte{1})
				dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
			}

			f.service.raidMu.Lock()
			for _, owner := range flow.Recipients {
				card := flow.Party[owner.CharacterID]
				if card == nil || !card.Committed {
					f.service.raidMu.Unlock()
					t.Fatalf("owner%d manual party card was not committed", owner.CharacterID)
				}
			}
			if len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
				f.service.raidMu.Unlock()
				t.Fatal("squad was granted before the raid clock drove the flow")
			}
			f.service.raidMu.Unlock()
			projectionBefore := r195ManualRewardProjectionSignatures(sessions)

			// The raid callback reads q.Now(), while the production reward worker
			// legitimately uses wall time; AutoSelectAt remains two real minutes away.
			if !q.Now().Before(autoSelectAt) {
				t.Fatalf("clock instant %s is not before AutoSelectAt %s", q.Now(), autoSelectAt)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.service.startCurrentRaidClock(ctx)

			q.mu.Lock()
			task := q.active[currentRaidClockName]
			q.mu.Unlock()
			if task == nil {
				t.Fatal("startCurrentRaidClock did not schedule the real named clock")
			}
			if task.delay != time.Second {
				t.Fatalf("raid clock delay=%s, want 1s", task.delay)
			}

			q.fire(task, false)
			r195WaitManualRewardWorker(t, f.service)
			wire, observedAt := r195WaitManualRewardKind3(t, sessions)
			// NOTI600 is written before the production projection synchronously
			// rebuilds and reconciles both owners' containers. Its cached flow
			// signature is committed only after that refresh, so waiting for it
			// prevents the asset snapshot from racing a legitimate refresh.
			r195WaitManualRewardProjections(t, sessions, projectionBefore)
			if !observedAt.Before(autoSelectAt) {
				t.Fatalf("kind3 observed at %s, AutoSelectAt=%s", observedAt, autoSelectAt)
			}

			f.service.raidMu.Lock()
			for _, owner := range flow.Recipients {
				party, squad := flow.Party[owner.CharacterID], flow.Squad[owner.CharacterID]
				if party == nil || !party.Committed || squad == nil || !squad.Committed {
					f.service.raidMu.Unlock()
					t.Fatalf("owner%d party/squad commit incomplete", owner.CharacterID)
				}
			}
			if flow.SquadShownAt.IsZero() {
				f.service.raidMu.Unlock()
				t.Fatal("squad projection was not made eligible by the worker")
			}
			frozen := cloneCurrentAntonRewardFlow(flow)
			f.service.raidMu.Unlock()

			for _, session := range sessions {
				got := r195ReadFiendRewardWire(t, wire[session.selectedCharacterID])
				got = r195ManualRewardOnlyKind3(t, got)
				r195WantFiendRewardWire(t, got, frozen,
					map[byte][]uint16{3: {19, 20}}, "kind3")
			}

			// Exact deltas prove each frozen party/squad plan was persisted once.
			// This catches a second grant hidden by either the manual 660 display
			// or the later kind3 projection.
			for _, session := range sessions {
				id := session.selectedCharacterID
				after := r195ManualRewardInventoryTotals(t, f, session)
				delta := r195ManualRewardTotalsDelta(before[id], after)
				want := r195ManualRewardExpectedTotals(t, frozen, id)
				if !r195ManualRewardTotalsEqual(delta, want) {
					t.Fatalf("owner%d reward asset delta=%v, want exactly %v", id, delta, want)
				}
			}
		})
	}
}

func r195WaitManualRewardWorker(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(r195ManualRewardClockWait)
	for {
		s.raidMu.Lock()
		running := s.antonRewardWorkRunning
		s.raidMu.Unlock()
		if !running {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("Anton reward worker did not finish within its bounded test deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func r195WaitManualRewardKind3(t *testing.T, sessions []*gameSession) (map[uint16][]byte, time.Time) {
	t.Helper()
	deadline := time.Now().Add(r195ManualRewardClockWait)
	for {
		streams := make(map[uint16][]byte, len(sessions))
		ready := true
		for _, session := range sessions {
			stream := r195ManualRewardSessionWire(t, session)
			streams[session.selectedCharacterID] = stream
			got := r195ReadFiendRewardWire(t, stream)
			found := false
			for _, event := range got.sequence {
				if event == "kind3" {
					found = true
					break
				}
			}
			ready = ready && found
		}
		if ready {
			return streams, time.Now()
		}
		if !time.Now().Before(deadline) {
			t.Fatal("both owners did not receive kind3 before the bounded test deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func r195ManualRewardProjectionSignatures(sessions []*gameSession) map[uint16][32]byte {
	signatures := make(map[uint16][32]byte, len(sessions))
	for _, session := range sessions {
		session.raidProjection.mu.Lock()
		signatures[session.selectedCharacterID] = session.raidProjection.rewardFlowSignature
		session.raidProjection.mu.Unlock()
	}
	return signatures
}

func r195WaitManualRewardProjections(t *testing.T, sessions []*gameSession, before map[uint16][32]byte) {
	t.Helper()
	deadline := time.Now().Add(r195ManualRewardClockWait)
	for {
		ready := true
		for _, session := range sessions {
			id := session.selectedCharacterID
			session.raidProjection.mu.Lock()
			signature := session.raidProjection.rewardFlowSignature
			session.raidProjection.mu.Unlock()
			if signature == [32]byte{} || signature == before[id] {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("both owners' post-kind3 container projections did not finish before the bounded test deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func r195ManualRewardSessionWire(t *testing.T, session *gameSession) []byte {
	t.Helper()
	session.wireMu.Lock()
	conn, ok := session.conn.(*bufferConn)
	var stream []byte
	if ok {
		stream = append(stream, conn.write.Bytes()...)
	}
	session.wireMu.Unlock()
	if !ok {
		t.Fatalf("owner%d session uses unexpected wire connection %T", session.selectedCharacterID, session.conn)
	}
	return stream
}

func r195ManualRewardOnlyKind3(t *testing.T, got r195FiendRewardWire) r195FiendRewardWire {
	t.Helper()
	kind3Packets := 0
	for _, event := range got.sequence {
		if event == "kind3" {
			kind3Packets++
		}
	}
	if kind3Packets != 1 {
		t.Fatalf("kind3 packets=%d, want exactly one; sequence=%v", kind3Packets, got.sequence)
	}
	out := r195FiendRewardWire{
		sequence: []string{"kind3"},
		owners:   map[byte][]uint16{3: append([]uint16(nil), got.owners[3]...)},
		items:    map[r195FiendRewardDescriptor]int{},
	}
	for item, count := range got.items {
		if item.kind == 3 {
			out.items[item] = count
		}
	}
	return out
}

func r195ManualRewardInventoryTotals(t *testing.T, f *raidStartGuardFixture, session *gameSession) map[int64]int64 {
	t.Helper()
	id := session.selectedCharacterID
	inventory, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
	if err != nil || !found {
		t.Fatalf("load owner%d inventory found=%v err=%v", id, found, err)
	}
	totals := map[int64]int64{}
	for _, stack := range inventory.Slots {
		if stack.ItemID != 0 && stack.Count != 0 {
			totals[stack.ItemID] += stack.Count
		}
	}
	for _, stack := range inventory.Warehouse {
		if stack.ItemID != 0 && stack.Count != 0 {
			totals[stack.ItemID] += stack.Count
		}
	}
	// Rewards may legitimately take the account-shared or mail fallback when
	// the character container cannot place them. Count every durable carrier so
	// the once-only assertion is independent of destination selection.
	accountID := f.service.accountIDForSession(session)
	if accountID == "" {
		t.Fatalf("owner%d has no account inventory owner", id)
	}
	account, accountFound, err := f.repos.AccountInventory.Load(context.Background(), accountID)
	if err != nil {
		t.Fatalf("load owner%d account inventory err=%v", id, err)
	}
	if accountFound {
		for _, stack := range account.Slots {
			if stack.ItemID != 0 && stack.Count != 0 {
				totals[stack.ItemID] += stack.Count
			}
		}
	}
	mailbox, mailboxFound, err := f.repos.Mailbox.Load(context.Background(), strconv.Itoa(int(id)))
	if err != nil {
		t.Fatalf("load owner%d mailbox err=%v", id, err)
	}
	if mailboxFound {
		for _, mail := range mailbox.Mails {
			for _, attachment := range mail.Attachments {
				if attachment.ItemID != 0 && attachment.Count != 0 {
					totals[attachment.ItemID] += attachment.Count
				}
			}
		}
	}
	return totals
}

func r195ManualRewardExpectedTotals(t *testing.T, flow *currentAntonRewardFlow, id uint16) map[int64]int64 {
	t.Helper()
	party, squad := flow.Party[id], flow.Squad[id]
	if party == nil || !party.Committed || squad == nil || !squad.Committed {
		t.Fatalf("owner%d has no committed party/squad plan", id)
	}
	totals := map[int64]int64{}
	for _, card := range []*currentAntonRewardCard{party, squad} {
		if len(card.Plan.Rewards) == 0 {
			t.Fatalf("owner%d committed reward plan is empty", id)
		}
		for _, reward := range card.Plan.Rewards {
			if reward.Count == 0 {
				t.Fatalf("owner%d has zero-count reward %d", id, reward.Definition.ItemID)
			}
			totals[int64(reward.Definition.ItemID)] += int64(reward.Count)
		}
	}
	return totals
}

func r195ManualRewardTotalsDelta(before, after map[int64]int64) map[int64]int64 {
	delta := map[int64]int64{}
	for item, count := range after {
		if count != before[item] {
			delta[item] = count - before[item]
		}
	}
	for item, count := range before {
		if after[item] == 0 && count != 0 {
			delta[item] = -count
		}
	}
	return delta
}

func r195ManualRewardTotalsEqual(a, b map[int64]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for item, count := range a {
		if b[item] != count {
			return false
		}
	}
	return true
}
