// Package experienceitem owns the durable rules for the character-progression
// consumables the current client sends through ENUM_CMDPACKET_INCREASE_STATUS
// (op30). This round implements the SP/TP skill books the runtime PVF declares
// as `SP+5技能书` / `SP+20技能书` / `TP+1技能书` / `TP+5技能书`.
//
// The reported defect behind this package is "SP书无法使用": op44 (use
// stackable) requires a PVF-proven `[waste]` row, and every skill book is
// `[stackable type] `[etc]“, so the item was rejected before any effect. The
// reference implementation (ServerS4A21 `ExperienceItemUseService`) grants the
// book through INCREASE_STATUS and re-synchronizes the absolute SP/TP ledger;
// this owner performs the item decrement, the persisted bonus points, and the
// PVF-based ledger recomputation inside one character-settlement transaction.
package experienceitem

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

var (
	ErrOwnerUnavailable       = errors.New("experience item repositories are unavailable")
	ErrAccountRequired        = errors.New("experience item account is required")
	ErrCharacterRequired      = errors.New("experience item character is required")
	ErrCharacterNotFound      = errors.New("experience item character was not found")
	ErrCharacterOwnerMismatch = errors.New("experience item character owner mismatch")
	ErrSlotNotFound           = errors.New("experience item source slot was not found")
	ErrItemMismatch           = errors.New("experience item source row does not match the request")
	ErrBookInvalid            = errors.New("experience item skill book definition is invalid")
	ErrLedgerUnavailable      = errors.New("experience item skill-point ledger is unavailable")
	ErrLedgerRegression       = errors.New("experience item would reduce the persisted SP/TP ledger")
)

// Kind is the point family one book grants.
type Kind uint8

const (
	KindSP Kind = iota + 1
	KindTP
)

// Book is one PVF-proven skill book. Amount and Kind come from the runtime PVF
// item definition, never from the request or from a hardcoded item table.
type Book struct {
	ItemID  int64
	Kind    Kind
	Amount  int
	PVFPath string
	Name    string
}

func (b Book) Validate() error {
	if b.ItemID <= 0 {
		return fmt.Errorf("%w: item=%d", ErrBookInvalid, b.ItemID)
	}
	if b.Kind != KindSP && b.Kind != KindTP {
		return fmt.Errorf("%w: item=%d kind=%d", ErrBookInvalid, b.ItemID, b.Kind)
	}
	if b.Amount <= 0 {
		return fmt.Errorf("%w: item=%d amount=%d", ErrBookInvalid, b.ItemID, b.Amount)
	}
	return nil
}

// Command is one skill-book use. PVFTotalSP/PVFTotalTP are the runtime PVF
// cumulative tables at the character's current level; the owner adds the
// persisted bonus points to them exactly like the login ledger repair.
type Command struct {
	AccountID       string
	CharacterID     string
	SourceListType  byte
	SourceSlotIndex int16
	ItemID          int64
	PVFTotalSP      int
	PVFTotalTP      int
	Book            Book
	Now             time.Time
}

// Result describes the committed use.
type Result struct {
	AccountID      string
	CharacterID    string
	ListType       byte
	SlotIndex      int16
	ItemID         int64
	RemainingCount int64
	Book           Book
	Points         dnfrepo.SkillPointState
	BonusSP        int64
	BonusTP        int64
}

type Owner struct {
	repositories dnfrepo.Group
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.CharacterSettlement == nil ||
		repositories.Character == nil ||
		repositories.Inventory == nil ||
		repositories.Skill == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{repositories: repositories}, nil
}

