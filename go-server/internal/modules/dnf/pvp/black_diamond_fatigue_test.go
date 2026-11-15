package pvp

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	blackDiamondPVPAccountID   = "fixture-black-diamond-account"
	blackDiamondPVPCharacterID = "fixture-black-diamond-character"
)

func blackDiamondPVPFatigueFixture(t *testing.T, active bool, spent, remaining int64, eventID string) (repo.Group, time.Time, repo.ActivityEvent) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	g := memory.NewMemoryGroup()
	account := repo.AccountRecord{AccountID: blackDiamondPVPAccountID}
	if active {
		account.Metadata = map[string]string{
			premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}
	}
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}

	limit := int64(156)
	if active {
		limit = 188
	}
	character := repo.CharacterRecord{
		AccountID:   blackDiamondPVPAccountID,
		CharacterID: blackDiamondPVPCharacterID,
		Stats: map[string]int64{
			"fatigue":                                    remaining,
			"fatigue_limit":                              limit,
			"fatigue_used":                               limit - remaining,
			"fatigue_display_update":                     limit - remaining,
			fatigue.DayKey:                               premium.ServiceDayID(now),
			premium.BlackDiamondFatigueDayKey:            premium.ServiceDayID(now),
			premium.BlackDiamondFatigueSpentKey:          spent,
			premium.BlackDiamondFatigueCreditedTotalKey:  0,
			premium.BlackDiamondFatigueDeliveredTotalKey: 0,
		},
	}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if active && !premium.Active(account, premium.TypeBlackDiamond, now) {
		t.Fatal("fixture account is not an active type 17 Black Diamond account")
	}
	event := repo.ActivityEvent{
		ID:          eventID,
		AccountID:   blackDiamondPVPAccountID,
		CharacterID: blackDiamondPVPCharacterID,
		Metric:      repo.ActivityFatigue,
		Amount:      1,
		OccurredAt:  now,
		Source:      "arcade",
	}
	return g, now, event
}

func assertBlackDiamondPVPFatigue(
	t *testing.T,
	g repo.Group,
	wantRemaining, wantSpent, wantCredited, wantDelivered, wantActivity int64,
) repo.CharacterRecord {
	t.Helper()
	ctx := context.Background()
	character, found, err := g.Character.Load(ctx, blackDiamondPVPCharacterID)
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
	}
	if got := character.Stats["fatigue"]; got != wantRemaining {
		t.Fatalf("fatigue=%d want=%d", got, wantRemaining)
	}
	if got := character.Stats[premium.BlackDiamondFatigueSpentKey]; got != wantSpent {
		t.Fatalf("black diamond fatigue spent=%d want=%d", got, wantSpent)
	}
	if got := character.Stats[premium.BlackDiamondFatigueCreditedTotalKey]; got != wantCredited {
		t.Fatalf("black diamond fatigue credited=%d want=%d", got, wantCredited)
	}
	if got := character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != wantDelivered {
		t.Fatalf("black diamond fatigue delivered=%d want=%d", got, wantDelivered)
	}
	wantUsed := character.Stats["fatigue_limit"] - wantRemaining
	if got := character.Stats["fatigue_used"]; got != wantUsed {
		t.Fatalf("fatigue_used=%d want=%d", got, wantUsed)
	}
	if got := character.Stats["fatigue_display_update"]; got != wantUsed {
		t.Fatalf("fatigue_display_update=%d want=%d", got, wantUsed)
	}

	summary, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: blackDiamondPVPAccountID})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Metrics[repo.ActivityFatigue]; got != wantActivity {
		t.Fatalf("arcade activity=%d want=%d", got, wantActivity)
	}
	events, err := g.Activity.Events(ctx, repo.ActivityQuery{AccountID: blackDiamondPVPAccountID})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(events)) != wantActivity {
		t.Fatalf("arcade receipts=%d want=%d", len(events), wantActivity)
	}
	return character
}

