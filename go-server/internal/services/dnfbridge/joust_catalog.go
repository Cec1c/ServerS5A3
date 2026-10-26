package dnfbridge

import (
	"context"

	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
)

func (s *Service) currentJoustCatalog(ctx context.Context) (*dnfjoust.Catalog, error) {
	if s == nil {
		return nil, dnfjoust.ErrRoundUnavailable
	}
	s.joustCatalogMu.Lock()
	if s.joustCatalog != nil || s.joustCatalogLoadErr != nil {
		catalog, err := s.joustCatalog, s.joustCatalogLoadErr
		s.joustCatalogMu.Unlock()
		return catalog, err
	}
	s.joustCatalogMu.Unlock()
	itemCatalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	s.joustCatalogMu.Lock()
	defer s.joustCatalogMu.Unlock()
	if s.joustCatalog != nil || s.joustCatalogLoadErr != nil {
		return s.joustCatalog, s.joustCatalogLoadErr
	}
	s.joustCatalog, s.joustCatalogLoadErr = dnfjoust.LoadCatalog(ctx, itemCatalog.source)
	return s.joustCatalog, s.joustCatalogLoadErr
}

// currentJoustCatalogForPublishing treats an unusable optional-event catalog as
// an unavailable activity, not as a town-initialization failure. It records the
// concrete resource error once at each publishing entry point; callers must
// continue with unrelated protocol traffic and must still return any later
// network write error.
func (s *Service) currentJoustCatalogForPublishing(
	ctx context.Context,
	session *gameSession,
	source string,
) (*dnfjoust.Catalog, bool) {
	catalog, err := s.currentJoustCatalog(ctx)
	if err != nil {
		if s != nil {
			s.logGameEvent(session, "game-joust-catalog-unavailable",
				"source", source,
				"event_id", currentJoustEventID,
				"reason", err.Error())
		}
		return nil, false
	}
	if catalog == nil {
		if s != nil {
			s.logGameEvent(session, "game-joust-catalog-unavailable",
				"source", source,
				"event_id", currentJoustEventID,
				"reason", "joust catalog is nil")
		}
		return nil, false
	}
	return catalog, true
}
