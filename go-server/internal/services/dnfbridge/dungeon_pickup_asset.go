package dnfbridge

import (
	"context"
	"errors"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func grantCurrentDungeonPickupGold(
	ctx context.Context,
	uow dnfrepo.CharacterAssetUnitOfWork,
	characterID string,
	amount uint32,
	now time.Time,
	adjust ...dnfdungeon.GoldPickupAdjuster,
) (int64, error) {
	if uow == nil || characterID == "" || amount == 0 {
		return 0, errDungeonPickupTransactionMissing
	}
	owner, err := dnfdungeon.NewOwner(dnfrepo.Group{CharacterAssets: uow})
	if err != nil {
		return 0, errDungeonPickupTransactionMissing
	}
	var modifier dnfdungeon.GoldPickupAdjuster
	if len(adjust) > 0 {
		modifier = adjust[0]
	}
	result, err := owner.GrantPickupGold(ctx, dnfdungeon.GoldPickupCommand{
		CharacterID: characterID,
		Amount:      amount,
		Adjust:      modifier,
		UpdatedAt:   now,
	})
	if err != nil {
		switch {
		case errors.Is(err, dnfdungeon.ErrOwnerUnavailable):
			return 0, errDungeonPickupTransactionMissing
		case errors.Is(err, dnfdungeon.ErrCharacterNotFound):
			return 0, errDungeonPickupInventoryMissing
		case errors.Is(err, dnfdungeon.ErrRewardInvalid),
			errors.Is(err, dnfdungeon.ErrGoldOverflow):
			return 0, errDungeonPickupItemInvalid
		}
		return 0, err
	}
	return result.GoldAfter, nil
}
