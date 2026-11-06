package wedding

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func guestOwnerFixture(t *testing.T) (Owner, PairRequest, *GuestRules, GuestLedger) {
	t.Helper()
	o, now := ownerFixture(t)
	req := pairRequest(now)
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		partner := int64(1)
		if id == "1" {
			partner = 2
		}
		write(&c, State{PartnerID: partner, Phase: Reserved, CeremonyAt: now.Unix() + 300, CeremonyChannel: 10, CeremonyArea: 8})
		if err := r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	for i := 4; i <= 7; i++ {
		id := strconv.Itoa(i)
		if err := o.Repositories.Character.Save(context.Background(), r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Name: "guest" + id, Level: 30, Stats: map[string]int64{"gold": 10000000}}); err != nil {
			t.Fatal(err)
		}
	}
	rules := &GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50, Capacity: map[byte]int{0: 2}, Gifts: []GuestGiftTier{
		{Maximum: 99999, ByKind: map[byte][]r.MailAttachment{0: {{ItemID: 10002622, Count: 3}}}},
		{Maximum: 400000000, ByKind: map[byte][]r.MailAttachment{0: {{ItemID: 10002623, Count: 10}}}},
	}}
	ledger, err := o.EnsureGuestWedding(context.Background(), req, rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		if c.Stats["wedding_v1_relation_token"] != int64(ledger.Key) {
			t.Fatal("run/host token split")
		}
	}
	return o, req, rules, ledger
}

func guestPay(t *testing.T, o Owner, req PairRequest, rules *GuestRules, ledger GuestLedger, gold int64) {
	t.Helper()
	if _, err := o.InviteWeddingGuests(context.Background(), req, rules, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GiveWeddingGold(context.Background(), rules, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: gold, Wish: "新婚快乐", Now: req.Now}); err != nil {
		t.Fatal(err)
	}
}

