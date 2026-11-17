package wedding

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestGenderPolicyEligibilityAndTransactionalBoundaries(t *testing.T) {
	for _, policy := range []string{"", GenderPolicyOppositeOnly, GenderPolicyDenyMaleMale, GenderPolicyUnrestricted, "invalid"} {
		for _, jobs := range [][2]string{{"0", "11"}, {"11", "0"}, {"11", "6"}, {"0", "2"}, {"unknown", "11"}, {"0", "unknown"}} {
			t.Run(policy+"/"+jobs[0]+"-"+jobs[1], func(t *testing.T) {
				o, now := ownerFixture(t)
				ctx := context.Background()
				a, _, _ := o.Repositories.Character.Load(ctx, "1")
				b, _, _ := o.Repositories.Character.Load(ctx, "2")
				a.Job = jobs[0]
				b.Job = jobs[1]
				if err := o.Repositories.Character.Save(ctx, a); err != nil {
					t.Fatal(err)
				}
				if err := o.Repositories.Character.Save(ctx, b); err != nil {
					t.Fatal(err)
				}
				af, ak := Female(a.Job)
				bf, bk := Female(b.Job)
				want := ak && bk && ((policy == "" || policy == GenderPolicyOppositeOnly) && af != bf || policy == GenderPolicyDenyMaleMale && (af || bf) || policy == GenderPolicyUnrestricted)
				o.Rules.GenderPolicy = policy
				if err := Eligible(a, b, o.Rules); (err == nil) != want {
					t.Fatalf("Eligible=%v want=%t", err, want)
				}
				req := pairRequest(now)
				_, err := o.Engage(ctx, req)
				if (err == nil) != want || !want && !errors.Is(err, ErrEligibility) {
					t.Fatalf("Engage=%v want=%t", err, want)
				}
				// Reserve rechecks the current policy even for an already engaged pair.
				if !want {
					o.Rules.GenderPolicy = GenderPolicyUnrestricted
					a.Job = "0"
					b.Job = "11"
					if err = o.Repositories.Character.Save(ctx, a); err != nil {
						t.Fatal(err)
					}
					if err = o.Repositories.Character.Save(ctx, b); err != nil {
						t.Fatal(err)
					}
					if _, err = o.Engage(ctx, req); err != nil {
						t.Fatal(err)
					}
					a, _, _ = o.Repositories.Character.Load(ctx, "1")
					b, _, _ = o.Repositories.Character.Load(ctx, "2")
					a.Job = jobs[0]
					b.Job = jobs[1]
					if err = o.Repositories.Character.Save(ctx, a); err != nil {
						t.Fatal(err)
					}
					if err = o.Repositories.Character.Save(ctx, b); err != nil {
						t.Fatal(err)
					}
				}
				o.Rules.GenderPolicy = policy
				for _, id := range []string{"1", "2"} {
					if err = o.Repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
						t.Fatal(err)
					}
				}
				_, err = o.Reserve(ctx, req, 0, 0, 20261101)
				if (err == nil) != want || !want && !errors.Is(err, ErrEligibility) {
					t.Fatalf("Reserve=%v want=%t", err, want)
				}
			})
		}
	}
}

func TestGenderPolicyParsingAndInvalidParticipants(t *testing.T) {
	for _, policy := range []string{"", GenderPolicyOppositeOnly, GenderPolicyDenyMaleMale, GenderPolicyUnrestricted} {
		parsed, err := ParseGenderPolicy(" " + policy + " ")
		if err != nil || parsed == "" {
			t.Fatalf("parse %q: %q %v", policy, parsed, err)
		}
	}
	if _, err := ParseGenderPolicy("enabled"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	o, _ := ownerFixture(t)
	a, _, _ := o.Repositories.Character.Load(context.Background(), "1")
	b, _, _ := o.Repositories.Character.Load(context.Background(), "2")
	if err := Eligible(a, b, nil); !errors.Is(err, ErrEligibility) {
		t.Fatal(err)
	}
	for _, change := range []func(*r.CharacterRecord){func(c *r.CharacterRecord) { c.AccountID = "" }, func(c *r.CharacterRecord) { c.AccountID = a.AccountID }, func(c *r.CharacterRecord) { c.Level = 1 }} {
		c := b
		change(&c)
		if err := Eligible(a, c, o.Rules); !errors.Is(err, ErrEligibility) {
			t.Fatal("invalid participant accepted", err)
		}
	}
}
