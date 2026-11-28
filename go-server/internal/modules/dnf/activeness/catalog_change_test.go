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

func changedCatalog(t *testing.T, text string) *Catalog {
	t.Helper()
	d, err := pvf.Parse(PVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(d)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestActivityCatalogChangePreservesProgressClaimsAndRollback(t *testing.T) {
	ctx := context.Background()
	g, old, cmd := fixture(t)
	event(t, g, old, cmd, 4, 1)
	event(t, g, old, cmd, 6, 90)
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = old.Fingerprint
	cmd.Claim = 1
	if _, err := Settle(ctx, g, old, cmd); err != nil {
		t.Fatal(err)
	}
	// Target decreases, reward threshold increases, task points change.
	next := changedCatalog(t, strings.Replace(strings.Replace(strings.Replace(config, "6 100 20", "6 50 30", 1), "1 30 500", "1 40 700", 1), "4 1 100", "4 2 60", 1))
	cmd.Claim = 0
	if _, err := Settle(ctx, g, next, cmd); err != nil {
		t.Fatal("catalog upgrade rejected", err)
	}
	ch, _, _ := g.Character.Load(ctx, "7")
	if ch.Stats[progressKey(6)] != 90 || ch.Stats[progressKey(4)] != 1 || ch.Stats[prefix+"points"] != 130 || ch.Stats[prefix+"claimed"] != 1 {
		t.Fatal("lost progress/claims", ch.Stats)
	}
	for _, c := range []*Catalog{old, next, old, next} {
		event(t, g, c, cmd, 6, 1)
		event(t, g, c, cmd, 4, 1)
		cmd.ExpectedFingerprint = c.Fingerprint
		cmd.Claim = 1
		st, err := Settle(ctx, g, c, cmd)
		if err != nil || !st.Replayed {
			t.Fatal("claim replay", st, err)
		}
	}
	ch, _, _ = g.Character.Load(ctx, "7")
	a, _, _ := g.Account.Load(ctx, "a")
	box, _, _ := g.Mailbox.Load(ctx, "7")
	if ch.Stats[prefix+"points"] != 130 || ch.Stats[progressKey(6)] != 90 || ch.Stats[progressKey(4)] != 1 || len(box.Mails) != 1 || cerashop.WalletBalance(a).HappyTokenCera != 500 {
		t.Fatal("rollback duplicated/lost awards", ch.Stats, box, a)
	}
	st := view(ch, next)
	if st.Progress[3] != next.Tasks[3].Target || st.Progress[5] != next.Tasks[5].Target {
		t.Fatal("completed task projection", st)
	}
	untouched, _, _ := g.Character.Load(ctx, "8")
	if untouched.Stats[prefix+"fingerprint"] != 0 {
		t.Fatal("other character modified")
	}
}

func TestActivityCatalogChangePartialOnlyThenNewCompletion(t *testing.T) {
	g, old, cmd := fixture(t)
	event(t, g, old, cmd, 6, 40)
	next := changedCatalog(t, strings.Replace(config, "6 100 20", "6 80 30", 1))
	event(t, g, next, cmd, 6, 39)
	ch, _, _ := g.Character.Load(context.Background(), "7")
	if ch.Stats[progressKey(6)] != 79 || ch.Stats[prefix+"points"] != 0 {
		t.Fatal(ch.Stats)
	}
	event(t, g, next, cmd, 6, 1)
	event(t, g, old, cmd, 6, 1)
	ch, _, _ = g.Character.Load(context.Background(), "7")
	if ch.Stats[progressKey(6)] != 80 || ch.Stats[prefix+"points"] != 30 {
		t.Fatal(ch.Stats)
	}
}

func TestActivityCatalogChangeMixedPointsDoNotWrap(t *testing.T) {
	g, _, cmd := fixture(t)
	old := changedCatalog(t, strings.Replace(config, "1 1 20", "1 1 60000", 1))
	next := changedCatalog(t, strings.Replace(strings.Replace(config, "2 1 20", "2 1 60000", 1), "3 100 3500", "3 60000 3500", 1))
	event(t, g, old, cmd, 1, 1)
	event(t, g, next, cmd, 2, 1)
	ch, _, _ := g.Character.Load(context.Background(), "7")
	if ch.Stats[prefix+"points"] != 120000 {
		t.Fatal(ch.Stats)
	}
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = next.Fingerprint
	cmd.Claim = 3
	st, err := Settle(context.Background(), g, next, cmd)
	if err != nil || st.Points != 65535 || st.Claimed != 4 {
		t.Fatal("mixed points wrap/reject", st, err)
	}
}

func TestActivityCatalogChangeClaimFailureAtomicAndSixAMReset(t *testing.T) {
	ctx := context.Background()
	g, old, cmd := fixture(t)
	event(t, g, old, cmd, 4, 1)
	next := changedCatalog(t, strings.Replace(config, "1 30 500", "1 30 700", 1))
	before, _, _ := g.Character.Load(ctx, "7")
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = next.Fingerprint
	cmd.Claim = 1
	deliver := cmd.Deliver
	cmd.Deliver = func(context.Context, repo.MailboxRepository, Reward, string) (string, error) {
		return "", errors.New("mail unavailable")
	}
	if _, err := Settle(ctx, g, next, cmd); err == nil {
		t.Fatal("failed delivery accepted")
	}
	after, _, _ := g.Character.Load(ctx, "7")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial migration")
	}
	cmd.Deliver = deliver
	if _, err := Settle(ctx, g, next, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.ExpectedFingerprint = old.Fingerprint
	if _, err := Settle(ctx, g, next, cmd); !errors.Is(err, ErrStaleDay) {
		t.Fatal("stale UI claim", err)
	}
	cmd.Claim = 0
	cmd.Now = cmd.Now.Add(24 * time.Hour)
	st, err := Settle(ctx, g, next, cmd)
	if err != nil || st.Points != 0 || st.Claimed != 0 {
		t.Fatal("day reset", st, err)
	}
}

func TestActivityCatalogChangeUnknownLegacyFingerprint(t *testing.T) {
	g, c, cmd := fixture(t)
	event(t, g, c, cmd, 4, 1)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "7")
	ch.Stats[prefix+"fingerprint"] = c.Fingerprint ^ 1
	if ch.Stats[prefix+"fingerprint"] == 0 {
		ch.Stats[prefix+"fingerprint"] = 2
	}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	event(t, g, c, cmd, 6, 1)
	after, _, _ := g.Character.Load(ctx, "7")
	if after.Stats[prefix+"fingerprint"] != c.Fingerprint || after.Stats[prefix+"points"] != 100 || after.Stats[progressKey(6)] != 1 {
		t.Fatal(after.Stats)
	}
}

func TestActivityCatalogChangeKnownMigrationKeepsEarnedPoints(t *testing.T) {
	g, old, cmd := fixture(t)
	for _, id := range []uint16{1, 2, 5} {
		event(t, g, old, cmd, id, 1)
	}
	next := rules011Catalog(t)
	st, err := Settle(context.Background(), g, next, cmd)
	if err != nil || st.Points != 60 {
		t.Fatal("previously earned points reduced", st, err)
	}
}

func TestActivityCatalogChangeConcurrentClaimAndCarryValidation(t *testing.T) {
	ctx := context.Background()
	g, old, cmd := fixture(t)
	event(t, g, old, cmd, 4, 1)
	next := changedCatalog(t, strings.Replace(config, "1 30 500", "1 30 700", 1))
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = next.Fingerprint
	cmd.Claim = 1
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() { _, err := Settle(ctx, g, next, cmd); results <- err }()
	}
	for i := 0; i < 16; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	account, _, _ := g.Account.Load(ctx, "a")
	mail, _, _ := g.Mailbox.Load(ctx, "7")
	if cerashop.WalletBalance(account).HappyTokenCera != 700 || len(mail.Mails) != 1 {
		t.Fatal("concurrent claim duplicated")
	}
	saved, _, _ := g.Character.Load(ctx, "7")
	for _, key := range []string{carryVersionKey, carryPointsKey, carryDoneKey, carryClaimedKey, earnedKey(1), prefix + "points", prefix + "claimed", prefix + "done", progressKey(1)} {
		t.Run(key, func(t *testing.T) {
			corrupt := repo.CloneCharacter(saved)
			corrupt.Stats[key] = -1
			if err := initState(&corrupt, old, cmd.Now); !errors.Is(err, ErrState) {
				t.Fatal("accepted corrupt carried state", err)
			}
		})
	}
}

