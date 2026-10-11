package onlineevent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The approved attendance rule is all four receipts, not just enough online
// seconds and a last-stage receipt. Exercise the real Owner and repositories.
func TestReviewDailyQualificationRequiresEveryDailyReceipt(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("status_import_%v", legacy), func(t *testing.T) {
			ctx := context.Background()
			repos := seededRepositories(t, ctx)
			owner := mustOwner(t, repos)
			d := testDefinition()
			d.Stages = nil
			thresholds := []uint64{1800, 3600, 7200, 10800}
			for n, seconds := range thresholds {
				d.Stages = append(d.Stages, Stage{ID: fmt.Sprintf("stage-%d", n+1), RequiredSeconds: seconds, Items: []ItemReward{{ItemID: int64(490006574 + n), Count: 1}}})
			}
			d.QualificationStageID = "stage-4"
			claimDef := d
			if legacy {
				claimDef.QualificationStageID = ""
			}
			at := time.Date(2026, 10, 9, 7, 0, 0, 0, d.calendar())
			if _, err := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: claimDef, IntervalFrom: at, IntervalTo: at.Add(3 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: claimDef, StageID: "stage-4", ClaimedAt: at.Add(3 * time.Hour), Allocate: cumulativeAllocator(nil)}); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err := owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, ObservedAt: at.Add(3*time.Hour + time.Second)}); err != nil {
					t.Fatal(err)
				}
			}
			account, _, err := repos.Account.Load(ctx, "account-1")
			if err != nil {
				t.Fatal(err)
			}
			daily, err := parseState(account.Metadata[metadataKey(d.ID)], d)
			if err != nil {
				t.Fatal(err)
			}
			if len(daily.Claims) != 1 {
				t.Fatalf("fixture must have only one genuine daily receipt: %+v", daily.Claims)
			}
			cd := CumulativeDefinition{Daily: d, Stages: []CumulativeStage{{ID: "sum-1", RequiredDays: 5, Items: []ItemReward{{ItemID: 490006578, Count: 1}}}}}
			status, err := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: cd, ObservedAt: at.Add(3*time.Hour + 2*time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			if len(status.QualifiedDates) != 0 {
				t.Fatalf("incomplete day qualified: only=%v dates=%v status_import=%v", daily.snapshot().ClaimedStageIDs, status.QualifiedDates, legacy)
			}
		})
	}
}
