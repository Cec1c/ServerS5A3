package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestPVPTotalEntryUsesNativeChannelNumbersWithoutTournamentEvent(t *testing.T) {
	// The actual tab click at 1EA4EDB calls 1CF3800, which accepts native
	// channel numbers 75..77. Type 41 only selects the DPL preliminary UI.
	for _, tc := range []struct {
		id   int
		kind byte
		mode uint16
	}{{74, 8, 0}, {75, 8, 7}, {76, 8, 7}, {77, 8, 7}, {78, 8, 0}, {501, 8, 0}, {505, 41, 0}, {75, 31, 0}, {75, 13, 0}} {
		session := &gameSession{channel: channelcatalog.Channel{ID: tc.id, Type: tc.kind, Group: "pvp"}}
		if got := currentPVPMatchingGameType(session); got != tc.mode {
			t.Errorf("native channel %d/type%d: matching mode %d, want %d", tc.id, tc.kind, got, tc.mode)
		}
	}
	// 注：参考实现此处还有一段断言，验证普通总战绩不会启用"预选锦标赛"入口
	// （buildCurrentOnlineEventInfoBody / readRuinsPortalTestDirectory）。那属于在线活动 +
	// 神秘遗迹目录系统，不是决斗场代码；本树未移植该链路，故这段断言一并去掉。
}
