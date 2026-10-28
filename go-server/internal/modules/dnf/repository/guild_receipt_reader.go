package repository

import (
	"context"
	"time"
)

// GuildSettlementReceiptReader streams immutable dungeon settlement receipts.
// Call under the guild registry lock so totals and receipts have one boundary.
// Implementations read the writer, never a potentially lagging replica.
type GuildSettlementReceiptReader interface {
	WalkGuildSettlementReceipts(context.Context, uint32, time.Time, func(SettingsRecord) error) error
}

func (s *guildSettings) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(SettingsRecord) error) error {
	reader, ok := s.base.(GuildSettlementReceiptReader)
	if !ok {
		return ErrGuildTransaction
	}
	return reader.WalkGuildSettlementReceipts(ctx, guildID, since, visit)
}
