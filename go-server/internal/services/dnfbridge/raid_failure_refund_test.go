package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Existing public deadline fixtures now include the normal client movie
// completion before expecting typed24. No clock, clear or return bit is forged.
func completeRaidFailureMovieForDeadlineAudit(t *testing.T, f *raidStartGuardFixture, p *gameSession, r *runtimeDungeonState) {
	t.Helper()
	s := f.service
	until := time.Now().Add(createWriteTimeout)
	ready := false
	for time.Now().Before(until) {
		p.raidProjection.mu.Lock()
		ready = p.raidProjection.resultSent && !p.raidProjection.failed && !p.raidProjection.failureResultAt.IsZero()
		p.raidProjection.mu.Unlock()
		s.raidMu.Lock()
		queued := s.currentRaidDeadlineQueued[r]
		s.raidMu.Unlock()
		if ready && !queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("failure result did not reach the movie owner")
	}
	deadlineAuditV10Event(t, s, p, func() error {
		p.dungeon.mu.Lock()
		early := r.townReturnOp24Sent
		p.dungeon.mu.Unlock()
		if early {
			t.Error("typed24 replaced the failure movie before completion")
		}
		frame, err := dnfproto.BuildChannelPacket(659, []byte{1}, 1, 1)
		if err != nil {
			return err
		}
		return s.handleGameUpper(p, frame)
	})
	s.queueCurrentRaidDeadlineReturns(s.currentRaidFailurePresentationNow())
}

func TestRaidEntryDebitRollsBackWithTicketCost(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	ctx := context.Background()
	bag, _, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := f.repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	bag = repo.CloneInventory(bag)
	character = repo.CloneCharacter(character)
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	d := &raid.EntryDebit{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, ItemID: raidStartGuardTestMaterial, Count: 1, RefundItemID: raidStartGuardTestMaterial, RefundCount: 1, PaidAt: time.Now()}
	rule := raid.AdmissionRule{ItemID: d.ItemID, Count: d.Count}
	if _, err = f.service.consumeRaidEntryCostAndReceipt(ctx, []string{"19"}, "19", "account-1", rule, nil, d); err == nil {
		t.Fatal("injected admission commit failure accepted")
	}
	afterBag, _, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(afterBag, bag) {
		t.Fatal("failed admission retained ticket debit", err, afterBag)
	}
	afterCharacter, _, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(afterCharacter, character) {
		t.Fatal("failed admission retained paid receipt", err, afterCharacter)
	}
	f.repos.CharacterTrade = original
	if _, err = f.service.consumeRaidEntryCostAndReceipt(ctx, []string{"19"}, "19", "account-1", rule, nil, d); err != nil {
		t.Fatal(err)
	}
	paid, _, _ := f.repos.Character.Load(ctx, "19")
	status, err := raid.QueryEntryRefund(paid, *d)
	if err != nil || !status.Paid || status.Refunded {
		t.Fatal("successful admission missing debit receipt", status, err)
	}
	paidBag, _, _ := f.repos.Inventory.Load(ctx, "19")
	if paidBag.Slots["0:50"].Count != bag.Slots["0:50"].Count-1 {
		t.Fatal("successful admission did not debit exactly once")
	}
}