func TestActivityCatalogChangeSignalsPersistenceWhenTaskAlreadyCompletes(t *testing.T) {
	_, old, cmd := fixture(t)
	ch := repo.CharacterRecord{Stats: map[string]int64{}}
	if _, err := Apply(&ch, old, 6, 90, cmd.Now, cmd.Now); err != nil {
		t.Fatal(err)
	}
	next := changedCatalog(t, strings.Replace(config, "6 100 20", "6 50 30", 1))
	changed, err := Apply(&ch, next, 6, 1, cmd.Now, cmd.Now)
	if err != nil || !changed {
		t.Fatal("caller would skip saving migration", changed, err)
	}
}

func TestActivityCatalogChangeRejectsInvalidLegacyPointsAndKnownCarryClaim(t *testing.T) {
	_, old, cmd := fixture(t)
	ch := repo.CharacterRecord{Stats: map[string]int64{}}
	if _, err := Apply(&ch, old, 1, 1, cmd.Now, cmd.Now); err != nil {
		t.Fatal(err)
	}
	invalid := repo.CloneCharacter(ch)
	invalid.Stats[prefix+"fingerprint"]++
	invalid.Stats[prefix+"points"] = 0
	if err := initState(&invalid, old, cmd.Now); !errors.Is(err, ErrState) {
		t.Fatal("zero points for completed task", err)
	}
	unknown := *old
	unknown.Fingerprint++
	if err := initState(&ch, &unknown, cmd.Now); err != nil {
		t.Fatal(err)
	}
	if err := initState(&ch, old, cmd.Now); err != nil {
		t.Fatal(err)
	}
	ch.Stats[prefix+"claimed"] = 2 // 20 points cannot legitimately claim the 50-point stage.
	if err := initState(&ch, &unknown, cmd.Now); !errors.Is(err, ErrState) {
		t.Fatal("invalid known-source claim grandfathered", err)
	}
}

