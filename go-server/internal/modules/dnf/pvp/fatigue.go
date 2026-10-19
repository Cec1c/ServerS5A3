package pvp

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/fatigue"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

var ErrFatigueInsufficient = errors.New("街机疲劳不足")

type ConsumeArcadeCommand struct {
	AccountID, CharacterID string
	Amount                 int64
	RequireFull            bool
	UpdatedAt              time.Time
}
type ConsumeArcadeResult struct{ Before, After, Consumed int64 }

// 街机先收取完整疲劳；复用现用会员上限、服务日刷新及账号角色事务。
func ConsumeArcadeFatigue(ctx context.Context, repos repo.Group, q ConsumeArcadeCommand) (ConsumeArcadeResult, error) {
	var out ConsumeArcadeResult
	if repos.RentalAssets == nil || q.AccountID == "" || q.CharacterID == "" || q.Amount < 0 || q.UpdatedAt.IsZero() {
		return out, repo.ErrRentalAssetTransactionUnavailable
	}
	err := repos.RentalAssets.WithinRentalAssets(ctx, q.AccountID, q.CharacterID, func(a repo.AccountRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		account, ok, err := a.Load(ctx, q.AccountID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("街机账号不存在")
		}
		character, ok, err := c.Load(ctx, q.CharacterID)
		if err != nil {
			return err
		}
		if !ok || character.AccountID != q.AccountID {
			return fmt.Errorf("街机角色归属不符")
		}
		character = repo.CloneCharacter(character)
		fatigue.Refresh(&character, account, q.UpdatedAt)
		out.Before = character.Stats["fatigue"]
		if out.Before < q.Amount {
			return ErrFatigueInsufficient
		}
		out.Consumed = q.Amount
		out.After = out.Before - q.Amount
		character.Stats["fatigue"] = out.After
		character.Stats["fatigue_used"] = character.Stats["fatigue_limit"] - out.After
		character.Stats["fatigue_display_update"] = character.Stats["fatigue_used"]
		character.UpdatedAt = q.UpdatedAt
		return repo.SaveCharacterFields(ctx, c, character, repo.CharacterFieldStats)
	})
	return out, err
}