func TestRealPVFRaidFailurePaidMailAndResultLifecycle(t *testing.T) {
	for _, name := range []string{"anton_phase0", "anton_phase1", "luke", "fiendwar"} {
		t.Run(name, func(t *testing.T) {
			var f *raidStartGuardFixture
			var p *gameSession
			switch name {
			case "anton_phase0":
				f, p, _ = realAntonProjectionFixture(t, 0)
			case "anton_phase1":
				f, p, _ = realAntonProjectionFixture(t, 1)
			case "luke":
				f, p, _ = normalLukeFixture(t)
			case "fiendwar":
				var peers []*gameSession
				f, peers = actualCurrentFiendWar(t, 2)
				p = peers[0]
				f.state.Started = true
			}
			b := f.state.Battle
			if b.Mode != 3 {
				rule, err := f.service.currentRaidAdmissionRule(p)
				if err != nil {
					t.Fatal(err)
				}
				d := &raid.EntryDebit{InstanceID: b.InstanceID, Mode: b.Mode, ItemID: rule.ItemID, Count: rule.Count, RefundItemID: rule.ItemID, RefundCount: rule.Count, PaidAt: b.StartedAt}
				ids := []string{}
				for id := range b.MemberAccounts {
					idText := strconv.Itoa(int(id))
					ids = append(ids, idText)
					bag, _, err := f.repos.Inventory.Load(context.Background(), idText)
					if err != nil {
						t.Fatal(err)
					}
					bag.Slots = map[string]repo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count + 2}}
					if err = f.repos.Inventory.Save(context.Background(), bag); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = f.service.consumeRaidEntryCostAndReceipt(context.Background(), ids, "19", p.accountID, rule, nil, d); err != nil {
					t.Fatal(err)
				}
				b.EntryDebit = d
			} else {
				if err := f.service.consumeCurrentFiendWarEntry(p, *f.state, b); err != nil {
					t.Fatal(err)
				}
			}
			at := b.Deadline
			if b.Mode != 3 {
				b.Deadline = b.EnterAfter.Add(time.Duration(b.Configuration.PhaseLimitSeconds[b.Phase]) * time.Second)
				at = b.Deadline
			}
			states, errs := f.service.advanceCurrentRaidClocks(at)
			if len(errs) != 0 || len(states) != 1 || !b.TimedOut || b.PhaseCompleted {
				t.Fatal("failure reducer", errs, len(states))
			}
			if b.Mode != 3 {
				if err := f.service.sendCurrentAntonTerminalResult(p, states[0]); err != nil {
					t.Fatal(err)
				}
				assertAntonResultStream(t, f.conn.write.Bytes(), 1, true)
			}
			// Leaving the lobby/going offline cannot remove a frozen paid recipient.
			f.state.Members = nil
			if _, errs = f.service.advanceCurrentAntonRewardFlows(at, func(int64) (int64, error) { t.Fatal("failure rolled clear rewards"); return 0, nil }); len(errs) != 0 {
				t.Fatal(errs)
			}
			for id := range b.MemberAccounts {
				idText := strconv.Itoa(int(id))
				mail, _, err := f.repos.Mailbox.Load(context.Background(), idText)
				if err != nil || len(mail.Mails) != 1 {
					t.Fatal("missing refund mail", id, mail, err)
				}
				var count int64
				for _, m := range mail.Mails {
					for _, item := range m.Attachments {
						if item.ItemID != int64(b.EntryDebit.RefundItemID) {
							t.Fatal("wrong refund item")
						}
						count += item.Count
					}
				}
				if count != b.EntryDebit.RefundCount {
					t.Fatal("wrong refund count", count)
				}
				c, _, _ := f.repos.Character.Load(context.Background(), idText)
				status, err := raid.QueryEntryRefund(c, *b.EntryDebit)
				if err != nil || !status.Refunded || status.MailID == "" {
					t.Fatal("unqueryable refund", status, err)
				}
			}
			if _, errs = f.service.advanceCurrentAntonRewardFlows(at.Add(time.Second), nil); len(errs) != 0 {
				t.Fatal(errs)
			}
			if b.RewardFlow[b.Phase] != nil || len(f.service.raidWeeklyIntents) != 0 {
				t.Fatal("failure produced clear entitlements")
			}
		})
	}
}

func TestRaidFailureWithoutDebitOrWithClearCannotFreezeRefund(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	b := &runtimeRaidBattle{Mode: 1, TimedOut: true, InstanceID: "00112233445566778899aabbccddeeff", MemberAccounts: map[uint16]string{19: "account-1"}}
	f.state.Battle = b
	if err := f.service.freezeCurrentRaidFailureRefundLocked(*f.state, time.Now()); err != nil || len(f.service.antonRewardFlows) != 0 {
		t.Fatal("unpaid historical battle minted ticket", err)
	}
	b.EntryDebit = &raid.EntryDebit{InstanceID: b.InstanceID, Mode: 1}
	b.PhaseCompleted = true
	if err := f.service.freezeCurrentRaidFailureRefundLocked(*f.state, time.Now()); err != nil || len(f.service.antonRewardFlows) != 0 {
		t.Fatal("successful clear got refund", err)
	}
}

