package dnfbridge

import (
	"errors"
	"fmt"
	"sort"
)

var errCurrentRaidMembersOutsideWaitingArea = errors.New("还有队员未进入本团等候区，无法开始攻坚")

// 调用方持有raidMu；整份在线名单在同一个读锁内校验。
// 初次开团必须尚未绑定战斗实例，第二阶段沿用本场实例。
// 失败发生在扣门票或推进阶段之前，不能只跳过未到位成员继续开团。
func (m *OnlinePlayerManager) requireRaidStartWaitingArea(scope currentRaidWaitingBattleScope, expectedBattle string) error {
	if scope.battleID == "" { // 旧协议不使用当前团本实例。
		return nil
	}
	if m == nil || len(scope.members) == 0 {
		return errCurrentRaidMembersOutsideWaitingArea
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var outside []int
	for id, member := range scope.members {
		key, found := m.byChar[id]
		info := m.byArea[key][id]
		if !found || id == 0 || member.session == nil || member.accountID == "" || key.ChannelID != scope.channelID || key.TownID != scope.townID || key.AreaID != scope.areaID || key.Instance != scope.raidKey || key.RaidBattleInstance != expectedBattle || info == nil || info.CharacterID != id || info.AccountID != member.accountID || info.Session != member.session || info.ChannelID != key.ChannelID || info.TownID != key.TownID || info.AreaID != key.AreaID || info.Instance != key.Instance || info.RaidBattleInstance != key.RaidBattleInstance {
			outside = append(outside, int(id))
		}
	}
	if len(outside) != 0 {
		sort.Ints(outside)
		return fmt.Errorf("%w：角色%v", errCurrentRaidMembersOutsideWaitingArea, outside)
	}
	return nil
}
