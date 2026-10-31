package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func (s *Service) currentTagTournamentCatalog() (*tagtournament.Catalog, error) {
	if s == nil {
		return nil, fmt.Errorf("帝国竞技场缺少服务实例")
	}
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.tagTournamentCatalog != nil || s.tagTournamentLoadErr != nil {
		return s.tagTournamentCatalog, s.tagTournamentLoadErr
	}
	archive, err := s.initialEquipmentArchiveLocked()
	if err == nil {
		s.tagTournamentCatalog, err = tagtournament.LoadCatalog(archive)
	}
	s.tagTournamentLoadErr = err
	return s.tagTournamentCatalog, err
}
