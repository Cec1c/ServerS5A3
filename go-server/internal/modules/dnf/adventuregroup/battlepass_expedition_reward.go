package adventuregroup

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// 奖励结算与远征删除、实际荣耀结晶增加、战令计数共用事务。旧状态比较阻止重放及覆盖并发变更。
func CommitBattlePassExpeditionReward(ctx context.Context, g repo.Group, accountID, characterID, expected string, config RuntimeConfig, area byte, now time.Time) (RuntimeState, error) {
	var result RuntimeState
	if g.CeraShopAssets == nil || accountID == "" || characterID == "" || now.IsZero() {
		return result, ErrOwnerUnavailable
	}
	err := g.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, "adventure_group_expedition:"+characterID, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.SettingsRepository) error {
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID || account.Metadata[RuntimeStateMetadataKey] != expected {
			return ErrExpeditionChanged
		}
		ch, found, err := chars.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != accountID || ch.CharacterID != characterID {
			return ErrExpeditionChanged
		}
		state, err := ParseRuntimeState(account, config, now)
		if err != nil {
			return err
		}
		key := ExpeditionKey(area)
		exp, found := state.Expeditions[key]
		if !found || exp.EndsAt > now.Unix() || exp.Reward == 0 {
			return ErrExpeditionChanged
		}
		before := state.ShopPoints[ShopPointGlory]
		delete(state.Expeditions, key)
		state.AddGlory(config, exp.Reward)
		encoded, err := state.Marshal()
		if err != nil {
			return err
		}
		if err := repo.SaveAccountMetadataEntry(ctx, accounts, account, RuntimeStateMetadataKey, encoded, now); err != nil {
			return err
		}
		if gained := int64(state.ShopPoints[ShopPointGlory]) - int64(before); gained > 0 {
			ch = repo.CloneCharacter(ch)
			repo.AddBattlePassCounter(&ch, 29, -1, gained, now)
			ch.UpdatedAt = now
			if err := repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats); err != nil {
				return err
			}
		}
		result = state
		return nil
	})
	if err != nil {
		return RuntimeState{}, err
	}
	return result, nil
}
