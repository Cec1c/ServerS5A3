package fiendwar

import (
	"fmt"
	"time"
)

// EntryReservation is an immutable before/after journal for one speculative
// Enter. The bridge owns its lifetime by raid instance and run ID. It is never
// used for a committed encounter or for a player's retreat.
type EntryReservation struct {
	before, after *FiendWarBattle
	area          int
	group         byte
}

func (b *FiendWarBattle) ReserveEntry(group byte, dungeon int, now time.Time) (*FiendWarBattle, *EntryReservation, error) {
	if b == nil || group == 0 {
		return nil, nil, fmt.Errorf("fiendwar reservation owner missing")
	}
	a, ok := b.AreaForDungeon(dungeon)
	if !ok || len(b.pending) != 0 {
		return nil, nil, fmt.Errorf("fiendwar reservation boundary unavailable")
	}
	next := b.Clone()
	next.GroupAreas[group] = a.Index
	if err := next.Enter(group, dungeon, now); err != nil {
		return nil, nil, err
	}
	if len(next.pending) != 0 {
		return nil, nil, fmt.Errorf("fiendwar reservation has pending events")
	}
	return next, &EntryReservation{before: b.Clone(), after: next.Clone(), area: a.Index, group: group}, nil
}

// rollbackEntryMap touches only keys changed by Enter, and only if no later
// operation changed their after-image. Existence is part of the comparison:
// restoring a deleted timer must not overwrite a subsequently installed timer.
func rollbackEntryMap[K comparable, V comparable](current, before, after map[K]V) bool {
	keys := make(map[K]bool, len(before)+len(after))
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		old, hadOld := before[key]
		next, hadNext := after[key]
		if hadOld == hadNext && old == next {
			continue
		}
		value, exists := current[key]
		if exists != hadNext || value != next {
			return false
		}
		if hadOld {
			current[key] = old
		} else {
			delete(current, key)
		}
	}
	return true
}

func rollbackEntryValue[T comparable](current *T, before, after T) bool {
	if before == after {
		return true
	}
	if *current != after {
		return false
	}
	*current = before
	return true
}

func (b *FiendWarBattle) CancelEntry(reservation *EntryReservation) (*FiendWarBattle, error) {
	if b == nil || reservation == nil || reservation.before == nil || reservation.after == nil ||
		b.Config != reservation.before.Config || len(b.pending) != 0 || b.Complete || b.Failed ||
		b.Areas[reservation.area].Group != reservation.group || b.GroupAreas[reservation.group] != reservation.area {
		return nil, fmt.Errorf("fiendwar reservation owner changed")
	}
	old, after := reservation.before, reservation.after
	next := b.Clone()
	// All writes are made to a private clone; a conflict discards the entire
	// rollback. Unrelated keys/scalars retain the latest shared battle value.
	valid := rollbackEntryMap(next.Symbols, old.Symbols, after.Symbols) &&
		rollbackEntryMap(next.Areas, old.Areas, after.Areas) &&
		rollbackEntryMap(next.Timers, old.Timers, after.Timers) &&
		rollbackEntryMap(next.DungeonStates, old.DungeonStates, after.DungeonStates) &&
		rollbackEntryMap(next.GroupAreas, old.GroupAreas, after.GroupAreas) &&
		rollbackEntryMap(next.MemberAreas, old.MemberAreas, after.MemberAreas) &&
		rollbackEntryMap(next.RecoveryUntil, old.RecoveryUntil, after.RecoveryUntil) &&
		rollbackEntryMap(next.ImprisonCounts, old.ImprisonCounts, after.ImprisonCounts) &&
		rollbackEntryMap(next.Retreats, old.Retreats, after.Retreats) &&
		rollbackEntryMap(next.FixtureMazes, old.FixtureMazes, after.FixtureMazes) &&
		rollbackEntryValue(&next.MerchantArea, old.MerchantArea, after.MerchantArea) &&
		rollbackEntryValue(&next.MerchantKind, old.MerchantKind, after.MerchantKind) &&
		rollbackEntryValue(&next.MerchantDuration, old.MerchantDuration, after.MerchantDuration) &&
		rollbackEntryValue(&next.MerchantExpiresAt, old.MerchantExpiresAt, after.MerchantExpiresAt) &&
		rollbackEntryValue(&next.NextUnitID, old.NextUnitID, after.NextUnitID) &&
		rollbackEntryValue(&next.Complete, old.Complete, after.Complete) &&
		rollbackEntryValue(&next.Failed, old.Failed, after.Failed) &&
		rollbackEntryValue(&next.BossWindowDone, old.BossWindowDone, after.BossWindowDone)
	if !valid {
		return nil, fmt.Errorf("fiendwar reservation after-image changed")
	}
	return next, nil
}