func TestRealPVFRaidFailureOneRecipientFailureDoesNotBlockOthers(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 2)
	f.state.Started = true
	b := f.state.Battle
	if err := f.service.consumeCurrentFiendWarEntry(peers[0], *f.state, b); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	paid, _, err := f.repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	paid = repo.CloneCharacter(paid)
	changed := repo.CloneCharacter(paid)
	changed.AccountID = "foreign-owner"
	if err = f.repos.Character.Save(ctx, changed); err != nil {
		t.Fatal(err)
	}
	at := b.Deadline
	if states, errs := f.service.advanceCurrentRaidClocks(at); len(states) != 1 || len(errs) != 0 {
		t.Fatal(states, errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(at, nil); len(errs) != 1 {
		t.Fatal("wrong owner refund was not rejected", errs)
	}
	blocked, _, _ := f.repos.Mailbox.Load(ctx, "19")
	other, _, _ := f.repos.Mailbox.Load(ctx, "20")
	if len(blocked.Mails) != 0 || len(other.Mails) != 1 {
		t.Fatal("one rejected recipient blocked another paid member", blocked, other)
	}
	if err = f.repos.Character.Save(ctx, paid); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(at.Add(3*time.Second), nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"19", "20"} {
		mail, _, _ := f.repos.Mailbox.Load(ctx, id)
		if len(mail.Mails) != 1 {
			t.Fatal("recipient retry missing or duplicated refund", id, mail)
		}
	}
}

func TestRealPVFRaidFailureMovieCompletionDoesNotOpenRewards(t *testing.T) {
	f, p, at := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	b.TimedOut = true
	b.Deadline = b.EnterAfter.Add(time.Duration(b.Configuration.PhaseLimitSeconds[0]) * time.Second)
	b.AntonFlowConfig.MovieEndSeconds = [4]uint32{20, 20, 20, 20}
	q := newFakeCurrentDungeonDeathTimerQueue()
	at = b.Deadline
	q.now = at
	f.service.gameplayTimers = q
	if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if f.service.currentRaidFailurePresentationReady(p, *f.state) {
		t.Fatal("immediate return would replace failure movie")
	}
	q.now = at.Add(19 * time.Second)
	if f.service.currentRaidFailurePresentationReady(p, *f.state) {
		t.Fatal("movie window ended early")
	}
	q.now = at.Add(20 * time.Second)
	if !f.service.currentRaidFailurePresentationReady(p, *f.state) {
		t.Fatal("missing completion did not release configured timeout")
	}
	q.now = at.Add(time.Second)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, p, 659, []byte{0})
	r195MovieWantPromptWire(t, f.conn.write.Bytes(), true)
	if f.service.currentRaidFailurePresentationReady(p, *f.state) {
		t.Fatal("skip prompt was treated as completion")
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, p, 659, []byte{1})
	ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if ack.Header.MsgID != 659 || len(ack.Body) != 1 || ack.Body[0] != 1 {
		t.Fatal("missing failure movie completion ack")
	}
	stage, rest := splitGameServerUpperPacket(t, rest)
	if stage.Header.MsgID != 587 || len(stage.Body) != 2 || stage.Body[0] != 4 || stage.Body[1] != 1 || len(rest) != 0 {
		t.Fatal("missing failure-only result stage", stage)
	}
	if !f.service.currentRaidFailurePresentationReady(p, *f.state) || len(b.RewardCardReady[0]) != 0 || len(b.RewardCards[0]) != 0 || b.PhaseCompleted || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("failure movie completion created clear eligibility")
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, p, 659, []byte{1})
	_, rest = splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatal("duplicate movie completion reopened failure screen")
	}
}
