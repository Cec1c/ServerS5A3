package onlineevent

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 四项移植活动使用当前账号和角色资产事务；奖励邮件与账号回执禁止拆分保存。
func withinPortedEventAssets(ctx context.Context, repos repo.Group, accountID, characterID string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error, validators ...func() bool) error {
	u, ok := repos.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork)
	if !ok {
		return ErrOwnerUnavailable
	}
	check := func() bool { return len(validators) == 0 || validators[0] == nil || validators[0]() }
	return u.WithinAccountTradeMailbox(ctx, accountID, []string{characterID}, characterID, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		if !check() {
			return ErrObservationStale
		}
		if err := apply(a, c, i, m); err != nil {
			return err
		}
		if !check() {
			return ErrObservationStale
		}
		return nil
	})
}
