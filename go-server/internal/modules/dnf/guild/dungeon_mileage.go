package guild

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// Approved server rule; matches [Guild Mileage for Dungeon Clear] in the PVF.
const OrdinaryDungeonMileage uint32 = 10

type DungeonMileageResult struct {
	GuildID uint32
	Amount  uint32
}

// GrantDungeonMileage is called ONLY inside the settlement transaction, after
// checking its durable completion receipt. Membership is resolved at commit.
// It changes the guild pool, not member mileage, contribution or guild EXP.
func GrantDungeonMileage(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner, now time.Time) (DungeonMileageResult, error) {
	reg, err := Load(ctx, settings)
	if err != nil {
		return DungeonMileageResult{}, err
	}
	g, m, found := reg.GuildForCharacter(owner.CharacterID)
	if !found {
		return DungeonMileageResult{}, nil
	}
	if m.AccountID != owner.AccountID {
		return DungeonMileageResult{}, ErrPermissionDenied
	}
	if g.Mileage > ^uint32(0)-OrdinaryDungeonMileage {
		return DungeonMileageResult{}, ErrMileageOverflow
	}
	if err := g.appendMileageHistory(MileageHistoryEntry{At: now, CharacterID: owner.CharacterID, CharacterName: m.Name, Kind: MileageHistoryDungeon, Amount: OrdinaryDungeonMileage}); err != nil {
		return DungeonMileageResult{}, err
	}
	g.Mileage += OrdinaryDungeonMileage
	g.UpdatedAt = now
	if err := Save(ctx, settings, reg, now); err != nil {
		return DungeonMileageResult{}, err
	}
	return DungeonMileageResult{GuildID: g.ID, Amount: OrdinaryDungeonMileage}, nil
}
