package guild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func guardianPolicyFixture(fee GuardianFirstPurchasePolicy, selectFirst bool) GuardianPolicy {
	return GuardianPolicy{FirstPurchase: fee, InitialLevel: 1, InitialSkills: []uint8{1, 1, 1, 1, 1}, FirstSelect: &selectFirst}
}

func guardianTransactionSeed(t *testing.T, repos r.Group) (GuardianRequest, *GuardianCatalog, time.Time) {
	t.Helper()
	reg, g, owner, _ := textRegistry(t)
	g.Essence = EssenceBalance{AP: 100000, GP: 2000}
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	text := guardianCatalogFixture
	if path := os.Getenv("GUILD_ESSENCE_EXTRACTED_ETC"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text = string(b)
	}
	cat, err := ParseGuardianCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	q := GuardianRequest{Owner: owner, GuildID: g.ID, RequestID: fmt.Sprintf("%064x", 1), Action: GuardianPurchase, GuardianID: 1, Policy: guardianPolicyFixture(GuardianFreeOnce, true), Revalidate: func() bool { return true }, CanInteract: func(r.CharacterRecord, *Guild, bool) bool { return true }}
	return q, cat, now
}

func TestGuardianTransactionExplicitPolicy(t *testing.T) {
	valid := `{"first_purchase":"free_once","initial_level":1,"initial_skills":[1,1,1,1,1],"first_select":false}`
	if p, err := ParseGuardianPolicy(valid); err != nil || *p.FirstSelect {
		t.Fatal(p, err)
	}
	for _, text := range []string{"", `{}`, `null`, valid + `{}`, `{"first_purchase":"paid","initial_level":1,"initial_skills":[1,1,1,1,1]}`, `{"first_purchase":"paid","initial_level":1,"initial_skills":[1,1,1,1],"first_select":true}`, `{"first_purchase":"paid","initial_level":51,"initial_skills":[1,1,1,1,1],"first_select":true}`, `{"first_purchase":"paid","initial_level":1,"initial_skills":[1,1,1,1,6],"first_select":true}`, `{"first_purchase":"paid","initial_level":1,"initial_skills":[1,1,1,1,1],"first_select":true,"other":1}`} {
		if _, err := ParseGuardianPolicy(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestGuardianTransactionPoliciesSelectionAndReceiptRecovery(t *testing.T) {
	for _, fee := range []GuardianFirstPurchasePolicy{GuardianFreeOnce, GuardianPaid} {
		for _, first := range []uint32{1, 2} {
			for _, auto := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/%t", fee, first, auto), func(t *testing.T) {
					repos := guildTestGroup(t, 2)
					q, cat, now := guardianTransactionSeed(t, repos)
					ctx := context.Background()
					q.Policy = guardianPolicyFixture(fee, auto)
					q.GuardianID = first
					a, err := ApplyGuardian(ctx, repos.Guild, q, cat, now)
					wantCost := EssenceBalance{}
					if fee == GuardianPaid {
						wantCost = EssenceBalance{50000, 1000}
					}
					wantActive := uint32(0)
					if auto {
						wantActive = first
					}
					if err != nil || a.Cost != wantCost || a.ActiveID != wantActive || a.Guardian.Level != 1 || a.Guardian.Skills != [5]uint8{1, 1, 1, 1, 1} {
						t.Fatal(a, err)
					}
					// Same permanent request survives repository reload and repairs result.
					again, err := ApplyGuardian(ctx, repos.Guild, q, cat, now.Add(time.Hour))
					if err != nil || !reflect.DeepEqual(a, again) {
						t.Fatal(again, err)
					}
					q2 := q
					q2.GuardianID = 3 - first
					q2.RequestID = fmt.Sprintf("%064x", 2)
					b, err := ApplyGuardian(ctx, repos.Guild, q2, cat, now)
					if err != nil || b.Cost != (EssenceBalance{50000, 1000}) || b.ActiveID != wantActive {
						t.Fatal("second changed selection", b, err)
					}
					q3 := q2
					q3.Action = GuardianSelect
					q3.RequestID = fmt.Sprintf("%064x", 3)
					c, err := ApplyGuardian(ctx, repos.Guild, q3, cat, now)
					if err != nil || c.ActiveID != q2.GuardianID || c.Cost != (EssenceBalance{}) {
						t.Fatal(c, err)
					}
					q4 := q3
					q4.RequestID = fmt.Sprintf("%064x", 4)
					if _, err = ApplyGuardian(ctx, repos.Guild, q4, cat, now); err != nil {
						t.Fatal("same selection", err)
					}
					q5 := q4
					q5.GuardianID = first
					q5.RequestID = fmt.Sprintf("%064x", 5)
					if _, err = ApplyGuardian(ctx, repos.Guild, q5, cat, now); err != nil {
						t.Fatal(err)
					}
					// Old select receipt must not undo a later selection.
					if _, err = ApplyGuardian(ctx, repos.Guild, q3, cat, now); err != nil {
						t.Fatal(err)
					}
					reg, err := Load(ctx, repos.Settings)
					if err != nil || reg.Guilds[1].Guardians.ActiveID != first || len(reg.GuardianReceipts) != 5 || reg.Guilds[1].Essence != b.BalanceAfter {
						t.Fatal(reg, err)
					}
					q6 := q
					q6.RequestID = fmt.Sprintf("%064x", 6)
					if _, err = ApplyGuardian(ctx, repos.Guild, q6, cat, now); !errors.Is(err, ErrGuardianAlreadyOwned) {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestGuardianTransactionRejectsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"missing_policy", "unowned_select", "ap", "gp", "account", "member_account", "nonmember", "other_guild", "deputy_bit16", "scene", "session_before", "session_before_save", "session_after_save", "save_failure", "late_failure", "bad_identity", "bad_action", "bad_id", "zero_time", "nil_callback", "nil_uow"} {
		t.Run(mode, func(t *testing.T) {
			repos := guildTestGroup(t, 2)
			q, cat, now := guardianTransactionSeed(t, repos)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			uow := repos.Guild
			q.Policy.FirstPurchase = GuardianPaid
			switch mode {
			case "missing_policy":
				q.Policy = GuardianPolicy{}
			case "unowned_select":
				q.Action = GuardianSelect
			case "ap":
				reg.Guilds[1].Essence.AP = 49999
			case "gp":
				reg.Guilds[1].Essence.GP = 999
			case "account":
				q.Owner.AccountID = "foreign"
			case "member_account":
				m := reg.Guilds[1].Members["1"]
				m.AccountID = "foreign"
				reg.Guilds[1].Members["1"] = m
			case "nonmember":
				delete(reg.Guilds, 1)
			case "other_guild":
				q.GuildID = 2
			case "deputy_bit16":
				q.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
				m := reg.Guilds[1].Members["2"]
				m.Grade = 2
				reg.Guilds[1].Members["2"] = m
				reg.Guilds[1].GradePermissions = map[byte]uint32{2: 1 << 16}
			case "scene":
				q.CanInteract = func(r.CharacterRecord, *Guild, bool) bool { return false }
			case "session_before", "session_before_save", "session_after_save":
				calls := 0
				limit := map[string]int{"session_before": 1, "session_before_save": 2, "session_after_save": 3}[mode]
				q.Revalidate = func() bool { calls++; return calls < limit }
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{uow, errors.New("late commit failure")}
			case "bad_identity":
				q.RequestID = "bad"
			case "bad_action":
				q.Action = 0
			case "bad_id":
				q.GuardianID = 3
			case "zero_time":
				now = time.Time{}
			case "nil_callback":
				q.CanInteract = nil
			case "nil_uow":
				uow = nil
			}
			if err := Save(ctx, repos.Settings, reg, time.Now()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			result, err := ApplyGuardian(ctx, uow, q, cat, now)
			if err == nil || result.GuildID != 0 {
				t.Fatal(result, err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed request persisted")
			}
		})
	}
}

func guardianConcurrentPurchases(t *testing.T, repos r.Group, q GuardianRequest, cat *GuardianCatalog, now time.Time, mode string) {
	t.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	out := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			req := q
			if mode != "same_receipt" {
				req.RequestID = fmt.Sprintf("%064x", i+1)
			}
			if mode == "two_ids" {
				req.GuardianID = uint32(i%2 + 1)
			}
			_, err := ApplyGuardian(ctx, repos.Guild, req, cat, now)
			out <- err
		})
	}
	wg.Wait()
	close(out)
	success := 0
	for err := range out {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrGuardianAlreadyOwned) {
			t.Fatal(err)
		}
	}
	want := 1
	if mode == "same_receipt" {
		want = 12
	}
	if mode == "two_ids" {
		want = 2
	}
	if success != want {
		t.Fatal(success, want)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	count := 1
	balance := EssenceBalance{100000, 2000}
	if mode == "two_ids" {
		count = 2
		balance = EssenceBalance{50000, 1000}
	}
	if len(g.Guardians.Owned) != count || len(reg.GuardianReceipts) != count || g.Essence != balance {
		t.Fatal(g, len(reg.GuardianReceipts))
	}
}

func TestGuardianTransactionConcurrentSameAndDifferentIDs(t *testing.T) {
	for _, mode := range []string{"same_receipt", "same_id", "two_ids"} {
		t.Run(mode, func(t *testing.T) {
			repos := guildTestGroup(t, 2)
			q, cat, now := guardianTransactionSeed(t, repos)
			guardianConcurrentPurchases(t, repos, q, cat, now, mode)
		})
	}
}

func TestGuardianTransactionReceiptIdentityAndMembershipRechecked(t *testing.T) {
	for _, mode := range []string{"id", "action", "guild", "member_account", "session"} {
		t.Run(mode, func(t *testing.T) {
			repos := guildTestGroup(t, 2)
			q, cat, now := guardianTransactionSeed(t, repos)
			ctx := context.Background()
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "id":
				q.GuardianID = 2
			case "action":
				q.Action = GuardianSelect
			case "guild":
				q.GuildID = 2
			case "session":
				q.Revalidate = func() bool { return false }
			case "member_account":
				reg, _ := Load(ctx, repos.Settings)
				m := reg.Guilds[1].Members["1"]
				m.AccountID = "foreign"
				reg.Guilds[1].Members["1"] = m
				if err := Save(ctx, repos.Settings, reg, now); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); err == nil {
				t.Fatal("replay bypassed identity")
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid replay wrote")
			}
		})
	}
}
