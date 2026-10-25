package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// GuildSettlementUnitOfWork adds the registry and one durable completion
// receipt and the owned mailbox to the existing character settlement. Lock order is registry first,
// then the owned character and its assets; no nested transaction is allowed.
type GuildSettlementUnitOfWork interface {
	WithinGuildSettlement(context.Context, GuildOwner, string, func(Group) error) error
}

func GuildSettlementReceiptScope(owner GuildOwner, completion string) string {
	return fmt.Sprintf("guild-clear:v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%q:%q:%q", owner.AccountID, owner.CharacterID, completion))))
}

func GuildSettlementSettings(base SettingsRepository, receiptScope string) SettingsRepository {
	return &guildSettlementSettings{base: base, receiptScope: receiptScope}
}

type guildSettlementSettings struct {
	base         SettingsRepository
	receiptScope string
}

func (r *guildSettlementSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if r.base == nil || (scope != GuildRegistryScope && scope != r.receiptScope) {
		return SettingsRecord{}, false, ErrGuildTransaction
	}
	return r.base.Load(ctx, scope)
}

func (r *guildSettlementSettings) Save(ctx context.Context, record SettingsRecord) error {
	if r.base == nil || (record.Scope != GuildRegistryScope && record.Scope != r.receiptScope) {
		return ErrGuildTransaction
	}
	return r.base.Save(ctx, record)
}
