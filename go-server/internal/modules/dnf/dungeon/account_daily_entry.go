package dungeon

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const SilentForestDungeonID int64 = 6062
const silentForestAccountEntryKey = "dungeon_account_daily_entry_6062_v1"

type accountDungeonEntryState struct {
	Day  int64 `json:"day"`
	Used int64 `json:"used"`
}

// SilentForestEntryUsed is shared by every character on the account. The
// established service day resets at 06:00 UTC+8, also stated by this event's PVF.
// Earlier admissions are not reconstructed from inventory or clear history.
func SilentForestEntryUsed(account repo.AccountRecord, now time.Time) (int64, error) {
	if strings.TrimSpace(account.AccountID) == "" || now.IsZero() {
		return 0, ErrEntryItemConsumeInvalid
	}
	raw := account.Metadata[silentForestAccountEntryKey]
	if raw == "" {
		return 0, nil
	}
	var state accountDungeonEntryState
	if err := json.Unmarshal([]byte(raw), &state); err != nil || state.Day <= 0 || state.Used < 0 || state.Day > premium.ServiceDayID(now) {
		return 0, fmt.Errorf("%w: invalid account dungeon daily entry ledger", ErrEntryItemConsumeInvalid)
	}
	if state.Day != premium.ServiceDayID(now) {
		return 0, nil
	}
	return state.Used, nil
}

// ConsumeSilentForestEntry shares the prepared event admission identity and
// durable replay receipt, but has neither a ticket charge nor fatigue debit.
// Account allowance and character receipt commit under the same account-first
// transaction, including when two different characters compete for entry ten.
func (o *Owner) ConsumeSilentForestEntry(ctx context.Context, cmd EpicEntryCommand, limit int64) (EpicEntryResult, error) {
	if o == nil || cmd.DungeonID != SilentForestDungeonID || limit != 10 || strings.TrimSpace(cmd.CharacterID) == "" || strings.TrimSpace(cmd.AccountID) == "" || cmd.ExpectedSequence < 0 || cmd.ExpectedSequence == math.MaxInt64 || cmd.Nonce <= 0 || cmd.UpdatedAt.IsZero() {
		return EpicEntryResult{}, ErrEntryItemConsumeInvalid
	}
	uow, ok := o.accountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)
	if !ok {
		return EpicEntryResult{}, ErrOwnerUnavailable
	}
	ctx = contextOrBackground(ctx)
	var result EpicEntryResult
	err := uow.WithinAccountCharacterLedgerAssets(ctx, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, _ repo.AccountInventoryRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		if cmd.Validate != nil {
			if err := cmd.Validate(); err != nil {
				return err
			}
		}
		account, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != cmd.AccountID {
			return ErrAccountNotFound
		}
		c, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID {
			return ErrEntryItemConsumeInvalid
		}
		if c.Level < 17 {
			return ErrEntryLevelTooLow
		}
		used, err := SilentForestEntryUsed(account, cmd.UpdatedAt)
		if err != nil {
			return err
		}
		seq := c.Stats[EpicEntrySequenceKey]
		if seq == cmd.ExpectedSequence+1 && c.Stats[epicEntryNonceKey] == cmd.Nonce && c.Stats[epicEntryDungeonKey] == cmd.DungeonID {
			result.Character, result.Replay = c, true
			return nil
		}
		if seq != cmd.ExpectedSequence {
			return fmt.Errorf("%w: stale account entry receipt", ErrEntryItemConsumeInvalid)
		}
		if used >= limit {
			return ErrDailyEntryLimit
		}
		raw, err := json.Marshal(accountDungeonEntryState{Day: premium.ServiceDayID(cmd.UpdatedAt), Used: used + 1})
		if err != nil {
			return err
		}
		if err = repo.SaveAccountMetadataEntry(ctx, accounts, account, silentForestAccountEntryKey, string(raw), cmd.UpdatedAt); err != nil {
			return err
		}
		c = repo.CloneCharacter(c)
		if c.Stats == nil {
			c.Stats = map[string]int64{}
		}
		c.Stats[EpicEntrySequenceKey] = cmd.ExpectedSequence + 1
		c.Stats[epicEntryNonceKey] = cmd.Nonce
		c.Stats[epicEntryDungeonKey] = cmd.DungeonID
		c.UpdatedAt = cmd.UpdatedAt
		if err = repo.SaveCharacterFields(ctx, chars, c, repo.CharacterFieldStats); err != nil {
			return err
		}
		if cmd.Validate != nil {
			if err = cmd.Validate(); err != nil {
				return err
			}
		}
		result.Character = c
		return nil
	})
	if err != nil {
		return EpicEntryResult{}, err
	}
	return result, nil
}
