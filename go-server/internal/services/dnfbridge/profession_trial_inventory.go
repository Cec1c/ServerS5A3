package dnfbridge

import (
	"strconv"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 现用EXE的E7E080/E7D7F0持有原书对象指针，再由25ECE10按指针查槽。
// op13/list0的2024CF0→25FEB80→25F3F10会释放原行对象；相同道具再发
// 也不会保留对象身份，旧地址被其它行复用时会提交364/365等错误来源。
// 仅规范7142单人体验沿用本连接已成功写出的当前角色主背包；其它容器、
// 冷入口、其它副本及现有资产增量通知保持原流程，不猜测或替换502的槽位。
func (s *Service) sendCurrentProfessionTrialInventoryBootstrap(
	session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, source string,
) error {
	preserve := false
	if session != nil && session.selectedCharacterID != 0 &&
		session.selectedMainInventoryCharacterID == session.selectedCharacterID && runtime != nil {
		session.dungeon.mu.Lock()
		if session.dungeon.runtime == runtime && runtime.Session != nil &&
			currentDungeonRoomOwnsScene(runtime, scene) {
			run := runtime.Session.Snapshot().Run
			preserve = run.Status == worldmap.DungeonRunActive && run.DungeonID == 7142 &&
				runtime.Dungeon.ID == 7142 && runtime.Request.DungeonID == 7142 &&
				runtime.Request.Difficulty == 0 && runtime.Request.RuntimeState == 1 &&
				runtime.Request.RuntimeToken == 0xffff && runtime.partyInstance == nil &&
				runtime.Character.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) &&
				runtime.AccountID == s.accountIDForSession(session)
		}
		session.dungeon.mu.Unlock()
	}
	return s.sendCurrentSelectInventoryBootstrapWithMainPreserved(session, source, preserve)
}
