package repository

import "context"

// AccountTradeMailboxUnitOfWork 在同一事务内更新一个账号、指定角色资产及该账号
// 所属角色的邮箱。用于金币寄售扣款、金币投递及代币券领取，不允许分次提交模拟。
// mailboxCharacterID 必须属于 accountID，且在 characterIDs 中。
type AccountTradeMailboxUnitOfWork interface {
	WithinAccountTradeMailbox(context.Context, string, []string, string, func(AccountRepository, CharacterRepository, InventoryRepository, MailboxRepository) error) error
}