func guestSetHostPhase(t *testing.T, o Owner, phase byte, at int64) {
	t.Helper()
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		state, err := Read(c)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase, state.CeremonyAt = phase, at
		if phase == Married {
			state.MarriedAt = at + 60
		}
		if phase == Single {
			state = State{Phase: Single}
		}
		write(&c, state)
		if err := r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWeddingGuestPerHostQuotaAndGlobalRecipientDedup(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	ctx := context.Background()
	first, err := o.InviteWeddingGuests(ctx, req, rules, []string{"3"})
	if err != nil || first.Remaining != 1 {
		t.Fatalf("first quota %+v/%v", first, err)
	}
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	second, err := o.InviteWeddingGuests(ctx, other, rules, []string{"4"})
	if err != nil || second.Remaining != 1 {
		t.Fatalf("partner quota %+v/%v", second, err)
	}
	if _, err := o.InviteWeddingGuests(ctx, other, rules, []string{"3"}); !errors.Is(err, GuestError(-5)) {
		t.Fatalf("cross-host duplicate %v", err)
	}
	if _, err := o.InviteWeddingGuests(ctx, req, rules, []string{"5"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.InviteWeddingGuests(ctx, req, rules, []string{"6"}); !errors.Is(err, GuestError(-4)) {
		t.Fatalf("over quota %v", err)
	}
	if _, err := o.InviteWeddingGuests(ctx, other, rules, []string{"7"}); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := o.ReadGuestLedger(ctx, ledger.Hosts[:], ledger.At)
	if len(stored.Invitations) != 4 || stored.RemainingFor("1") != 0 || stored.RemainingFor("2") != 0 {
		t.Fatalf("quotas interfered %+v", stored)
	}
}

func TestWeddingGuestConcurrentGiftDeductsOnceAndRejectsWrongOwner(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	ctx := context.Background()
	if _, err := o.InviteWeddingGuests(ctx, req, rules, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	request := GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: 10001, Now: req.Now}
	bad := request
	bad.Guest.AccountID = "wrong"
	if _, err := o.GiveWeddingGold(ctx, rules, bad); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign wallet %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := o.GiveWeddingGold(ctx, rules, request)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	success, duplicate := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, GuestError(-3)) {
			duplicate++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || duplicate != 1 {
		t.Fatalf("success/duplicate %d/%d", success, duplicate)
	}
	c, _, _ := o.Repositories.Character.Load(ctx, "3")
	stored, _, _ := o.ReadGuestLedger(ctx, ledger.Hosts[:], ledger.At)
	if c.Stats["gold"] != 10000000-10001 || stored.Invitations["3"].Gold != 10001 {
		t.Fatal("wallet and escrow diverged")
	}
}

func TestWeddingGuestSettlementFeeRemainderAndReplay(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	guestPay(t, o, req, rules, ledger, 10001)
	if _, err := o.FinalizeGuestWedding(context.Background(), req, ledger.At, rules); !errors.Is(err, ErrState) {
		t.Fatalf("premature settlement %v", err)
	}
	guestSetHostPhase(t, o, Married, ledger.At)
	req.Now = time.Unix(ledger.At+60, 0)
	result, err := o.FinalizeGuestWedding(context.Background(), req, ledger.At, rules)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ledger.FeeGold != 5000 || result.Ledger.HostGold != [2]int64{2500, 2500} || result.Ledger.RemainderGold != 1 {
		t.Fatalf("fee accounting %+v", result.Ledger)
	}
	if ref, err := o.ResolveGuestWedding(context.Background(), ledger.Key); err != nil || !ref.Closed {
		t.Fatalf("settlement did not close but preserve index mapping: %+v/%v", ref, err)
	}
	before := map[string]r.MailboxRecord{}
	for _, id := range []string{"1", "2", "3"} {
		mail, _, err := o.Repositories.Mailbox.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = mail
		if len(mail.Mails) != 1 {
			t.Fatalf("mail count %s %+v", id, mail)
		}
	}
	for _, id := range []string{"1", "2"} {
		for _, mail := range before[id].Mails {
			if mail.Gold != 2500 {
				t.Fatalf("host share %+v", mail)
			}
		}
	}
	for _, mail := range before["3"].Mails {
		if mail.Gold != 0 || len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 10002622 || mail.Attachments[0].Count != 3 {
			t.Fatalf("return gift %+v", mail)
		}
	}
	replay, err := o.FinalizeGuestWedding(context.Background(), req, ledger.At, rules)
	if err != nil || !replay.Replay {
		t.Fatalf("replay %+v/%v", replay, err)
	}
	for id, want := range before {
		got, _, _ := o.Repositories.Mailbox.Load(context.Background(), id)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("replay minted mail for %s", id)
		}
	}
	if _, err := o.RefundGuestWedding(context.Background(), req, ledger.At); !errors.Is(err, ErrState) {
		t.Fatalf("settled run refunded %v", err)
	}
}

func TestWeddingGuestRefundSurvivesCancelledRelationshipAndReplaysOnce(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	guestPay(t, o, req, rules, ledger, 10001)
	guestSetHostPhase(t, o, Single, 0)
	result, err := o.RefundGuestWedding(context.Background(), req, ledger.At)
	if err != nil || !result.Ledger.Refunded {
		t.Fatalf("refund %+v/%v", result, err)
	}
	if ref, err := o.ResolveGuestWedding(context.Background(), ledger.Key); err != nil || !ref.Closed {
		t.Fatalf("refund did not close but preserve index mapping: %+v/%v", ref, err)
	}
	mail, _, _ := o.Repositories.Mailbox.Load(context.Background(), "3")
	if len(mail.Mails) != 1 {
		t.Fatalf("refund mails %+v", mail)
	}
	for _, m := range mail.Mails {
		if m.Gold != 10001 || len(m.Attachments) != 0 {
			t.Fatalf("fee or gift applied to refund %+v", m)
		}
	}
	before := r.CloneMailbox(mail)
	if replay, err := o.RefundGuestWedding(context.Background(), req, ledger.At); err != nil || !replay.Replay {
		t.Fatalf("refund replay %+v/%v", replay, err)
	}
	mail, _, _ = o.Repositories.Mailbox.Load(context.Background(), "3")
	if !reflect.DeepEqual(mail, before) {
		t.Fatal("refund duplicated")
	}
}

type guestFailMailStore struct {
	r.MailboxRepository
	writes *int
}

func (m guestFailMailStore) Save(ctx context.Context, v r.MailboxRecord) error {
	*m.writes++
	if *m.writes == 2 {
		return errors.New("injected second mail failure")
	}
	return m.MailboxRepository.Save(ctx, v)
}

type guestFailMailUOW struct {
	r.CharacterTradeUnitOfWork
	base r.WeddingGuestLifecycleUnitOfWork
}

func (u guestFailMailUOW) WithinWeddingGuestLifecycle(ctx context.Context, hosts, guests []string, scope string, apply func(r.Group) error) error {
	return u.base.WithinWeddingGuestLifecycle(ctx, hosts, guests, scope, func(g r.Group) error { count := 0; g.Mailbox = guestFailMailStore{g.Mailbox, &count}; return apply(g) })
}

func TestWeddingGuestSettlementMailFailureRollsBackEntireLedgerAndAllMails(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	guestPay(t, o, req, rules, ledger, 10001)
	guestSetHostPhase(t, o, Married, ledger.At)
	before, _, _ := o.ReadGuestLedger(context.Background(), ledger.Hosts[:], ledger.At)
	req.Now = time.Unix(ledger.At+60, 0)
	base := o.Repositories.CharacterTrade
	o.Repositories.CharacterTrade = guestFailMailUOW{base, base.(r.WeddingGuestLifecycleUnitOfWork)}
	if _, err := o.FinalizeGuestWedding(context.Background(), req, ledger.At, rules); err == nil {
		t.Fatal("injected mail failure disappeared")
	}
	after, _, _ := o.ReadGuestLedger(context.Background(), ledger.Hosts[:], ledger.At)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial settlement receipt committed")
	}
	for _, id := range []string{"1", "2", "3"} {
		mail, _, _ := o.Repositories.Mailbox.Load(context.Background(), id)
		if len(mail.Mails) != 0 {
			t.Fatalf("partial mail committed to %s", id)
		}
	}
	o.Repositories.CharacterTrade = base
	if _, err := o.FinalizeGuestWedding(context.Background(), req, ledger.At, rules); err != nil {
		t.Fatal(err)
	}
}

func TestWeddingGuestMoneyBoundsAndHundredMillionMailChunks(t *testing.T) {
	o, req, rules, ledger := guestOwnerFixture(t)
	ctx := context.Background()
	if _, err := o.InviteWeddingGuests(ctx, req, rules, []string{"3", "4"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		gold int64
		code GuestError
	}{{9999, -6}, {400000001, -7}, {10000001, -8}} {
		if _, err := o.GiveWeddingGold(ctx, rules, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: row.gold, Now: req.Now}); !errors.Is(err, row.code) {
			t.Fatalf("gold %d error %v", row.gold, err)
		}
	}
	for _, id := range []string{"3", "4"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		c.Stats["gold"] = 400000000
		if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); err != nil {
			t.Fatal(err)
		}
		if _, err := o.GiveWeddingGold(ctx, rules, GuestGiftRequest{Guest: Actor{"a" + id, id}, Key: ledger.Key, Gold: 400000000, Now: req.Now}); err != nil {
			t.Fatal(err)
		}
	}
	guestSetHostPhase(t, o, Married, ledger.At)
	req.Now = time.Unix(ledger.At+60, 0)
	if _, err := o.FinalizeGuestWedding(ctx, req, ledger.At, rules); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		mail, _, _ := o.Repositories.Mailbox.Load(ctx, id)
		if len(mail.Mails) != 2 {
			t.Fatalf("mail chunk count %+v", mail)
		}
		var total int64
		for _, m := range mail.Mails {
			if m.Gold <= 0 || m.Gold > 100000000 {
				t.Fatal("mail exceeds documented gold cap")
			}
			total += m.Gold
		}
		if total != 200000000 {
			t.Fatalf("share %d", total)
		}
	}
}

