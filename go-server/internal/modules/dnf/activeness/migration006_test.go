package activeness

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strings"
	"testing"
	"time"
)

func rewards006Catalog(t *testing.T) *Catalog {
	t.Helper()
	s := strings.Replace(config, "1 1 20 2 1 20 3 1 20 4 1 100 5 1 20 6 100 20 7 1 20 8 1 20", "1 1 10 2 1 10 3 1 50 4 1 100 5 1 10 6 100 20 7 1 10 8 1 10", 1)
	s = strings.Replace(s, "2 50 2500", "2 50 500", 1)
	s = strings.Replace(s, "3 100 3500", "3 100 5500", 1)
	s = strings.Replace(s, "1 30 500 990027401 1 0 0", "1 30 500 990027401 1 3330 500", 1)
	d, e := pvf.Parse(PVFPath, s)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ParseCatalog(d)
	if e != nil || c.Fingerprint != rewards006Fingerprint {
		t.Fatal(c, e)
	}
	return c
}

func TestActivityRewards006AllProgressAndClaimsPreserved(t *testing.T) {
	old := rules011Catalog(t)
	next := rewards006Catalog(t)
	_, _, cmd := fixture(t)
	checked := 0
	for done := 0; done < 256; done++ {
		for claimed := 0; claimed < 8; claimed++ {
			ch := repo.CharacterRecord{Stats: map[string]int64{"unrelated": 123}}
			if e := initState(&ch, old, cmd.Now); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 8; i++ {
				if done&(1<<i) != 0 {
					if _, e := Apply(&ch, old, uint16(i+1), 100, cmd.Now, cmd.Now); e != nil {
						t.Fatal(e)
					}
				}
			}
			if done&(1<<5) == 0 {
				ch.Stats[progressKey(6)] = 99
			}
			allowed := true
			for i, r := range old.Rewards {
				if claimed&(1<<i) != 0 && ch.Stats[prefix+"points"] < int64(r.Required) {
					allowed = false
				}
			}
			if !allowed {
				continue
			}
			ch.Stats[prefix+"claimed"] = int64(claimed)
			before := repo.CloneCharacter(ch)
			if e := initState(&ch, next, cmd.Now); e != nil {
				t.Fatal(done, claimed, e)
			}
			before.Stats[prefix+"fingerprint"] = next.Fingerprint
			if !reflect.DeepEqual(ch.Stats, before.Stats) {
				t.Fatal("earned progress/claims altered", done, claimed)
			}
			if e := initState(&ch, next, cmd.Now); e != nil {
				t.Fatal("repeat", e)
			}
			checked++
		}
	}
	if checked < 1000 {
		t.Fatal("insufficient coverage", checked)
	}
	t.Logf("validated %d legal progress/claim states", checked)
}

func TestActivityRewards006ClaimsMailWalletAndReplay(t *testing.T) {
	ctx := context.Background()
	old := rules011Catalog(t)
	next := rewards006Catalog(t)
	for _, claimedBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "unclaimed_gets_1500", true: "old_claim_no_duplicate_no_backfill"}[claimedBefore], func(t *testing.T) {
			g, _, cmd := fixture(t)
			event(t, g, old, cmd, 4, 1)
			cmd.ExpectedDay = Day(cmd.Now)
			cmd.ExpectedFingerprint = old.Fingerprint
			if claimedBefore {
				cmd.Claim = 1
				if _, e := Settle(ctx, g, old, cmd); e != nil {
					t.Fatal(e)
				}
			}
			cmd.Claim = 0
			st, e := Settle(ctx, g, next, cmd)
			if e != nil || st.Points != 100 {
				t.Fatal(st, e)
			}
			cmd.ExpectedFingerprint = next.Fingerprint
			for _, id := range []uint16{1, 2, 3, 1, 2, 3} {
				cmd.Claim = id
				if _, e := Settle(ctx, g, next, cmd); e != nil {
					t.Fatal(e)
				}
			}
			a, _, _ := g.Account.Load(ctx, "a")
			if cerashop.WalletBalance(a).HappyTokenCera != 6500 {
				t.Fatal("wallet changed")
			}
			box, _, _ := g.Mailbox.Load(ctx, "7")
			counts := map[int64]int64{}
			for _, m := range box.Mails {
				for _, a := range m.Attachments {
					counts[a.ItemID] += a.Count
				}
			}
			want := int64(1500)
			if claimedBefore {
				want = 1000
			}
			if len(box.Mails) != 3 || counts[3330] != want || counts[990027401] != 1 || counts[990027402] != 1 || counts[990027403] != 1 {
				t.Fatal(counts, len(box.Mails))
			}
			// The other character on the same account remains untouched.
			cmd.Claim = 0
			cmd.CharacterID = "8"
			st, e = Settle(ctx, g, next, cmd)
			if e != nil || st.Points != 0 || st.Claimed != 0 {
				t.Fatal("cross character leak", st, e)
			}
		})
	}
}

