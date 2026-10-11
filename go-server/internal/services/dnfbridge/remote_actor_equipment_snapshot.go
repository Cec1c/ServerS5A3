package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
)

// A peer projection must not turn a later Equipment.Load failure into an
// empty full mode1 after publishing healthy mode0 appearance. Pin the actual
// actor's repository record for this construction, before sending any frame.
// Healthy found=false remains an authoritative empty worn set.
func (s *Service) currentRemoteActorEquipmentSnapshot(ctx context.Context, actor *gameSession, characterID uint16) (context.Context, error) {
	if actor == nil || characterID == 0 || actor.selectedCharacterID != characterID {
		return ctx, fmt.Errorf("remote actor equipment snapshot has stale selected character: requested=%d", characterID)
	}
	ctx, _ = withEquipMode0RepositoryMemo(ctx)
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Equipment == nil {
		return ctx, fmt.Errorf("remote actor equipment snapshot repository unavailable: character=%d", characterID)
	}
	if _, _, err := repos.Equipment.Load(ctx, strconv.Itoa(int(characterID))); err != nil {
		return ctx, fmt.Errorf("load remote actor equipment snapshot character=%d: %w", characterID, err)
	}
	return ctx, nil
}
