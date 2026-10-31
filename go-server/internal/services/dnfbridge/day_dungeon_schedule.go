package dnfbridge

import dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"

// 现用24.9客户端的0x203d240消费者依次读四个u32：类别、状态、ID、附加值。
// 类别0为地下城；0x20705c0接受状态1或2。108的星期活动不能替代这张入口表。
const currentDayDungeonScheduleMsgID uint16 = 855

func buildCurrentDayDungeonScheduleBody(dungeonID uint32, open bool) []byte {
	var writer packetWriter
	writer.writeUint32(0)
	writer.writeUint32(boolUint32(open))
	writer.writeUint32(dungeonID)
	writer.writeUint32(0)
	return writer.bytes()
}

// event来自currentDayDungeonEvent的整组真实PVF核对。五图始终完整同步，
// 非开放日写0，清除跨日、换角和重进区域的残留。此处不扣次数。
func (s *Service) sendCurrentDayDungeonSchedule(session *gameSession, event uint16, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 ||
		s.options.currentClientVersion != "official-24.9" || event < 2418 || event > 2424 {
		return nil
	}
	schedules := dnfdungeon.DayDungeonSchedules()
	var opened []uint32
	for id := int64(4906); id <= 4910; id++ {
		open := false
		for _, day := range schedules[id] {
			open = open || day == int64(event)
		}
		if err := s.sendCurrentSceneFixedClass0Packet(session, currentDayDungeonScheduleMsgID,
			buildCurrentDayDungeonScheduleBody(uint32(id), open), source+"_周常副本开放状态"); err != nil {
			return err
		}
		if open {
			opened = append(opened, uint32(id))
		}
	}
	s.logGameEvent(session, "周常副本开放状态已下发", "char_id", session.selectedCharacterID,
		"event_id", event, "open_dungeons", opened, "msg_id", currentDayDungeonScheduleMsgID,
		"reset", "06:00_UTC+8", "source", source)
	return nil
}

// 城镇加载完成后重发，恢复场景切换时重建的客户端入口管理器。
func (s *Service) sendCurrentDayDungeonTownSchedule(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || session.dungeon.runtime != nil {
		return nil
	}
	session.townMu.Lock()
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	if position.CharacterID != session.selectedCharacterID || position.TownID != 107 || position.AreaID != 0 {
		return nil
	}
	return s.sendCurrentDayDungeonSchedule(session, s.currentDayDungeonEvent(), source)
}
