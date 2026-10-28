package guild

import (
	"context"
	"encoding/json"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// HongbaoContributionMembers projects earned contribution coins, retaining the
// legacy dungeon score in storage and in its original consumers. Only verified
// dungeon receipts from this membership are subtracted. Spending does not
// reduce lifetime earnings. This function never saves or grants anything.
func HongbaoContributionMembers(ctx context.Context, settings r.SettingsRepository, g *Guild) ([]Member, error) {
	reader, ok := settings.(r.GuildSettlementReceiptReader)
	if !ok || g == nil {
		return nil, ErrRepositoryUnavailable
	}
	members := SortedMembers(g)
	if len(members) == 0 {
		return members, nil
	}
	since := members[0].JoinedAt
	for _, m := range members {
		if m.JoinedAt.Before(since) {
			since = m.JoinedAt
		}
	}
	totals := map[string]uint64{}
	err := reader.WalkGuildSettlementReceipts(ctx, since, func(record r.SettingsRecord) error {
		var receipt struct {
			Version      int
			Owner        r.GuildOwner
			GuildDungeon DungeonClearResult
		}
		if json.Unmarshal([]byte(record.Values["receipt"]), &receipt) != nil || receipt.Version != 1 || record.UpdatedAt.IsZero() {
			return ErrContributionState
		}
		d := receipt.GuildDungeon
		if d.GuildID != g.ID || d.Contribution == 0 {
			return nil
		}
		m, found := g.Members[receipt.Owner.CharacterID]
		if !found || m.AccountID != receipt.Owner.AccountID || record.UpdatedAt.Before(m.JoinedAt) {
			return nil
		}
		totals[m.CharacterID] += uint64(d.Contribution)
		if totals[m.CharacterID] > uint64(m.ContributionPoints) {
			return ErrContributionState
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range members {
		members[i].ContributionPoints -= uint32(totals[members[i].CharacterID])
	}
	return members, nil
}
