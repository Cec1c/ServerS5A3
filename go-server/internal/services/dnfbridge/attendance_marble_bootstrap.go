package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

// 现用原生图标61查询活动145；629/630为class1回包，641仅为上行。
const currentAttendanceMarbleEventID uint16 = 145

func currentAttendanceMarbleTrainingRoom(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.Dungeon.ID == 5000 && strings.EqualFold(strings.ReplaceAll(runtime.Dungeon.Path, "\\", "/"), "dungeon/PoongjinTrainingRoom/PoongjinTrainingRoom.dgn")
}

func (s *Service) currentAttendanceMarbleAvailable() bool {
	if !s.currentAttendanceMarbleEnabled() {
		return false
	}
	catalog, err := s.currentAttendanceMarbleCatalog()
	if err != nil {
		s.logGameEvent(nil, "大富翁配置不可用", "error", err)
		return false
	}
	return catalog != nil
}

func (s *Service) currentAttendanceMarbleEnabled() bool {
	return s != nil && (s.activityControls == nil || s.activityControls.Enabled(currentAttendanceMarbleEventID))
}

// 不发送会自动打开窗口的629；登录仅恢复剩余地下城Buff。
func (s *Service) restoreAttendanceMarbleBuffs(session *gameSession) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	catalog, err := s.currentAttendanceMarbleCatalog()
	if err != nil {
		s.logGameEvent(session, "大富翁登录恢复暂缓", "error", err)
		return nil
	}
	if catalog == nil {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		s.logGameEvent(session, "大富翁登录状态读取暂缓", "error", err)
		return nil
	}
	if !found || character.AccountID != s.accountIDForSession(session) {
		return nil
	}
	if _, err := onlineevent.ReadMarbleState(character, catalog, s.gameplayNow()); err != nil {
		s.logGameEvent(session, "大富翁登录显示跳过异常状态", "error", err)
		return nil
	}
	for index := 29; index <= 32; index++ {
		if count := character.Stats[attendanceMarbleBuffKey(index)]; count > 0 {
			if err := s.sendAttendanceMarbleBuff(session, index, count); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) activateAttendanceMarbleAfterEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if session == nil || runtime == nil || runtime.Session == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime != runtime {
		return nil
	}
	if err := s.activateAttendanceMarbleDungeonBuffs(session, runtime); err != nil {
		return err
	}
	return nil
}
