package wedding

import (
	"context"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestCeremonyAwardsConfiguredInitialRingOnBothCompletionPaths(t *testing.T) {
	for kind := byte(0); kind < 3; kind++ {
		for _, admitted := range []bool{false, true} {
			o, now := ownerFixture(t)
			req := pairRequest(now)
			ctx := context.Background()
			o.Rules.Ceremonies[kind] = Ceremony{Kind: kind, Ring: int(kind)}
			o.Rules.CeremonyGifts[kind] = Gift{ItemID: 10002629 + int64(kind), Count: 1}
			if _, err := o.Engage(ctx, req); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"1", "2"} {
				if err := o.Repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := o.Reserve(ctx, req, kind, 0, 20261101); err != nil {
				t.Fatal(err)
			}
			at := now.Unix() + 300
			if _, err := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330}); err != nil {
				t.Fatal(err)
			}
			other := req
			other.Actor, other.Partner = req.Partner, req.Actor
			if _, err := o.EnterChurch(ctx, other, at, CeremonyPosition{X: 685, Y: 360}); err != nil {
				t.Fatal(err)
			}
			req.Now = time.Unix(at, 0)
			req.Revalidate = func() bool { return true }
			var result PairResult
			var err error
			if admitted {
				if _, err = o.MarkCeremonyReady(ctx, req, at); err != nil {
					t.Fatal(err)
				}
				if _, err = o.BeginAdmittedCeremony(ctx, req, at); err != nil {
					t.Fatal(err)
				}
				req.Now = time.Unix(at+o.Rules.CeremonySeconds, 0)
				result, err = o.FinishAdmittedCeremony(ctx, req, at, ceremonyTestGrant)
			} else {
				result, err = o.StartCeremony(ctx, req, at, ceremonyTestGrant)
			}
			if err != nil {
				t.Fatal(err)
			}
			a, b, err := ValidatePair(result.Actor, result.Partner)
			if err != nil || a.Phase != Married || b.Phase != Married || a.Ring != int(kind) || b.Ring != int(kind) {
				t.Fatalf("kind=%d admitted=%v states=%+v/%+v err=%v", kind, admitted, a, b, err)
			}
		}
	}
}
