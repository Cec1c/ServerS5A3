package dnfbridge

import (
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"longheng.io/server/internal/modules/dnf/raid"
)

// 跟随终局意图冻结原生播报，提交重试期间即使攻坚队已经解散也能正常通知。
func prepareCurrentRaidClearNotice(state runtimeRaidState) []byte {
	battle := state.Battle
	leader := runtimeRaidMemberIndex(state, state.LeaderID)
	if leader < 0 {
		return nil
	}
	raidName := state.NameBytes
	if battle.Single {
		raidName = nil // 原生单人文案只使用团长名。
	} else if len(raidName) > raid.RaidClearAnnouncementNameMaxBytes {
		// 创建攻坚队允许的编码长度大于 626。仅缩短播报里展示的团名，保留完整字符。
		text, err := simplifiedchinese.GB18030.NewDecoder().String(string(raidName))
		if err != nil {
			return nil
		}
		var name [raid.RaidClearAnnouncementNameMaxBytes]byte
		n, _, err := simplifiedchinese.GB18030.NewEncoder().Transform(name[:], []byte(strings.TrimSpace(text)), true)
		if err != nil && err != transform.ErrShortDst {
			return nil
		}
		raidName = name[:n]
	}
	leaderName, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(state.Members[leader].Name))
	if err != nil {
		return nil
	}
	body, _ := raid.BuildRaidClearAnnouncementBody(battle.Mode, raidName, leaderName, battle.Single)
	return body
}

func (s *Service) publishCurrentRaidClearNotice(instanceID string) {
	s.raidMu.Lock()
	intent := s.raidWeeklyIntents[instanceID]
	if intent == nil || !intent.Committed || len(intent.ClearNotice) == 0 {
		s.raidMu.Unlock()
		return
	}
	body := intent.ClearNotice
	intent.ClearNotice = nil
	s.raidMu.Unlock()

	const event = "团本通关全服播报"
	for _, target := range s.onlineGameSessionCharacterSnapshots() {
		if target.session.conn == nil {
			continue
		}
		err := s.postCurrentGameNotice(target, event, func() error {
			if !s.boundGameSessionCharacterCurrent(target) {
				return nil
			}
			return s.sendGameUpperRawClassCodec(target.session, raid.RaidClearAnnouncementMsgID, body, 0, true)
		})
		if err != nil {
			s.logGameEvent(target.session, "团本通关全服播报投递失败", "instance_id", instanceID, "error", err)
		}
	}
}
