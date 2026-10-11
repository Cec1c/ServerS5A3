package dungeon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const dayDungeonAccountMetadataKey = "day_dungeon_admitted_characters_v1"

// The original five growthguide descriptions distinguish per-dungeon entries
// from the account's daily set of distinct admitted characters.
func DayDungeonCharacterEntryLimit(dungeonID int64) int64 {
	if dungeonID == 4906 {
		return 2
	}
	if dungeonID >= 4907 && dungeonID <= 4910 {
		return 1
	}
	return 0
}

type dayDungeonAccountState struct {
	Version    int      `json:"version"`
	Day        int64    `json:"day"`
	Characters []string `json:"characters"`
}

type DayDungeonEntryStatus struct {
	CharacterUsed         int64
	CharacterLimit        int64
	AccountCharactersUsed int64
	AccountCharacterLimit int64
	CharacterAdmitted     bool
}

type DayDungeonEntryCommand struct {
	AccountID             string
	CharacterID           string
	Dungeon               worldmap.Dungeon
	AccountCharacterLimit int64
	ObservedAt            time.Time
}

type DayDungeonEntryResult struct {
	Character dnfrepo.CharacterRecord
	Status    DayDungeonEntryStatus
}

type DayDungeonEntryOwner struct {
	assets     dnfrepo.RentalAssetUnitOfWork
	accounts   dnfrepo.AccountRepository
	characters dnfrepo.CharacterRepository
}

// The existing account/character asset transaction locks both owners and
// supplies scoped metadata/stat repositories. No rental points or items change.
func NewDayDungeonEntryOwner(repos dnfrepo.Group) (*DayDungeonEntryOwner, error) {
	if repos.RentalAssets == nil || repos.Account == nil || repos.Character == nil {
		return nil, ErrOwnerUnavailable
	}
	return &DayDungeonEntryOwner{repos.RentalAssets, repos.Account, repos.Character}, nil
}

func dayDungeonStatus(account dnfrepo.AccountRecord, character dnfrepo.CharacterRecord, cmd DayDungeonEntryCommand) (dayDungeonAccountState, DayDungeonEntryStatus, error) {
	characterID, idErr := strconv.ParseUint(cmd.CharacterID, 10, 16)
	if idErr != nil || characterID == 0 || strconv.FormatUint(characterID, 10) != cmd.CharacterID {
		return dayDungeonAccountState{}, DayDungeonEntryStatus{}, ErrEntryItemConsumeInvalid
	}
	if strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CharacterID) == "" || cmd.ObservedAt.IsZero() || cmd.AccountCharacterLimit < 5 || cmd.AccountCharacterLimit > 10 || DayDungeonCharacterEntryLimit(cmd.Dungeon.ID) == 0 {
		return dayDungeonAccountState{}, DayDungeonEntryStatus{}, ErrEntryItemConsumeInvalid
	}
	if account.AccountID != cmd.AccountID || character.CharacterID != cmd.CharacterID || character.AccountID != cmd.AccountID {
		return dayDungeonAccountState{}, DayDungeonEntryStatus{}, fmt.Errorf("day-dungeon account/character owner mismatch")
	}
	if err := ValidateDayDungeonSchedule(cmd.Dungeon, cmd.ObservedAt); err != nil {
		return dayDungeonAccountState{}, DayDungeonEntryStatus{}, err
	}
	state := dayDungeonAccountState{Version: 1, Day: premium.ServiceDayID(cmd.ObservedAt)}
	if raw := account.Metadata[dayDungeonAccountMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil || state.Version != 1 || len(state.Characters) > 10 {
			return dayDungeonAccountState{}, DayDungeonEntryStatus{}, ErrEntryItemConsumeInvalid
		}
		seen := make(map[string]bool)
		for _, id := range state.Characters {
			value, err := strconv.ParseUint(id, 10, 16)
			if err != nil || value == 0 || strconv.FormatUint(value, 10) != id || seen[id] {
				return dayDungeonAccountState{}, DayDungeonEntryStatus{}, ErrEntryItemConsumeInvalid
			}
			seen[id] = true
		}
		if state.Day != premium.ServiceDayID(cmd.ObservedAt) {
			state.Day = premium.ServiceDayID(cmd.ObservedAt)
			state.Characters = nil
		}
	}
	used, err := DailyEntryUsed(character, cmd.Dungeon.ID, cmd.ObservedAt)
	if err != nil {
		return dayDungeonAccountState{}, DayDungeonEntryStatus{}, err
	}
	status := DayDungeonEntryStatus{CharacterUsed: used, CharacterLimit: DayDungeonCharacterEntryLimit(cmd.Dungeon.ID), AccountCharactersUsed: int64(len(state.Characters)), AccountCharacterLimit: cmd.AccountCharacterLimit}
	for _, id := range state.Characters {
		if id == cmd.CharacterID {
			status.CharacterAdmitted = true
		}
	}
	if used >= status.CharacterLimit || !status.CharacterAdmitted && status.AccountCharactersUsed >= status.AccountCharacterLimit {
		return state, status, ErrDailyEntryLimit
	}
	return state, status, nil
}

