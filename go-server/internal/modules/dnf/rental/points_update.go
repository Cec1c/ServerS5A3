package rental

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const SystemPVFPath = "etc/chnrentsystem/rentsysteminfo.etc"

func PointLimit(doc *pvf.Document) (uint32, error) {
	if doc == nil {
		return 0, ErrCommandInvalid
	}
	values := doc.Ints("limit point")
	if len(values) != 1 || values[0] <= 0 || values[0] > math.MaxUint32 {
		return 0, fmt.Errorf("invalid rental [limit point]: %v", values)
	}
	return uint32(values[0]), nil
}

type UpdatePointsCommand struct {
	AccountID, CharacterID string
	Value, Amount          *int64
	Limit                  uint32
	UpdatedAt              time.Time
}

// UpdatePoints serializes administrative edits with purchases and dungeon
// settlements using the existing account lock. Only the point metadata changes.
func (o *Owner) UpdatePoints(ctx context.Context, cmd UpdatePointsCommand) (WalletResult, error) {
	if cmd.Limit == 0 || (cmd.Value == nil) == (cmd.Amount == nil) {
		return WalletResult{}, ErrCommandInvalid
	}
	var result WalletResult
	err := o.withinOwners(ctx, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, account repo.AccountRecord, character repo.CharacterRecord) error {
		current, err := Points(account)
		if err != nil {
			return err
		}
		target := int64(current)
		if cmd.Value != nil {
			target = *cmd.Value
		} else {
			amount := *cmd.Amount
			if amount == 0 {
				return ErrCommandInvalid
			}
			// Bounds before addition prevent overflow for hostile signed inputs.
			if amount < -target || amount > int64(cmd.Limit)-target {
				return ErrPointLimit
			}
			target += amount
		}
		if target < 0 || target > int64(cmd.Limit) {
			return ErrPointLimit
		}
		if target != int64(current) {
			if err := repo.SaveAccountMetadataEntry(ctx, accounts, account, PointMetadataKey, strconv.FormatInt(target, 10), normalizedTime(cmd.UpdatedAt)); err != nil {
				return err
			}
		}
		result = WalletResult{Points: uint32(target), Gold: character.Stats["gold"]}
		return nil
	})
	if err != nil {
		return WalletResult{}, err
	}
	return result, nil
}
