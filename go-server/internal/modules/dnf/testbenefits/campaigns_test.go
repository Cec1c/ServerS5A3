package testbenefits_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/mail"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/testbenefits"
)

func TestItemRuleReceiptsAcrossBatchesClaimAndRecreation(t *testing.T) {
	g, j, r := fixture(t)
	ctx := context.Background()
	j.Gold = 0
	j.CampaignID = strings.Repeat("b", 32)
	j.Attachments = []repository.MailAttachment{{ItemID: 490009735, Count: 1, Extra: map[string]string{"mailbox_pvf_kind": "stackable"}}}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		copy := j
		copy.ID = strings.Repeat(string(rune('a'+i%6)), 32)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := testbenefits.Deliver(ctx, g, copy, r)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	m, _, err := g.Mailbox.Load(ctx, r.CharacterID)
	require(t, err)
	if len(m.Mails) != 1 || m.Mails["1"].Attachments[0].ItemID != 490009735 {
		t.Fatal(m)
	}
	_, err = mail.NewOwner(g, func(id uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{Kind: "stackable", PVFPath: "stackable/fixture.stk", StackLimit: 99, SlotStart: 65, SlotEnd: 120}, nil
	}).Claim(ctx, r.CharacterID, mail.ExtractRequest{MailIDs: []uint32{1}})
	require(t, err)
	require(t, g.Mailbox.Save(ctx, repository.MailboxRecord{CharacterID: r.CharacterID}))
	_, err = testbenefits.Deliver(ctx, g, j, r)
	require(t, err)
	m, _, err = g.Mailbox.Load(ctx, r.CharacterID)
	require(t, err)
	if len(m.Mails) != 0 {
		t.Fatal("claimed/deleted mail redelivered")
	}
	changed := j
	changed.Attachments = []repository.MailAttachment{{ItemID: 490009736, Count: 1}}
	if _, err = testbenefits.Deliver(ctx, g, changed, r); err == nil {
		t.Fatal("changed reward accepted")
	}
	a, _, err := g.Account.Load(ctx, r.AccountID)
	require(t, err)
	if id, err := testbenefits.PreviousDelivery(a, j, r); err != nil || id != "1" {
		t.Fatal(id, err)
	}
	c, _, err := g.Character.Load(ctx, r.CharacterID)
	require(t, err)
	c.CreatedAt = c.CreatedAt.Add(time.Hour)
	require(t, g.Character.Save(ctx, c))
	r.CreatedAt = c.CreatedAt
	_, err = testbenefits.Deliver(ctx, g, j, r)
	require(t, err)
	m, _, err = g.Mailbox.Load(ctx, r.CharacterID)
	require(t, err)
	if len(m.Mails) != 1 {
		t.Fatal("recreated character not independently eligible")
	}
	j.CampaignID = strings.Repeat("c", 32)
	_, err = testbenefits.Deliver(ctx, g, j, r)
	require(t, err)
	m, _, err = g.Mailbox.Load(ctx, r.CharacterID)
	require(t, err)
	if len(m.Mails) != 2 {
		t.Fatal("new independent rule did not deliver")
	}
}

func TestItemRuleEligibilityAndRollback(t *testing.T) {
	g, j, r := fixture(t)
	ctx := context.Background()
	j.Gold = 0
	j.CampaignID = strings.Repeat("b", 32)
	j.Attachments = []repository.MailAttachment{{ItemID: 490009735, Count: 1}}
	c, _, err := g.Character.Load(ctx, r.CharacterID)
	require(t, err)
	rule := testbenefits.Campaign{Enabled: true, AutoNewCharacters: true, FirstEnabledAt: c.CreatedAt}
	if !testbenefits.EligibleNewCharacter(rule, c) {
		t.Fatal("new character rejected")
	}
	for _, bad := range []testbenefits.Campaign{
		{AutoNewCharacters: true, FirstEnabledAt: c.CreatedAt},
		{Enabled: true, FirstEnabledAt: c.CreatedAt},
		{Enabled: true, AutoNewCharacters: true},
		{Enabled: true, AutoNewCharacters: true, FirstEnabledAt: c.CreatedAt.Add(time.Second)},
	} {
		if testbenefits.EligibleNewCharacter(bad, c) {
			t.Fatal("ineligible character accepted")
		}
	}
	c.Stats["delete_flag"] = 1
	if testbenefits.EligibleNewCharacter(rule, c) {
		t.Fatal("deleted character eligible")
	}
	g.MailboxAssets = failingReceipt{g.MailboxAssets, g.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork)}
	if _, err := testbenefits.Deliver(ctx, g, j, r); err == nil {
		t.Fatal("expected receipt failure")
	}
	m, _, err := g.Mailbox.Load(ctx, r.CharacterID)
	require(t, err)
	if len(m.Mails) != 0 {
		t.Fatal("mail escaped rollback")
	}
	for _, a := range []repository.MailAttachment{{ItemID: 0, Count: 1}, {ItemID: 490009735, Count: 0}, {ItemID: 490009735, Count: testbenefits.MaxAmount + 1}} {
		j.Attachments = []repository.MailAttachment{a}
		if j.Validate() == nil {
			t.Fatal(a)
		}
	}
}
