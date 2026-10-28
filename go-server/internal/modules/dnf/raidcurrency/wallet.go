// Package raidcurrency owns the account-wide fate currency and acquisition cap.
// Callers must persist it together with the source item or reward receipt.
package raidcurrency

import (
	"errors"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	FateItemID  uint32 = 10304529
	WeeklyLimit uint32 = 2500
	balanceKey         = "raid_fate_balance_v1"
	weekKey            = "raid_fate_week_v1"
	earnedKey          = "raid_fate_earned_v1"
)

var (
	ErrState        = errors.New("invalid account raid currency state")
	ErrInsufficient = errors.New("insufficient account raid currency")
)

type State struct {
	Balance uint32
	Earned  uint32
	Week    int64
}

// Read resets the acquisition window in its return value only. The balance
// survives week rollover. Existing malformed or partial counters are rejected.
func Read(account repo.AccountRecord, now time.Time) (State, error) {
	if account.AccountID == "" || now.IsZero() || now.Unix() <= 0 {
		return State{}, ErrState
	}
	state := State{Week: premium.ServiceDayID(now) / 7}
	b, bok := account.Metadata[balanceKey]
	w, wok := account.Metadata[weekKey]
	e, eok := account.Metadata[earnedKey]
	if !bok && !wok && !eok {
		return state, nil
	}
	if !bok || !wok || !eok {
		return State{}, ErrState
	}
	bn, err := strconv.ParseUint(b, 10, 32)
	if err != nil || bn > math.MaxInt32 {
		return State{}, ErrState
	}
	wn, err := strconv.ParseInt(w, 10, 64)
	if err != nil || wn < 0 || wn > state.Week {
		return State{}, ErrState
	}
	en, err := strconv.ParseUint(e, 10, 32)
	if err != nil || en > uint64(WeeklyLimit) {
		return State{}, ErrState
	}
	state.Balance = uint32(bn)
	if wn == state.Week {
		state.Earned = uint32(en)
	}
	return state, nil
}

func write(account *repo.AccountRecord, state State) {
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[balanceKey] = strconv.FormatUint(uint64(state.Balance), 10)
	account.Metadata[weekKey] = strconv.FormatInt(state.Week, 10)
	account.Metadata[earnedKey] = strconv.FormatUint(uint64(state.Earned), 10)
}

// Credit grants only the remainder of this account's weekly acquisition quota.
// A capped grant still belongs to the caller's durable reward receipt.
func Credit(account *repo.AccountRecord, amount uint32, now time.Time) (uint32, State, error) {
	if account == nil || amount == 0 {
		return 0, State{}, ErrState
	}
	state, err := Read(*account, now)
	if err != nil {
		return 0, State{}, err
	}
	granted := min(amount, WeeklyLimit-state.Earned)
	if uint64(state.Balance)+uint64(granted) > math.MaxInt32 {
		return 0, State{}, ErrState
	}
	state.Balance += granted
	state.Earned += granted
	write(account, state)
	return granted, state, nil
}

func Debit(account *repo.AccountRecord, amount uint32, now time.Time) (State, error) {
	if account == nil || amount == 0 {
		return State{}, ErrState
	}
	state, err := Read(*account, now)
	if err != nil {
		return State{}, err
	}
	if amount > state.Balance {
		return State{}, ErrInsufficient
	}
	state.Balance -= amount
	write(account, state)
	return state, nil
}

// Consolidate preserves already-owned materials without applying a new weekly
// cap to old property. Removing exactly the credited rows makes retries no-ops.
// The caller must save account and inventory in the same owner transaction.
func Consolidate(account *repo.AccountRecord, inventory *repo.InventoryRecord, now time.Time) (uint32, error) {
	if account == nil || inventory == nil || inventory.CharacterID == "" {
		return 0, ErrState
	}
	state, err := Read(*account, now)
	if err != nil {
		return 0, err
	}
	var total uint64
	keys := make([]string, 0)
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(FateItemID) {
			continue
		}
		// Timed or malformed variants need explicit handling, never silent loss.
		if stack.Count <= 0 || !stack.ExpireAt.IsZero() {
			return 0, ErrState
		}
		total += uint64(stack.Count)
		if total+uint64(state.Balance) > math.MaxInt32 {
			return 0, ErrState
		}
		keys = append(keys, key)
	}
	if total == 0 {
		return 0, nil
	}
	state.Balance += uint32(total)
	write(account, state)
	for _, key := range keys {
		delete(inventory.Slots, key)
	}
	return uint32(total), nil
}
