package premium

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// BlackDiamondFatigueDayKey stores the 06:00 UTC+8 service-day ID. The
	// three ledger totals are independent so a day rollover can reset only the
	// current-day bucket without losing earned, not-yet-delivered coins.
	BlackDiamondFatigueDayKey                  = "black_diamond_fatigue_day_id"
	BlackDiamondFatigueSpentKey                = "black_diamond_fatigue_spent"
	BlackDiamondFatigueCreditedTotalKey        = "black_diamond_fatigue_credited_total"
	BlackDiamondFatigueDeliveredTotalKey       = "black_diamond_fatigue_delivered_total"
	BlackDiamondFatigueThreshold         int64 = 150
	BlackDiamondFatigueDailyCoins        int64 = 1
	BlackDiamondCoinItemID               int64 = 7454
)

var (
	ErrBlackDiamondFatigueInvalid = errors.New("black diamond fatigue accumulation is invalid")
	ErrBlackDiamondFatigueState   = errors.New("black diamond fatigue ledger is invalid")
)

// AccumulateBlackDiamondFatigue records one caller-verified fatigue debit for
// an active Black Diamond account. It must run under the caller's character
// and account transaction. A positive consumed value is the amount actually
// deducted; zero is a no-op. Replayed input cannot be identified here, so the
// caller must supply a durable receipt/high-water mark for the same debit.
//
// A service day credits at most one coin. Crossing the threshold once awards
// one coin even when one call crosses it by more than one point; later debits
// on that service day, including a total of 300 or more, never award again.
// The credited and delivered totals survive day rollover so pending coins are
// not lost when the account later expires.
func AccumulateBlackDiamondFatigue(character *dnfrepo.CharacterRecord, account dnfrepo.AccountRecord, consumed int64, now time.Time) error {
	if character == nil || now.IsZero() || strings.TrimSpace(character.CharacterID) == "" || strings.TrimSpace(character.AccountID) == "" {
		return ErrBlackDiamondFatigueInvalid
	}
	if strings.TrimSpace(account.AccountID) == "" || character.AccountID != account.AccountID {
		return fmt.Errorf("%w: character account does not match", ErrBlackDiamondFatigueInvalid)
	}
	if consumed < 0 {
		return fmt.Errorf("%w: consumed=%d", ErrBlackDiamondFatigueInvalid, consumed)
	}
	if consumed == 0 || !Active(account, TypeBlackDiamond, now) {
		return nil
	}

	stats := character.Stats
	credited, delivered, err := validBlackDiamondLedger(stats)
	if err != nil {
		return err
	}
	dayID := ServiceDayID(now)
	storedDay, hasDay := stats[BlackDiamondFatigueDayKey]
	if hasDay && storedDay < 0 {
		return fmt.Errorf("%w: day=%d", ErrBlackDiamondFatigueState, storedDay)
	}
	if hasDay && storedDay > dayID {
		return fmt.Errorf("%w: observed day %d precedes stored day %d", ErrBlackDiamondFatigueState, dayID, storedDay)
	}
	spent := int64(0)
	if storedSpent, ok := stats[BlackDiamondFatigueSpentKey]; ok {
		if storedSpent < 0 {
			return fmt.Errorf("%w: spent=%d", ErrBlackDiamondFatigueState, storedSpent)
		}
		if hasDay && storedDay == dayID {
			spent = storedSpent
		}
	}
	if consumed > math.MaxInt64-spent {
		return fmt.Errorf("%w: spent overflow", ErrBlackDiamondFatigueState)
	}
	nextSpent := spent + consumed
	nextCredited := credited
	if spent < BlackDiamondFatigueThreshold && nextSpent >= BlackDiamondFatigueThreshold {
		if credited == math.MaxInt64 {
			return fmt.Errorf("%w: credited total overflow", ErrBlackDiamondFatigueState)
		}
		nextCredited += BlackDiamondFatigueDailyCoins
	}

	if character.Stats == nil {
		character.Stats = make(map[string]int64, 4)
	}
	// Day rollover deliberately touches only the current-day bucket. The two
	// lifetime totals remain untouched.
	character.Stats[BlackDiamondFatigueDayKey] = dayID
	character.Stats[BlackDiamondFatigueSpentKey] = nextSpent
	character.Stats[BlackDiamondFatigueCreditedTotalKey] = nextCredited
	character.Stats[BlackDiamondFatigueDeliveredTotalKey] = delivered
	return nil
}

func validBlackDiamondLedger(stats map[string]int64) (credited, delivered int64, err error) {
	for _, key := range []string{
		BlackDiamondFatigueDayKey,
		BlackDiamondFatigueSpentKey,
		BlackDiamondFatigueCreditedTotalKey,
		BlackDiamondFatigueDeliveredTotalKey,
	} {
		if value, ok := stats[key]; ok && value < 0 {
			return 0, 0, fmt.Errorf("%w: %s=%d", ErrBlackDiamondFatigueState, key, value)
		}
	}
	credited = stats[BlackDiamondFatigueCreditedTotalKey]
	delivered = stats[BlackDiamondFatigueDeliveredTotalKey]
	if delivered > credited {
		return 0, 0, fmt.Errorf("%w: delivered=%d credited=%d", ErrBlackDiamondFatigueState, delivered, credited)
	}
	return credited, delivered, nil
}
