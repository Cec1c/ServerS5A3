package wedding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingCommentsGiftAndChurchBookSurviveMarriageAndSettlement(t *testing.T) {
	o, host, _, ledger := weddingCommentFixture(t)
	ctx := context.Background()
	rules := &GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50,
		Capacity: map[byte]int{0: 2}, Gifts: []GuestGiftTier{{Maximum: 400000000, ByKind: map[byte][]r.MailAttachment{0: {{ItemID: 10002622, Count: 1}}}}}}
	if _, err := o.GiveWeddingGold(ctx, rules, GuestGiftRequest{Guest: Actor{"a3", "3"}, Key: ledger.Key, Gold: 10000, Wish: "随礼祝福", Now: host.Now}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateGuestComment(ctx, host, "礼堂留言"); err != nil {
		t.Fatal(err)
	}
	before, err := o.LoadGuestComments(ctx, host)
	if err != nil || len(before) != 2 {
		t.Fatalf("church book %+v/%v", before, err)
	}
	for _, id := range []string{"1", "2", "3"} {
		box, _, err := o.Repositories.Mailbox.Load(ctx, id)
		if err != nil || len(box.Mails) != 0 {
			t.Fatalf("payment created premature mail for %s: %+v/%v", id, box, err)
		}
	}
	guestSetHostPhase(t, o, Married, ledger.At)
	req := pairRequest(time.Unix(ledger.At+60, 0))
	if _, err := o.FinalizeGuestWedding(ctx, req, ledger.At, rules); err != nil {
		t.Fatal(err)
	}
	host.Now = req.Now
	for range 2 {
		after, err := o.LoadGuestComments(ctx, host)
		if err != nil || len(after) != len(before) {
			t.Fatalf("married room book %+v/%v", after, err)
		}
		for i := range before {
			if after[i] != before[i] {
				t.Fatalf("married room lost author/time/text or duplicated rows: %+v / %+v", after, before)
			}
		}
	}
}

func weddingCommentFixture(t *testing.T) (Owner, CommentRequest, CommentRequest, GuestLedger) {
	t.Helper()
	o, req, rules, ledger := guestOwnerFixture(t)
	if _, err := o.InviteWeddingGuests(context.Background(), req, rules, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "3"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		c.Name = "author" + id
		if err := o.Repositories.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.EnterGuestChurch(context.Background(), Actor{"a3", "3"}, ledger.Key, req.Now, CeremonyPosition{X: 670, Y: 330}, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	host := CommentRequest{Actor: req.Actor, Key: ledger.Key, Now: req.Now, Revalidate: func() bool { return true }}
	guest := CommentRequest{Actor: Actor{"a3", "3"}, Key: ledger.Key, Now: req.Now, Revalidate: func() bool { return true }}
	return o, host, guest, ledger
}

func TestWeddingCommentsOwnedGuestUpsertHostDeleteAndLeavingRevokesAccess(t *testing.T) {
	o, host, guest, _ := weddingCommentFixture(t)
	ctx := context.Background()
	first, err := o.UpdateGuestComment(ctx, guest, "新婚快乐")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := o.UpdateGuestComment(ctx, guest, "百年好合")
	if err != nil || updated.ID != first.ID {
		t.Fatalf("upsert %+v/%v", updated, err)
	}
	if _, err := o.UpdateGuestComment(ctx, host, "感谢大家"); err != nil {
		t.Fatal(err)
	}
	rows, err := o.LoadGuestComments(ctx, guest)
	if err != nil || len(rows) != 2 || rows[0].Text != "百年好合" || rows[0].Name != "author3" {
		t.Fatalf("rows %+v/%v", rows, err)
	}
	if err := o.DeleteGuestComment(ctx, guest, first.ID); !errors.Is(err, ErrOwnership) {
		t.Fatalf("guest deleted host-managed row: %v", err)
	}
	if err := o.DeleteGuestComment(ctx, host, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.DeleteGuestComment(ctx, host, first.ID); err != nil {
		t.Fatal("delete replay", err)
	}
	rows, err = o.LoadGuestComments(ctx, host)
	if err != nil || len(rows) != 1 || rows[0].AuthorID != "1" {
		t.Fatalf("delete result %+v/%v", rows, err)
	}
	c, _, _ := o.Repositories.Character.Load(ctx, "3")
	c.Stats["area_id"] = 0
	if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadGuestComments(ctx, guest); !errors.Is(err, ErrOwnership) {
		t.Fatalf("left guest retained private book access %v", err)
	}
	bad := host
	bad.Actor.AccountID = "foreign"
	if _, err := o.LoadGuestComments(ctx, bad); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign host account %v", err)
	}
}

func TestWeddingCommentsConcurrentUpsertKeepsOneAuthorRowAndOtherSettings(t *testing.T) {
	o, host, guest, ledger := weddingCommentFixture(t)
	ctx := context.Background()
	scope, _ := r.WeddingPairScope(ledger.Hosts[:])
	record, _, _ := o.Repositories.Settings.Load(ctx, scope)
	record.Values["room"] = `{"version":1,"bag":{},"placed":{}}`
	if err := o.Repositories.Settings.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	beforeLedger := record.Values[guestLedgerKey(ledger.At)]
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, text := range []string{"one", "two"} {
		workers.Add(1)
		go func(text string) {
			defer workers.Done()
			<-start
			_, err := o.UpdateGuestComment(ctx, guest, text)
			results <- err
		}(text)
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := o.LoadGuestComments(ctx, host)
	if err != nil || len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("duplicate author rows %+v/%v", rows, err)
	}
	after, _, _ := o.Repositories.Settings.Load(ctx, scope)
	if after.Values["room"] != record.Values["room"] || after.Values[guestLedgerKey(ledger.At)] != beforeLedger {
		t.Fatal("comment update overwrote room or financial ledger")
	}
	bad := guest
	bad.Actor = Actor{"a4", "4"}
	if _, err := o.LoadGuestComments(ctx, bad); err == nil {
		t.Fatal("uninvited character read private book")
	}
}
