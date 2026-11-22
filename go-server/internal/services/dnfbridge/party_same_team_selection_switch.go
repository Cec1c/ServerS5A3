package dnfbridge

import (
	"os"
	"strings"
)

// samePartyTownSelectionNoticeEnv 控制城镇中右键同队队员（mode=15）时，
// 在 shouldSkipTownCopresencePartyFrame 跳过 op9 抢绑护栏之后是否继续下发 0x0007 选择通知：
//   - 默认（空 / off / 0 / false / legacy）：保持基线原行为（直接 return nil，不发 0x0007）；
//   - on / 1 / true / yes：保留“跳过 op9 避免抢绑 [localPlayer+0x4C8]”护栏不变，
//     仅补发 0x0007 BuildRequestPeerSelectionNotice，让客户端正常弹出队员右键菜单。
const samePartyTownSelectionNoticeEnv = "DNFBRIDGE_SAME_PARTY_TOWN_SELECTION_NOTICE"

func samePartyTownSelectionNoticeEnabled() bool {
	return parseSamePartyTownSelectionNoticeEnabled(os.Getenv(samePartyTownSelectionNoticeEnv))
}

func parseSamePartyTownSelectionNoticeEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}
