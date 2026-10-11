package dungeon

import (
	"context"
	"fmt"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strings"
	"time"
)

const EpicEntrySequenceKey = "epic_entry_sequence"
const epicEntryNonceKey = "epic_entry_nonce"
const epicEntryDungeonKey = "epic_entry_dungeon"

// EpicEntryTicketCount is limited to the three current 369d consuming dungeons.
func EpicEntryTicketCount(id int64) (int64, bool) {
	switch id {
	case 6063:
		return 30, true
	case 6064:
		return 45, true
	case 6065:
		return 60, true
	}
	return 0, false
}

type EpicEntryCommand struct {
	CharacterID, AccountID             string
	DungeonID, ExpectedSequence, Nonce int64
	Campaign                           *onlineevent.EpicCampaign
	UpdatedAt                          time.Time
	Project                            EntryItemProjector
	Validate                           func() error
}
type EpicEntryResult struct {
	Character dnfrepo.CharacterRecord
	Consumed  []EntryItemConsumedStack
	Replay    bool
}

// ConsumeEpicEntry keeps current FP1 behavior. It does not interpret the PVF
// 'use fatigue only start dungeon' payload as a new fatigue policy.
func (o *Owner) ConsumeEpicEntry(ctx context.Context, cmd EpicEntryCommand) (EpicEntryResult, error) {
	result := EpicEntryResult{}
	count, ok := EpicEntryTicketCount(cmd.DungeonID)
	if o == nil || o.assets == nil || !ok || strings.TrimSpace(cmd.CharacterID) == "" || strings.TrimSpace(cmd.AccountID) == "" || cmd.ExpectedSequence < 0 || cmd.ExpectedSequence == math.MaxInt64 || cmd.Nonce <= 0 || cmd.UpdatedAt.IsZero() {
		return result, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		if cmd.Validate != nil {
			if err := cmd.Validate(); err != nil {
				return err
			}
		}
		c, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID {
			return fmt.Errorf("%w: epic character owner", ErrEntryItemConsumeInvalid)
		}
		c = dnfrepo.CloneCharacter(c)
		seq := c.Stats[EpicEntrySequenceKey]
		if seq == cmd.ExpectedSequence+1 && c.Stats[epicEntryNonceKey] == cmd.Nonce && c.Stats[epicEntryDungeonKey] == cmd.DungeonID {
			result.Character = c
			result.Replay = true
			return nil
		}
		if seq != cmd.ExpectedSequence {
			return fmt.Errorf("%w: stale epic entry receipt", ErrEntryItemConsumeInvalid)
		}
		if c.Level < 85 {
			return ErrEntryLevelTooLow
		}
		if fp, known := c.Stats["fatigue"]; !known || fp <= 0 {
			return ErrEntryFatigueExhausted
		}
		v, found, err := bags.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || v.CharacterID != cmd.CharacterID {
			return ErrEntryItemConsumeInsufficient
		}
		v = dnfrepo.CloneInventory(v)
		slots, err := consumeEntryItemRequirement(&v, EntryItemRequirement{ItemID: 3330, Count: count, ConsumeOnEntry: true}, cmd.Project)
		if err != nil {
			return err
		}
		v.UpdatedAt = cmd.UpdatedAt
		if err = dnfrepo.SaveInventoryFields(ctx, bags, v, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if cmd.Campaign != nil {
			if err = onlineevent.AccumulateEpicInvitations(&c, *cmd.Campaign, cmd.UpdatedAt, count); err != nil {
				return err
			}
		}
		dnffatigue.Debit(&c)
		c.Stats[EpicEntrySequenceKey] = cmd.ExpectedSequence + 1
		c.Stats[epicEntryNonceKey] = cmd.Nonce
		c.Stats[epicEntryDungeonKey] = cmd.DungeonID
		c.UpdatedAt = cmd.UpdatedAt
		if err = dnfrepo.SaveCharacterFields(ctx, chars, c, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		if cmd.Validate != nil {
			if err = cmd.Validate(); err != nil {
				return err
			}
		}
		result.Character = c
		result.Consumed = []EntryItemConsumedStack{{ItemID: 3330, Count: count, Slots: slots}}
		return nil
	})
	if err != nil {
		return EpicEntryResult{}, err
	}
	return result, nil
}
