package testbenefits_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/mail"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/testbenefits"
)

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (repository.Group, testbenefits.Job, testbenefits.Recipient) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	require(t, g.Account.Save(ctx, repository.AccountRecord{AccountID: "test-account", Metadata: map[string]string{"unrelated": "keep"}}))
	c := repository.CharacterRecord{AccountID: "test-account", CharacterID: "1", Name: "fixture", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Stats: map[string]int64{"gold": 50}}
	require(t, g.Character.Save(ctx, c))
	j := testbenefits.Job{ID: strings.Repeat("a", 32), Gold: 1234, Title: "测试金币", Body: "领取测试"}
	r := testbenefits.Recipient{AccountID: c.AccountID, CharacterID: c.CharacterID, CreatedAt: c.CreatedAt}
	return g, j, r
}

func TestGoldMailConcurrentRetryAfterClaim(t *testing.T) {
	g, j, r := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := testbenefits.Deliver(ctx, g, j, r)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	m, _, err := g.Mailbox.Load(ctx, "1")
	require(t, err)
	if len(m.Mails) != 1 || m.Mails["1"].Gold != 1234 {
		t.Fatal(m)
	}
	a, _, err := g.Account.Load(ctx, r.AccountID)
	require(t, err)
	if a.Metadata["unrelated"] != "keep" {
		t.Fatal("unrelated metadata lost")
	}
	_, err = mail.NewOwner(g).Claim(ctx, "1", mail.ExtractRequest{MailIDs: []uint32{1}})
	require(t, err)
	c, _, err := g.Character.Load(ctx, "1")
	require(t, err)
	if c.Stats["gold"] != 1284 {
		t.Fatal(c.Stats)
	}
	// Remove even the original mail record: the separate receipt must survive.
	require(t, g.Mailbox.Save(ctx, repository.MailboxRecord{CharacterID: "1"}))
	id, err := testbenefits.Deliver(ctx, g, j, r)
	require(t, err)
	if id != "1" {
		t.Fatal(id)
	}
	m, _, err = g.Mailbox.Load(ctx, "1")
	require(t, err)
	if len(m.Mails) != 0 {
		t.Fatal("retry sent another mail")
	}
	c, _, err = g.Character.Load(ctx, "1")
	require(t, err)
	if c.Stats["gold"] != 1284 {
		t.Fatal("retry credited again")
	}
	j.Gold++
	if _, err = testbenefits.Deliver(ctx, g, j, r); err == nil {
		t.Fatal("changed content accepted")
	}
}

type failAccounts struct{ repository.AccountRepository }

func (f failAccounts) Save(context.Context, repository.AccountRecord) error {
	return errors.New("injected receipt failure")
}

type failingReceipt struct {
	repository.MailboxAssetUnitOfWork
	tx repository.AccountMailboxCharacterUnitOfWork
}

func (f failingReceipt) WithinAccountMailboxCharacter(ctx context.Context, a, c string, apply func(repository.AccountRepository, repository.CharacterRecord, repository.MailboxRepository) error) error {
	return f.tx.WithinAccountMailboxCharacter(ctx, a, c, func(ar repository.AccountRepository, cr repository.CharacterRecord, mr repository.MailboxRepository) error {
		return apply(failAccounts{ar}, cr, mr)
	})
}

func TestReceiptFailureRollsBackGoldMail(t *testing.T) {
	g, j, r := fixture(t)
	g.MailboxAssets = failingReceipt{g.MailboxAssets, g.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork)}
	if _, err := testbenefits.Deliver(context.Background(), g, j, r); err == nil {
		t.Fatal("expected failure")
	}
	m, _, err := g.Mailbox.Load(context.Background(), "1")
	require(t, err)
	if len(m.Mails) != 0 {
		t.Fatal("unreceipted mail committed")
	}
}

func TestGoldMailRejectsChangedRecipientAndInvalidAmounts(t *testing.T) {
	g, j, r := fixture(t)
	ctx := context.Background()
	r.CreatedAt = r.CreatedAt.Add(time.Second)
	if _, err := testbenefits.Deliver(ctx, g, j, r); err == nil {
		t.Fatal("recreated role accepted")
	}
	for _, amount := range []int64{-1, 0, testbenefits.MaxAmount + 1} {
		j.Gold = amount
		if _, err := testbenefits.Deliver(ctx, g, j, r); err == nil {
			t.Fatal(amount)
		}
	}
	for _, amount := range []int64{-1, testbenefits.MaxAmount + 1} {
		if err := (testbenefits.Policy{AccountCera: amount}).Validate(); err == nil {
			t.Fatal(amount)
		}
	}
	if err := (testbenefits.Policy{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWelcomeCeraOnlyOnceAndCharacterFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	creator := characterdata.NewCreator(g)
	require(t, g.Account.Save(ctx, repository.AccountRecord{AccountID: "new", Metadata: map[string]string{"account_cera": "25", "other": "keep"}}))
	create := func(id string, slot int, amount int64) error {
		return creator.Create(ctx, characterdata.Creation{
			Account: &repository.AccountRecord{AccountID: "new"}, WelcomeCera: amount,
			Character: repository.CharacterRecord{CharacterID: id, AccountID: "new", Slot: slot, Name: "test" + id, Stats: map[string]int64{"gold": 678}},
		})
	}
	// Collision must roll back the credit and receipt along with the character.
	require(t, g.Character.Save(ctx, repository.CharacterRecord{CharacterID: "1", AccountID: "other"}))
	if err := create("1", 0, 200); err == nil {
		t.Fatal("expected collision")
	}
	a, _, err := g.Account.Load(ctx, "new")
	require(t, err)
	if a.Metadata["account_cera"] != "25" || a.Metadata[testbenefits.WelcomeReceipt] != "" {
		t.Fatal(a)
	}
	require(t, create("2", 0, 200))
	require(t, create("3", 1, 200))
	a, _, err = g.Account.Load(ctx, "new")
	require(t, err)
	if a.Metadata["account_cera"] != "225" || a.Metadata["other"] != "keep" {
		t.Fatal(a)
	}
	for _, id := range []string{"2", "3"} {
		c, _, err := g.Character.Load(ctx, id)
		require(t, err)
		if c.Stats["gold"] != 678 {
			t.Fatal(c)
		}
	}
	// Zero default has no credit or receipt; invalid/overflow fails closed.
	old := repository.AccountRecord{}
	require(t, testbenefits.WelcomeCera(&old, 0))
	if len(old.Metadata) != 0 {
		t.Fatal(old)
	}
	old.Metadata = map[string]string{"account_cera": strconv.FormatInt(testbenefits.MaxAmount, 10)}
	if err := testbenefits.WelcomeCera(&old, 1); err == nil {
		t.Fatal("overflow accepted")
	}
}