func TestActivityCatalogChangeDelayedEventPersistsOnlyMigration(t *testing.T) {
	_, old, cmd := fixture(t)
	ch := repo.CharacterRecord{Stats: map[string]int64{}}
	if _, err := Apply(&ch, old, 6, 90, cmd.Now, cmd.Now); err != nil {
		t.Fatal(err)
	}
	next := changedCatalog(t, strings.Replace(config, "6 100 20", "6 50 30", 1))
	changed, err := Apply(&ch, next, 1, 1, cmd.Now, cmd.Now.Add(-24*time.Hour))
	if err != nil || !changed || ch.Stats[progressKey(1)] != 0 || ch.Stats[progressKey(6)] != 90 || ch.Stats[prefix+"points"] != 30 {
		t.Fatal("delayed event migration", changed, err, ch.Stats)
	}
}

func TestActivityCatalogChangeRejectsImpossibleUnknownClaim(t *testing.T) {
	_, old, cmd := fixture(t)
	ch := repo.CharacterRecord{Stats: map[string]int64{}}
	if err := initState(&ch, old, cmd.Now); err != nil {
		t.Fatal(err)
	}
	ch.Stats[prefix+"fingerprint"]++
	ch.Stats[prefix+"claimed"] = 1
	if err := initState(&ch, old, cmd.Now); !errors.Is(err, ErrState) {
		t.Fatal("zero-point claim accepted", err)
	}
}
