package wedding

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func cancellationFixture(t *testing.T) (Owner, PairRequest) {
	t.Helper()
	o, now := ownerFixture(t)
	o.Rules.EngagementCancellationGold = 500000
	req := pairRequest(now)
	if _, e := o.Engage(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		s, _ := Read(c)
		s.LovePoints, s.LastLoveDay, s.LastWeddingDay, s.CooldownUntil = 79, 20, 21, 100
		write(&c, s)
		if e := r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
		q := r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "active", ProgressValue: 3, Extra: map[string]string{"wedding_partner": "old"}}, 20738: {Status: "completed"}, 42: {Status: "active", ProgressValue: 7}}, Progress: map[int64]r.QuestState{20737: {Status: "active", ProgressValue: 3}, 20738: {Status: "completed"}, 43: {Status: "completed"}}}
		if e := o.Repositories.Quest.Save(context.Background(), q); e != nil {
			t.Fatal(e)
		}
	}
	req.Now = now.Add(time.Minute)
	return o, req
}

func TestWeddingCancellationClearsOnlyPairAndPreparationAndChargesActorOnce(t *testing.T) {
	o, req := cancellationFixture(t)
	ctx := context.Background()
	result, err := o.CancelEngagement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Actor.Stats["gold"] != 9500000 || result.Partner.Stats["gold"] != 10000000 {
		t.Fatal("wrong cancellation payer")
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		s, e := Read(c)
		if e != nil || s.Phase != Single || s.PartnerID != 0 || s.Ring != 0 || s.Kind != 0 || s.CeremonyAt != 0 || s.MarriedAt != 0 || s.RoomPoints != 0 {
			t.Fatalf("uncleared state %s: %+v %v", id, s, e)
		}
		if s.LovePoints != 79 || s.LastLoveDay != 20 || s.LastWeddingDay != 21 || s.CooldownUntil != 100 {
			t.Fatal("personal balance or limits changed")
		}
		q, _, _ := o.Repositories.Quest.Load(ctx, id)
		if len(q.States) != 1 || q.States[42].ProgressValue != 7 || len(q.Progress) != 1 || q.Progress[43].Status != "completed" {
			t.Fatalf("quest cleanup scope %+v", q)
		}
	}
	a, _, _ := o.Repositories.Character.Load(ctx, "1")
	b, _, _ := o.Repositories.Character.Load(ctx, "2")
	q, _, _ := o.Repositories.Quest.Load(ctx, "1")
	if _, err = o.CancelEngagement(ctx, req); !errors.Is(err, ErrState) {
		t.Fatalf("repeat cancellation: %v", err)
	}
	aa, _, _ := o.Repositories.Character.Load(ctx, "1")
	bb, _, _ := o.Repositories.Character.Load(ctx, "2")
	qq, _, _ := o.Repositories.Quest.Load(ctx, "1")
	if !reflect.DeepEqual(a, aa) || !reflect.DeepEqual(b, bb) || !reflect.DeepEqual(q, qq) {
		t.Fatal("repeat cancellation mutated a record")
	}
}

func TestWeddingCancellationRejectsInsufficientGoldWithoutMutation(t *testing.T) {
	o, req := cancellationFixture(t)
	ctx := context.Background()
	c, _, _ := o.Repositories.Character.Load(ctx, "1")
	c.Stats["gold"] = 499999
	if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	before, _, _ := o.Repositories.Character.Load(ctx, "1")
	quest, _, _ := o.Repositories.Quest.Load(ctx, "2")
	if _, err := o.CancelEngagement(ctx, req); !errors.Is(err, ErrGold) {
		t.Fatalf("insufficient balance: %v", err)
	}
	after, _, _ := o.Repositories.Character.Load(ctx, "1")
	afterQuest, _, _ := o.Repositories.Quest.Load(ctx, "2")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(quest, afterQuest) {
		t.Fatal("rejected cancellation mutated wallet or quest")
	}
	assertPair(t, o, Engaged, 0)
}

func TestWeddingCancellationRequiresCurrentActorAndEngagedReciprocalPair(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(Owner, *PairRequest)
	}{
		{"foreign-account", func(_ Owner, p *PairRequest) { p.Actor.AccountID = "other" }},
		{"wrong-partner", func(_ Owner, p *PairRequest) { p.Partner = Actor{"a3", "3"} }},
		{"invalid-session", func(_ Owner, p *PairRequest) { p.Revalidate = func() bool { return false } }},
		{"reserved", func(o Owner, p *PairRequest) {
			for _, id := range []string{"1", "2"} {
				c, _, _ := o.Repositories.Character.Load(context.Background(), id)
				s, _ := Read(c)
				s.Phase = Reserved
				s.CeremonyChannel = 10
				s.CeremonyArea = 8
				write(&c, s)
				_ = r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding)
			}
		}},
		{"unknown-phase3", func(o Owner, p *PairRequest) {
			for _, id := range []string{"1", "2"} {
				c, _, _ := o.Repositories.Character.Load(context.Background(), id)
				s, _ := Read(c)
				s.Phase = InCeremony
				s.CeremonyChannel = 10
				s.CeremonyArea = 8
				write(&c, s)
				_ = r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding)
			}
		}},
		{"married", func(o Owner, p *PairRequest) {
			for _, id := range []string{"1", "2"} {
				c, _, _ := o.Repositories.Character.Load(context.Background(), id)
				s, _ := Read(c)
				s.Phase = Married
				write(&c, s)
				_ = r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, req := cancellationFixture(t)
			tc.change(o, &req)
			before, _, _ := o.Repositories.Character.Load(context.Background(), "1")
			q, _, _ := o.Repositories.Quest.Load(context.Background(), "1")
			if _, e := o.CancelEngagement(context.Background(), req); e == nil {
				t.Fatal("invalid cancellation accepted")
			}
			after, _, _ := o.Repositories.Character.Load(context.Background(), "1")
			qq, _, _ := o.Repositories.Quest.Load(context.Background(), "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(q, qq) {
				t.Fatal("invalid cancellation mutated state")
			}
		})
	}
}

func TestWeddingCancellationFeeMatchesCurrentPVF(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("matching PVF not supplied")
	}
	archive, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	rules, e := Load(archive)
	if e != nil || rules.EngagementCancellationGold != 500000 {
		t.Fatalf("fee=%+v err=%v", rules, e)
	}
}
