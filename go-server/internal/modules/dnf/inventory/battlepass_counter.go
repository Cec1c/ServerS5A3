package inventory

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// 调用方必须持有角色资产事务；抽奖失败但已经合法消耗材料仍是一次升级尝试。
func (o *Owner) recordBattlePassOperation(ctx context.Context, id string, kind uint32, now time.Time) error {
	if !o.inItemTransaction || o.characters == nil {
		return ErrCharacterAssetTxnRequired
	}
	ch, found, err := o.characters.Load(ctx, id)
	if err != nil {
		return err
	}
	if !found || ch.CharacterID != id {
		return ErrCharacterRequired
	}
	ch = dnfrepo.CloneCharacter(ch)
	dnfrepo.AddBattlePassCounter(&ch, kind, -1, 1, now)
	ch.UpdatedAt = now
	return dnfrepo.SaveCharacterFields(ctx, o.characters, ch, dnfrepo.CharacterFieldStats)
}