func guestPrepareReservation(t *testing.T, o Owner, req PairRequest) {
	t.Helper()
	if _, err := o.Engage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := o.Repositories.Quest.Save(context.Background(), r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWeddingGuestReservationReceiptsRefundExactPayerGoldOrOriginalTicket(t *testing.T) {
	for _, kind := range []byte{0, 2} {
		t.Run(strconv.Itoa(int(kind)), func(t *testing.T) {
			o, now := ownerFixture(t)
			req := pairRequest(now)
			o.Rules.Ceremonies[2] = Ceremony{Kind: 2, TicketID: 2683401, TicketCount: 1}
			rules := &GuestRules{Capacity: map[byte]int{0: 10, 2: 30}}
			ticketExpiry := now.Add(time.Hour)
			if err := o.Repositories.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:3": {ItemID: 2683401, Count: 2, Bind: true, ExpireAt: ticketExpiry, Extra: map[string]string{"refund_test": "original-ticket"}}}}); err != nil {
				t.Fatal(err)
			}
			guestPrepareReservation(t, o, req)
			pair, err := o.ReserveWithGuests(context.Background(), req, kind, 0, 20261101, rules)
			if err != nil {
				t.Fatal(err)
			}
			state, _, _ := ValidatePair(pair.Actor, pair.Partner)
			ledger, found, err := o.ReadGuestLedger(context.Background(), []string{"1", "2"}, state.CeremonyAt)
			if err != nil || !found {
				t.Fatal(err)
			}
			if ledger.ReservationPayment.PayerID != "1" || state.RelationToken != int64(ledger.Key) {
				t.Fatalf("receipt/token %+v", ledger)
			}
			if kind == 0 && pair.Actor.Stats["gold"] != 9500000 {
				t.Fatal("actual gold was not debited")
			}
			if kind == 2 && pair.Inventories["1"].Slots["0:3"].Count != 1 {
				t.Fatal("actual ticket was not consumed")
			}
			guestSetHostPhase(t, o, Engaged, 0)
			refund, err := o.RefundGuestWedding(context.Background(), req, ledger.At)
			if err != nil || !refund.Ledger.ReservationPayment.Refunded {
				t.Fatalf("reservation refund %+v/%v", refund, err)
			}
			mail, _, _ := o.Repositories.Mailbox.Load(context.Background(), "1")
			if len(mail.Mails) != 1 {
				t.Fatalf("payer refund mails %+v", mail)
			}
			for _, m := range mail.Mails {
				if kind == 0 && m.Gold != 500000 {
					t.Fatalf("gold refund %+v", m)
				}
				if kind == 2 && (len(m.Attachments) != 1 || m.Attachments[0].ItemID != 2683401 || m.Attachments[0].Count != 1 || !m.Attachments[0].Bind || !m.Attachments[0].ExpireAt.Equal(ticketExpiry) || m.Attachments[0].Extra["refund_test"] != "original-ticket") {
					t.Fatalf("ticket refund lost original ownership/lifetime %+v", m)
				}
			}
			partner, _, _ := o.Repositories.Mailbox.Load(context.Background(), "2")
			if len(partner.Mails) != 0 {
				t.Fatal("refund guessed the other spouse as payer")
			}
			before := r.CloneMailbox(mail)
			if _, err := o.RefundGuestWedding(context.Background(), req, ledger.At); err != nil {
				t.Fatal(err)
			}
			mail, _, _ = o.Repositories.Mailbox.Load(context.Background(), "1")
			if !reflect.DeepEqual(before, mail) {
				t.Fatal("reservation refund duplicated")
			}
		})
	}
}

func TestWeddingGuestReservationAllocationFailureRollsBackCostsAndQuestDay(t *testing.T) {
	o, now := ownerFixture(t)
	req := pairRequest(now)
	guestPrepareReservation(t, o, req)
	record, index, err := loadGuestIndex(context.Background(), o.Repositories.Settings)
	if err != nil {
		t.Fatal(err)
	}
	index.Next = 0xffffffff
	raw, _ := json.Marshal(index)
	record.Values["index"] = string(raw)
	if err := o.Repositories.Settings.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReserveWithGuests(context.Background(), req, 0, 0, 20261101, &GuestRules{Capacity: map[byte]int{0: 10}}); err == nil {
		t.Fatal("overflow token allocation unexpectedly succeeded")
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		state, _ := Read(c)
		if state.Phase != Engaged || state.CeremonyAt != 0 || state.LastWeddingDay != 0 || c.Stats["gold"] != 10000000 {
			t.Fatalf("partial reservation/cost committed %+v gold=%d", state, c.Stats["gold"])
		}
	}
}