func (o *Owner) UseSkillBook(ctx context.Context, cmd Command) (Result, error) {
	if o == nil || o.repositories.CharacterSettlement == nil {
		return Result{}, ErrOwnerUnavailable
	}
	accountID := strings.TrimSpace(cmd.AccountID)
	characterID := strings.TrimSpace(cmd.CharacterID)
	if accountID == "" {
		return Result{}, ErrAccountRequired
	}
	if characterID == "" {
		return Result{}, ErrCharacterRequired
	}
	if err := cmd.Book.Validate(); err != nil {
		return Result{}, err
	}
	if cmd.ItemID <= 0 || cmd.ItemID != cmd.Book.ItemID {
		return Result{}, fmt.Errorf("%w: request=%d book=%d", ErrItemMismatch, cmd.ItemID, cmd.Book.ItemID)
	}
	if cmd.SourceSlotIndex < 0 || cmd.SourceListType != dnfrepo.MainInventoryListType {
		return Result{}, fmt.Errorf("%w: list=%d slot=%d", ErrSlotNotFound, cmd.SourceListType, cmd.SourceSlotIndex)
	}
	if cmd.PVFTotalSP < 0 || cmd.PVFTotalTP < 0 {
		return Result{}, fmt.Errorf("%w: pvf_total_sp=%d pvf_total_tp=%d", ErrLedgerUnavailable, cmd.PVFTotalSP, cmd.PVFTotalTP)
	}
	now := cmd.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	result := Result{
		AccountID:   accountID,
		CharacterID: characterID,
		ListType:    cmd.SourceListType,
		SlotIndex:   cmd.SourceSlotIndex,
		ItemID:      cmd.ItemID,
		Book:        cmd.Book,
	}
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(
		ctx,
		characterID,
		func(tx dnfrepo.Group) error {
			character, characterFound, loadErr := tx.Character.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if !characterFound {
				return ErrCharacterNotFound
			}
			if strings.TrimSpace(character.AccountID) != accountID {
				return ErrCharacterOwnerMismatch
			}
			if character.Level <= 0 {
				return fmt.Errorf("%w: character_level=%d", ErrLedgerUnavailable, character.Level)
			}

			inventory, inventoryFound, loadErr := tx.Inventory.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if !inventoryFound {
				return ErrSlotNotFound
			}
			key := slotKey(cmd.SourceListType, cmd.SourceSlotIndex)
			stack, stackFound := inventory.Slots[key]
			if !stackFound || stack.Count <= 0 {
				return fmt.Errorf("%w: list=%d slot=%d", ErrSlotNotFound, cmd.SourceListType, cmd.SourceSlotIndex)
			}
			if stack.ItemID != cmd.ItemID {
				return fmt.Errorf("%w: list=%d slot=%d want=%d got=%d", ErrItemMismatch, cmd.SourceListType, cmd.SourceSlotIndex, cmd.ItemID, stack.ItemID)
			}

			skill, skillFound, loadErr := tx.Skill.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if !skillFound || skill.CharacterID != characterID {
				return fmt.Errorf("%w: character=%s", ErrLedgerUnavailable, characterID)
			}

			currentBonusSP := characterStatValue(character, "bonus_sp")
			currentBonusTP := characterStatValue(character, "bonus_tp")
			if currentBonusSP < 0 || currentBonusTP < 0 {
				return fmt.Errorf("%w: bonus_sp=%d bonus_tp=%d", ErrLedgerUnavailable, currentBonusSP, currentBonusTP)
			}
			bonusSP := currentBonusSP
			bonusTP := currentBonusTP
			if cmd.Book.Kind == KindSP {
				bonusSP += int64(cmd.Book.Amount)
			} else {
				bonusTP += int64(cmd.Book.Amount)
			}

			target, targetErr := ledgerTarget(cmd.PVFTotalSP, cmd.PVFTotalTP, bonusSP, bonusTP, character.Level)
			if targetErr != nil {
				return targetErr
			}
			next, _, syncErr := dnfskill.SyncPointState(skill.Points, target)
			if syncErr != nil {
				return fmt.Errorf("%w: %v", ErrLedgerUnavailable, syncErr)
			}
			// A book may only ever add points. If the persisted ledger was
			// higher than PVF totals plus bonus (an out-of-band grant this
			// owner cannot account for), refuse instead of silently reducing
			// the player's SP/TP.
			if next.TotalSP < skill.Points.TotalSP || next.TotalTP < skill.Points.TotalTP {
				return fmt.Errorf(
					"%w: current=%+v next=%+v",
					ErrLedgerRegression,
					skill.Points,
					next,
				)
			}

			remaining := stack.Count - 1
			if remaining == 0 {
				delete(inventory.Slots, key)
			} else {
				updated := stack
				updated.RawEntry = append([]byte(nil), stack.RawEntry...)
				updated.Extra = cloneStringMap(stack.Extra)
				updated.Count = remaining
				inventory.Slots[key] = updated
			}
			inventory.UpdatedAt = now
			if saveErr := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, inventory, dnfrepo.InventoryFieldSlots); saveErr != nil {
				return saveErr
			}

			if character.Stats == nil {
				character.Stats = make(map[string]int64, 8)
			}
			character.Stats["bonus_sp"] = bonusSP
			character.Stats["bonus_tp"] = bonusTP
			character.UpdatedAt = now
			if saveErr := dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldStats); saveErr != nil {
				return saveErr
			}

			skill.Points = next
			skill.UpdatedAt = now
			if saveErr := dnfrepo.SaveSkillFields(ctx, tx.Skill, skill, dnfrepo.SkillFieldPoints); saveErr != nil {
				return saveErr
			}

			result.RemainingCount = remaining
			result.Points = next
			result.BonusSP = bonusSP
			result.BonusTP = bonusTP
			return nil
		},
	)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func ledgerTarget(pvfTotalSP, pvfTotalTP int, bonusSP, bonusTP int64, level int) (dnfrepo.SkillPointState, error) {
	if bonusSP < 0 || bonusTP < 0 ||
		int64(pvfTotalSP) > int64(^uint(0)>>1)-bonusSP ||
		int64(pvfTotalTP) > int64(^uint(0)>>1)-bonusTP {
		return dnfrepo.SkillPointState{}, fmt.Errorf("%w: pvf_sp=%d pvf_tp=%d bonus_sp=%d bonus_tp=%d", ErrLedgerUnavailable, pvfTotalSP, pvfTotalTP, bonusSP, bonusTP)
	}
	return dnfrepo.SkillPointState{
		TotalSP:     pvfTotalSP + int(bonusSP),
		TotalTP:     pvfTotalTP + int(bonusTP),
		SyncedLevel: level,
	}, nil
}

func slotKey(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.FormatInt(int64(slot), 10)
}

func characterStatValue(character dnfrepo.CharacterRecord, key string) int64 {
	if character.Stats == nil {
		return 0
	}
	return character.Stats[key]
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
