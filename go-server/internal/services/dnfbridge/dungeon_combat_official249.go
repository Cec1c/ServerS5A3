package dnfbridge

import "longheng.io/server/internal/modules/dnf/dungeoncmd"

func (s *Service) decodeCurrentDungeonMonsterDeath(body []byte) (dungeoncmd.DieMonsterRequest, error) {
	if s.options.currentClientVersion == "official-24.9" {
		return dungeoncmd.DecodeOfficial249DieMonsterRequest(body)
	}
	return dungeoncmd.DecodeDieMonsterRequest(body)
}
