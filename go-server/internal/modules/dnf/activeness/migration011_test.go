package activeness

import (
	"context"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

func rules011Catalog(t *testing.T) *Catalog {
	t.Helper()
	s := strings.Replace(config, "1 1 20 2 1 20 3 1 20 4 1 100 5 1 20 6 100 20 7 1 20 8 1 20", "1 1 10 2 1 10 3 1 50 4 1 100 5 1 10 6 100 20 7 1 10 8 1 10", 1)
	s = strings.Replace(s, "2 50 2500", "2 50 500", 1)
	s = strings.Replace(s, "3 100 3500", "3 100 5500", 1)
	d, e := pvf.Parse(PVFPath, s)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ParseCatalog(d)
	if e != nil || c.Fingerprint != rules011NewFingerprint {
		t.Fatalf("catalog %+v %v", c, e)
	}
	return c
}

func TestActivityRules011MigrationAllProgressAndClaimStates(t *testing.T) {
	_, old, cmd := fixture(t)
	next := rules011Catalog(t)
	if old.Fingerprint != rules011OldFingerprint {
		t.Fatal("baseline fingerprint changed")
	}
	for done := 0; done < 256; done++ {
		for claimed := 0; claimed < 8; claimed++ {
			ch := repo.CharacterRecord{Stats: map[string]int64{}}
			for i := 0; i < 8; i++ {
				if done&(1<<i) != 0 {
					if _, e := Apply(&ch, old, uint16(i+1), 100, cmd.Now, cmd.Now); e != nil {
						t.Fatal(e)
					}
				}
			}
			if done == 0 {
				if e := initState(&ch, old, cmd.Now); e != nil {
					t.Fatal(e)
				}
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
			if e := initState(&ch, next, cmd.Now); e != nil {
				t.Fatalf("done=%d claimed=%d: %v", done, claimed, e)
			}
			want := int64(0)
			for i, v := range next.Tasks {
				if done&(1<<i) != 0 {
					want += int64(v.Points)
				}
			}
			if ch.Stats[prefix+"points"] != want || ch.Stats[prefix+"claimed"] != int64(claimed) || ch.Stats[prefix+"done"] != int64(done) {
				t.Fatal("migration changed earned progress/claims")
			}
			if e := initState(&ch, next, cmd.Now); e != nil {
				t.Fatal("repeated read", e)
			}
			if e := initState(&ch, next, cmd.Now.Add(24*time.Hour)); e != nil || ch.Stats[prefix+"claimed"] != 0 || ch.Stats[prefix+"rules011_prior_claimed"] != 0 {
				t.Fatal("daily reset", e)
			}
		}
	}
}

func TestActivityRules011MigrationClaimsAndRollback(t *testing.T) {
	g, old, cmd := fixture(t)
	next := rules011Catalog(t)
	ctx := context.Background()
	for _, id := range []uint16{1, 2, 5} {
		event(t, g, old, cmd, id, 1)
	} // old 60, new 30
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = old.Fingerprint
	for _, stage := range []uint16{1, 2} {
		cmd.Claim = stage
		if _, e := Settle(ctx, g, old, cmd); e != nil {
			t.Fatal(e)
		}
	}
	cmd.Claim = 0
	fail := cmd
	fail.Validate = func() error { return ErrOwner }
	if _, e := Settle(ctx, g, next, fail); e == nil {
		t.Fatal("failed transaction accepted")
	}
	ch, _, _ := g.Character.Load(ctx, "7")
	if ch.Stats[prefix+"fingerprint"] != old.Fingerprint {
		t.Fatal("failed transaction migrated")
	}
	st, e := Settle(ctx, g, next, cmd)
	if e != nil || st.Points != 30 || st.Claimed != 3 {
		t.Fatalf("migration %+v %v", st, e)
	}
	cmd.ExpectedFingerprint = next.Fingerprint
	cmd.Claim = 2
	if st, e = Settle(ctx, g, next, cmd); e != nil || !st.Replayed {
		t.Fatalf("old claim replay %+v %v", st, e)
	}
	event(t, g, next, cmd, 4, 1)
	cmd.Claim = 3
	for i := 0; i < 2; i++ {
		if _, e = Settle(ctx, g, next, cmd); e != nil {
			t.Fatal(e)
		}
	}
	a, _, _ := g.Account.Load(ctx, "a")
	mail, _, _ := g.Mailbox.Load(ctx, "7")
	if cerashop.WalletBalance(a).HappyTokenCera != 8500 || len(mail.Mails) != 3 {
		t.Fatal("prior awards changed or duplicate new claim")
	}
	// Previously unclaimed/new characters receive exactly 500+500+5500.
	fresh, _, claim := fixture(t)
	event(t, fresh, next, claim, 4, 1)
	claim.ExpectedDay = Day(claim.Now)
	claim.ExpectedFingerprint = next.Fingerprint
	for _, stage := range []uint16{1, 2, 3} {
		claim.Claim = stage
		if _, e = Settle(ctx, fresh, next, claim); e != nil {
			t.Fatal(e)
		}
	}
	a, _, _ = fresh.Account.Load(ctx, "a")
	if cerashop.WalletBalance(a).HappyTokenCera != 6500 {
		t.Fatal("new rewards incorrect")
	}
}
