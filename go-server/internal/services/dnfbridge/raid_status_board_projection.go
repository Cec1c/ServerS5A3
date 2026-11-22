package dnfbridge

import (
	"crypto/sha256"
	"sort"

	"longheng.io/server/internal/modules/dnf/raid"
)

// buildCurrentRaidStatusBoardSquads 将当前攻坚队编队与活跃副本进度投影为
// 客户端 S2C 634 (0x027A) 攻坚队状况表所需的按小队聚合快照。
// 纯只读投影，不修改任何团本或普通队伍状态。
func buildCurrentRaidStatusBoardSquads(state runtimeRaidState) []raid.StatusBoardSquad {
	if len(state.Members) == 0 {
		return nil
	}
	members := append([]runtimeRaidMemberState(nil), state.Members...)
	runtimeRaidSortMembers(members)

	byGroup := make(map[byte][]runtimeRaidMemberState, runtimeRaidMaxGroups)
	for _, member := range members {
		if member.CharID == 0 {
			continue
		}
		group := member.GroupIndex
		if group == 0 {
			continue
		}
		byGroup[group] = append(byGroup[group], member)
	}
	if len(byGroup) == 0 {
		return nil
	}

	groupKeys := make([]int, 0, len(byGroup))
	for group := range byGroup {
		groupKeys = append(groupKeys, int(group))
	}
	sort.Ints(groupKeys)

	b := state.Battle
	squads := make([]raid.StatusBoardSquad, 0, len(groupKeys))
	for _, groupKey := range groupKeys {
		groupMembers := byGroup[byte(groupKey)]
		charIDs := make([]uint16, 0, len(groupMembers))
		var usedCoins uint32
		coinResolved := false
		for _, member := range groupMembers {
			charIDs = append(charIDs, member.CharID)
			if !coinResolved && b != nil && len(b.Participation) > 0 {
				if entry, ok := b.Participation[member.CharID]; ok && entry.RunID != "" && entry.Phase == b.Phase {
					usedCoins = b.RunCoins[entry.RunID]
					coinResolved = true
				}
			}
		}
		squads = append(squads, raid.StatusBoardSquad{
			GroupIndex:      uint16(groupKey),
			MemberCharIDs:   charIDs,
			UsedCoins:       usedCoins,
			DungeonProgress: 0,
		})
	}
	return squads
}

// currentRaidStatusBoardBody 编码 S2C 634 正文并返回去重签名。
func currentRaidStatusBoardBody(state runtimeRaidState) ([]byte, [32]byte, error) {
	squads := buildCurrentRaidStatusBoardSquads(state)
	body, err := raid.BuildStatusBoardBody(squads)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return body, sha256.Sum256(body), nil
}