func TestBlackDiamondPVPFatigueThresholdCreditAndActivityReceiptReplay(t *testing.T) {
	ctx := context.Background()
	g, now, event := blackDiamondPVPFatigueFixture(t, true, 149, 20, "arcade:black-diamond:threshold")
	cmd := ConsumeArcadeCommand{
		Activity:    &event,
		AccountID:   blackDiamondPVPAccountID,
		CharacterID: blackDiamondPVPCharacterID,
		Amount:      1,
		UpdatedAt:   now,
	}

	first, err := ConsumeArcadeFatigue(ctx, g, cmd)
	if err != nil || first.Before != 20 || first.After != 19 || first.Consumed != 1 {
		t.Fatalf("first arcade debit: result=%+v err=%v", first, err)
	}
	assertBlackDiamondPVPFatigue(t, g, 19, 150, 1, 0, 1)

	replay, err := ConsumeArcadeFatigue(ctx, g, cmd)
	if err != nil || replay.Before != 19 || replay.After != 19 || replay.Consumed != 0 {
		t.Fatalf("arcade receipt replay: result=%+v err=%v", replay, err)
	}
	assertBlackDiamondPVPFatigue(t, g, 19, 150, 1, 0, 1)
}

func TestBlackDiamondPVPFatigueFailuresDoNotCredit(t *testing.T) {
	t.Run("transaction failure rolls back debit activity and ledger", func(t *testing.T) {
		ctx := context.Background()
		g, now, event := blackDiamondPVPFatigueFixture(t, true, 149, 20, "arcade:black-diamond:rollback")
		g.RentalAssets = activityAbortRental{RentalAssetUnitOfWork: g.RentalAssets}
		cmd := ConsumeArcadeCommand{
			Activity:    &event,
			AccountID:   blackDiamondPVPAccountID,
			CharacterID: blackDiamondPVPCharacterID,
			Amount:      1,
			UpdatedAt:   now,
		}

		if _, err := ConsumeArcadeFatigue(ctx, g, cmd); err == nil {
			t.Fatal("injected rental transaction failure committed")
		}
		assertBlackDiamondPVPFatigue(t, g, 20, 149, 0, 0, 0)
	})

	t.Run("insufficient fatigue does not credit or record receipt", func(t *testing.T) {
		ctx := context.Background()
		g, now, event := blackDiamondPVPFatigueFixture(t, true, 149, 0, "arcade:black-diamond:insufficient")
		cmd := ConsumeArcadeCommand{
			Activity:    &event,
			AccountID:   blackDiamondPVPAccountID,
			CharacterID: blackDiamondPVPCharacterID,
			Amount:      1,
			UpdatedAt:   now,
		}

		if _, err := ConsumeArcadeFatigue(ctx, g, cmd); !errors.Is(err, ErrFatigueInsufficient) {
			t.Fatalf("insufficient fatigue error=%v", err)
		}
		assertBlackDiamondPVPFatigue(t, g, 0, 149, 0, 0, 0)
	})
}

func TestBlackDiamondPVPFatigueInactiveAccountDoesNotCredit(t *testing.T) {
	ctx := context.Background()
	g, now, event := blackDiamondPVPFatigueFixture(t, false, 149, 20, "arcade:black-diamond:inactive")
	cmd := ConsumeArcadeCommand{
		Activity:    &event,
		AccountID:   blackDiamondPVPAccountID,
		CharacterID: blackDiamondPVPCharacterID,
		Amount:      1,
		UpdatedAt:   now,
	}

	debit, err := ConsumeArcadeFatigue(ctx, g, cmd)
	if err != nil || debit.Before != 20 || debit.After != 19 || debit.Consumed != 1 {
		t.Fatalf("inactive-account arcade debit: result=%+v err=%v", debit, err)
	}
	character := assertBlackDiamondPVPFatigue(t, g, 19, 149, 0, 0, 1)
	if got := character.Stats["fatigue_limit"]; got != 156 {
		t.Fatalf("inactive account fatigue_limit=%d want=156", got)
	}
}
