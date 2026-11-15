package guild

import (
	"context"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// ReadGuildID returns the membership-checked ID from one immutable source
// version. It is a routing/precondition hint, never mutation authorization:
// the write transaction must recheck its actor, membership and permissions.
// No mutable Guild, registry history or saveable partial Registry escapes.
func ReadGuildID(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner) (uint32, bool, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if err != nil {
		return 0, false, err
	}
	// Preserve GuildForOwner's character normalization and account identity.
	owner.CharacterID = strings.TrimSpace(owner.CharacterID)
	g := snapshot.owned(owner)
	if g == nil || owner.AccountID == "" {
		return 0, false, nil
	}
	return g.ID, true, nil
}