func TestActivityRewards006RejectsUnknownAndRollsBackFailure(t *testing.T) {
	ctx := context.Background()
	old := rules011Catalog(t)
	next := rewards006Catalog(t)
	g, _, cmd := fixture(t)
	event(t, g, old, cmd, 4, 1)
	before, _, _ := g.Character.Load(ctx, "7")
	cmd.Claim = 1
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = old.Fingerprint
	if _, e := Settle(ctx, g, next, cmd); !errors.Is(e, ErrStaleDay) {
		t.Fatal("stale packet accepted", e)
	}
	cmd.ExpectedFingerprint = next.Fingerprint
	cmd.Deliver = func(context.Context, repo.MailboxRepository, Reward, string) (string, error) {
		return "", errors.New("injected mailbox failure")
	}
	if _, e := Settle(ctx, g, next, cmd); e == nil {
		t.Fatal("failure accepted")
	}
	after, _, _ := g.Character.Load(ctx, "7")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed claim committed migration")
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if cerashop.WalletBalance(a).HappyTokenCera != 0 {
		t.Fatal("failed claim leaked money")
	}
	invalid := repo.CloneCharacter(before)
	invalid.Stats[prefix+"fingerprint"]++
	if e := initState(&invalid, next, cmd.Now); !errors.Is(e, ErrCatalogChanged) {
		t.Fatal("unknown fingerprint accepted", e)
	}
	invalid = repo.CloneCharacter(before)
	invalid.Stats[prefix+"points"]++
	if e := initState(&invalid, next, cmd.Now); e == nil {
		t.Fatal("corrupt old progress accepted")
	}
}

func TestActivityRewards006LegacyAndSixAMReset(t *testing.T) {
	g, legacy, cmd := fixture(t)
	next := rewards006Catalog(t)
	ctx := context.Background()
	for _, id := range []uint16{1, 2, 5} {
		event(t, g, legacy, cmd, id, 1)
	}
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = legacy.Fingerprint
	cmd.Claim = 2
	if _, e := Settle(ctx, g, legacy, cmd); e != nil {
		t.Fatal(e)
	}
	cmd.Claim = 0
	st, e := Settle(ctx, g, next, cmd)
	if e != nil || st.Points != 30 || st.Claimed != 2 {
		t.Fatal("historical legitimate claim", st, e)
	}
	ch, _, _ := g.Character.Load(ctx, "7")
	boundary := time.Date(2026, 10, 28, 6, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if e := initState(&ch, next, boundary.Add(-time.Second)); e != nil || ch.Stats[prefix+"points"] != 30 {
		t.Fatal("early reset", e)
	}
	if e := initState(&ch, next, boundary); e != nil || ch.Stats[prefix+"points"] != 0 || ch.Stats[prefix+"claimed"] != 0 || ch.Stats[prefix+"rules011_prior_claimed"] != 0 {
		t.Fatal("reset", e)
	}
}
