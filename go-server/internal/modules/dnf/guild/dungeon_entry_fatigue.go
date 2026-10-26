package guild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrDungeonEntryFatigue = errors.New("guild dungeon entry fatigue is insufficient")

type DungeonEntryFatigueQuery struct {
	Entries    []DungeonEntry
	Catalog    *DungeonCatalog
	Accounts   map[string]r.AccountRecord
	Cost       int64
	Revalidate func() bool
}

type DungeonEntryFatigueResult struct {
	Characters map[string]r.CharacterRecord
	Replayed   bool
}

type dungeonEntryFatigueReceipt struct {
	Entries []DungeonEntry `json:"entries"`
	Cost    int64          `json:"cost"`
}

// CommitDungeonEntryFatigue charges the frozen roster and its permanent run
// receipt in one transaction. The caller holds canonical sessions and party
// membership; no network writes or nested transactions occur here.
func CommitDungeonEntryFatigue(ctx context.Context, unit r.GuildUnitOfWork, q DungeonEntryFatigueQuery, now time.Time) (DungeonEntryFatigueResult, error) {
	var result DungeonEntryFatigueResult
	uow, ok := unit.(r.GuildDungeonEntryUnitOfWork)
	if !ok || q.Catalog == nil || q.Cost < 0 || q.Cost > math.MaxInt32 || q.Revalidate == nil || len(q.Entries) < 1 || len(q.Entries) > 4 {
		return result, r.ErrGuildTransaction
	}
	entries := append([]DungeonEntry(nil), q.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Owner.CharacterID < entries[j].Owner.CharacterID })
	owners := make([]r.GuildOwner, len(entries))
	first := entries[0]
	for i, entry := range entries {
		owners[i] = entry.Owner
		if entry.GuildID != first.GuildID || entry.DungeonID != first.DungeonID || entry.RunID != first.RunID || entry.Cycle != first.Cycle || entry.CatalogDigest != first.CatalogDigest || q.Accounts[entry.Owner.AccountID].AccountID != entry.Owner.AccountID {
			return result, ErrPermissionDenied
		}
		entries[i].JoinedAt = entry.JoinedAt.UTC()
	}
	if _, err := r.GuildDungeonEntryOwners(owners); err != nil {
		return result, err
	}
	scope := r.GuildDungeonEntryReceiptScope(first.GuildID, first.DungeonID, first.RunID)
	if scope == "" {
		return result, r.ErrGuildTransaction
	}
	encoded, err := json.Marshal(dungeonEntryFatigueReceipt{Entries: entries, Cost: q.Cost})
	if err != nil {
		return result, err
	}
	err = uow.WithinGuildDungeonEntry(ctx, owners, scope, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrPermissionDenied
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		current, err := PrepareDungeonEntries(reg, q.Catalog, first.DungeonID, owners, first.RunID, now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrPermissionDenied, err)
		}
		for i, e := range current {
			want := entries[i]
			if e.GuildID != want.GuildID || e.Cycle != want.Cycle || e.CatalogDigest != want.CatalogDigest || !e.JoinedAt.Equal(want.JoinedAt) {
				return ErrPermissionDenied
			}
		}
		receipt, found, err := tx.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		if found && (receipt.Scope != scope || receipt.Values["schema"] != "guild-dungeon-entry-fatigue:v1" || receipt.Values["receipt"] != string(encoded)) {
			return fmt.Errorf("%w: guild dungeon fatigue receipt mismatch", r.ErrGuildTransaction)
		}
		result = DungeonEntryFatigueResult{Characters: make(map[string]r.CharacterRecord, len(owners)), Replayed: found}
		for _, owner := range owners {
			character, exists, err := tx.Character.Load(ctx, owner.CharacterID)
			if err != nil {
				return err
			}
			if !exists || character.CharacterID != owner.CharacterID || character.AccountID != owner.AccountID {
				return r.ErrGuildTransaction
			}
			character = r.CloneCharacter(character)
			// Receipt lookup precedes balance validation: a run that spent its
			// last eight FP is still a successful replay at zero remaining FP.
			if !found {
				fatigue.Refresh(&character, q.Accounts[owner.AccountID], now)
				if character.Stats["fatigue"] < q.Cost {
					return fmt.Errorf("%w: character=%s required=%d", ErrDungeonEntryFatigue, owner.CharacterID, q.Cost)
				}
				character.Stats["fatigue"] -= q.Cost
				character.Stats["fatigue_used"] = character.Stats["fatigue_limit"] - character.Stats["fatigue"]
				character.Stats["fatigue_display_update"] = character.Stats["fatigue_used"]
			}
			result.Characters[owner.CharacterID] = character
		}
		if !q.Revalidate() {
			return ErrPermissionDenied
		}
		if found {
			return nil
		}
		for _, owner := range owners {
			if err := r.SaveCharacterFields(ctx, tx.Character, result.Characters[owner.CharacterID], r.CharacterFieldStats); err != nil {
				return err
			}
		}
		if err := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"schema": "guild-dungeon-entry-fatigue:v1", "receipt": string(encoded)}, UpdatedAt: now}); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrPermissionDenied
		}
		return nil
	})
	if err != nil {
		return DungeonEntryFatigueResult{}, err
	}
	return result, nil
}
