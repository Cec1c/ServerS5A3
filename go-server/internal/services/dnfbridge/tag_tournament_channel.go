package dnfbridge

import (
	"fmt"
	"strings"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 现用EXE的013F2180按类型39寻找帝国频道；城镇48来自实际town/town.lst。
const currentTagTournamentChannelType uint8 = 39

func (s *Service) isCurrentTagTournamentChannel(p *gameSession) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && p != nil && p.channel.Type == currentTagTournamentChannelType
}

// 仅接城镇查看入口；不创建双角色战斗或结算，也不编造入口坐标。
func (s *Service) currentTagTournamentLoginLocation(c repo.CharacterRecord) (currentTownLoginLocation, error) {
	a, ok := s.townGateArea(48)
	if !ok || a.ID < 0 || a.ID > 255 || a.Gate == nil ||
		!strings.EqualFold(strings.ReplaceAll(a.MapPath, "\\", "/"), "Town/CollosseumofEmpire/CollosseumofEmpire.map") ||
		int64(c.Level) < a.MinLevel || len(a.NeedQuests) != 0 ||
		a.Gate.X < -32768 || a.Gate.X > 32767 || a.Gate.Y < -32768 || a.Gate.Y > 32767 {
		return currentTownLoginLocation{}, fmt.Errorf("帝国竞技场真实PVF入口不可用或角色未满足区域条件")
	}
	return currentTownLoginLocation{TownID: 48, AreaID: byte(a.ID), MapPath: a.MapPath,
		PositionX: a.Gate.X, PositionY: a.Gate.Y, Direction: newCharacterInitialDirection, AreaState: newCharacterInitialAreaState,
		Branch: "帝国竞技场频道城镇", Source: "帝国竞技场真实PVF入口", PositionSource: "真实PVF区域gate"}, nil
}

func (s *Service) currentTagTournamentTownTransition(id uint16, c repo.CharacterRecord) (repo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	location, err := s.currentTagTournamentLoginLocation(c)
	if err != nil {
		return repo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	return currentReconnectLocationProjection(id, c, location)
}