func (o *DayDungeonEntryOwner) Status(ctx context.Context, cmd DayDungeonEntryCommand) (DayDungeonEntryStatus, error) {
	if o == nil || o.accounts == nil || o.characters == nil {
		return DayDungeonEntryStatus{}, ErrOwnerUnavailable
	}
	ctx = contextOrBackground(ctx)
	a, found, err := o.accounts.Load(ctx, cmd.AccountID)
	if err != nil {
		return DayDungeonEntryStatus{}, err
	}
	if !found {
		return DayDungeonEntryStatus{}, ErrAccountNotFound
	}
	c, found, err := o.characters.Load(ctx, cmd.CharacterID)
	if err != nil {
		return DayDungeonEntryStatus{}, err
	}
	if !found {
		return DayDungeonEntryStatus{}, ErrCharacterNotFound
	}
	_, status, err := dayDungeonStatus(a, c, cmd)
	return status, err
}

// Consume atomically commits one character entry and that account's distinct
// character set. Rechecking under the account lock prevents concurrent sixth
// characters or a duplicate entry from passing two independent status reads.
func (o *DayDungeonEntryOwner) Consume(ctx context.Context, cmd DayDungeonEntryCommand) (DayDungeonEntryResult, error) {
	if o == nil || o.assets == nil {
		return DayDungeonEntryResult{}, ErrOwnerUnavailable
	}
	ctx = contextOrBackground(ctx)
	var result DayDungeonEntryResult
	err := o.assets.WithinRentalAssets(ctx, cmd.AccountID, cmd.CharacterID, func(accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		a, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrAccountNotFound
		}
		c, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		state, status, err := dayDungeonStatus(a, c, cmd)
		if err != nil {
			return err
		}
		if !status.CharacterAdmitted {
			state.Characters = append(state.Characters, cmd.CharacterID)
			value, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if err := dnfrepo.SaveAccountMetadataEntry(ctx, accounts, a, dayDungeonAccountMetadataKey, string(value), cmd.ObservedAt); err != nil {
				return err
			}
			status.AccountCharactersUsed++
			status.CharacterAdmitted = true
		}
		c = dnfrepo.CloneCharacter(c)
		if c.Stats == nil {
			c.Stats = map[string]int64{}
		}
		dayKey, usedKey := dailyEntryKeys(cmd.Dungeon.ID)
		c.Stats[dayKey] = premium.ServiceDayID(cmd.ObservedAt)
		c.Stats[usedKey] = status.CharacterUsed + 1
		c.UpdatedAt = cmd.ObservedAt
		if err := dnfrepo.SaveCharacterFields(ctx, characters, c, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		status.CharacterUsed++
		result = DayDungeonEntryResult{Character: c, Status: status}
		return nil
	})
	if err != nil {
		return DayDungeonEntryResult{}, err
	}
	return result, nil
}
