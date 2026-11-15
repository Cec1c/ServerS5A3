package guild

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func blackDiamondGuildEntryFixture(t *testing.T, active bool, spent, remaining int64) (r.Group, DungeonEntryFatigueQuery, time.Time) {
	t.Helper()
	ctx := context.Background()
	repos, q, now := dungeonEntryFatigueFixture(t, 1)
	owner := q.Entries[0].Owner
	account := q.Accounts[owner.AccountID]
	if active {
		account.Metadata = map[string]string{
			premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}
	} else {
		account.Metadata = nil
	}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}

	limit := int64(156)
	if active {
		limit = 188
	}
	character, found, err := repos.Character.Load(ctx, owner.CharacterID)
	if err != nil || !found {
		t.Fatalf("load black diamond character: found=%v err=%v", found, err)
	}
	character.Stats["fatigue"] = remaining
	character.Stats["fatigue_limit"] = limit
	character.Stats["fatigue_used"] = limit - remaining
	character.Stats["fatigue_display_update"] = limit - remaining
	character.Stats[premium.BlackDiamondFatigueDayKey] = premium.ServiceDayID(now)
	character.Stats[premium.BlackDiamondFatigueSpentKey] = spent
	character.Stats[premium.BlackDiamondFatigueCreditedTotalKey] = 0
	character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey] = 0
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	q.Accounts[owner.AccountID] = account
	q.Cost = 1
	if active && !premium.Active(account, premium.TypeBlackDiamond, now) {
		t.Fatal("fixture account is not an active type 17 Black Diamond account")
	}
	return repos, q, now
}

func assertBlackDiamondGuildEntryFatigue(
	t *testing.T,
	repos r.Group,
	q DungeonEntryFatigueQuery,
	wantRemaining, wantSpent, wantCredited, wantDelivered int64,
	wantReceipt bool,
) r.CharacterRecord {
	t.Helper()
	assertDungeonEntryFatigue(t, repos, q, wantRemaining, wantReceipt)
	character, found, err := repos.Character.Load(context.Background(), q.Entries[0].Owner.CharacterID)
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
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
	return character
}

func TestBlackDiamondGuildEntryFatigueThresholdCreditAndReceiptReplay(t *testing.T) {
	ctx := context.Background()
	repos, q, now := blackDiamondGuildEntryFixture(t, true, 149, 20)

	first, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now)
	if err != nil || first.Replayed || len(first.Characters) != 1 {
		t.Fatalf("first entry: result=%+v err=%v", first, err)
	}
	assertBlackDiamondGuildEntryFatigue(t, repos, q, 19, 150, 1, 0, true)

	replay, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now)
	if err != nil || !replay.Replayed || len(replay.Characters) != 1 {
		t.Fatalf("receipt replay: result=%+v err=%v", replay, err)
	}
	assertBlackDiamondGuildEntryFatigue(t, repos, q, 19, 150, 1, 0, true)
}

func TestBlackDiamondGuildEntryFatigueFailuresDoNotCredit(t *testing.T) {
	t.Run("receipt save rolls back debit and ledger", func(t *testing.T) {
		ctx := context.Background()
		repos, q, now := blackDiamondGuildEntryFixture(t, true, 149, 20)
		unit := failedDungeonEntryUnit{GuildUnitOfWork: repos.Guild, failReceipt: true}

		if _, err := CommitDungeonEntryFatigue(ctx, unit, q, now); err == nil {
			t.Fatal("entry with failed receipt write committed")
		}
		assertBlackDiamondGuildEntryFatigue(t, repos, q, 20, 149, 0, 0, false)
	})

	t.Run("insufficient fatigue does not credit", func(t *testing.T) {
		ctx := context.Background()
		repos, q, now := blackDiamondGuildEntryFixture(t, true, 149, 0)

		if _, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now); !errors.Is(err, ErrDungeonEntryFatigue) {
			t.Fatalf("insufficient fatigue error=%v", err)
		}
		assertBlackDiamondGuildEntryFatigue(t, repos, q, 0, 149, 0, 0, false)
	})
}

func TestBlackDiamondGuildEntryFatigueInactiveAccountDoesNotCredit(t *testing.T) {
	ctx := context.Background()
	repos, q, now := blackDiamondGuildEntryFixture(t, false, 149, 20)

	if _, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now); err != nil {
		t.Fatal(err)
	}
	character := assertBlackDiamondGuildEntryFatigue(t, repos, q, 19, 149, 0, 0, true)
	if got := character.Stats["fatigue_limit"]; got != 156 {
		t.Fatalf("inactive account fatigue_limit=%d want=156", got)
	}
}
