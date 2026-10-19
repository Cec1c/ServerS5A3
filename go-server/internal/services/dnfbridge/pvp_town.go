package dnfbridge

import (
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentPVPTownTransition(p *gameSession, id uint16, c repo.CharacterRecord) (repo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	a, ok := s.townArea(10, 0)
	if !ok || a.Gate == nil || a.MapPath == "" || int64(c.Level) < a.MinLevel || a.Gate.X < -32768 || a.Gate.X > 32767 || a.Gate.Y < -32768 || a.Gate.Y > 32767 {
		return repo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("真实PVF决斗城镇入口不可用或等级不足")
	}
	return currentReconnectLocationProjection(id, c, currentTownLoginLocation{TownID: 10, AreaID: 0, MapPath: a.MapPath, PositionX: a.Gate.X, PositionY: a.Gate.Y, Direction: 5, AreaState: 1})
}
