package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 开战(666) 的服务端入场材料闸门。V24 将逐成员扣票改为整队事务，
// 并在 startRuntimeRaidWithEntryCost 的队长、成员和 Started 校验之后执行。
//
// 材料规则来自 raid.LoadAdmissionRule 读取的真实 PVF
// etc/raid/anton.etc / etc/raid/luke/luke.etc，与 658 查询同源。
//
// 三档 env（与 item_slot_family_guard 同样的取舍：先观测、再拦）：
//
//	DNFBRIDGE_RAID_START_ENTRY_COST_GUARD = log（默认）| enforce | off
//	  log     : 只读材料校验并写日志；不扣材料、不因材料判定拒绝合法首次开战
//	  enforce : 任一成员不足 ⇒ 回失败且**不**标记开始；全员齐备 ⇒ 整队同一事务扣除后再开始
//	  off     : 跳过材料校验（零额外日志、零额外读库）
//
// log 档依赖失败仅记录；enforce 档依赖失败拒绝开战。
// 强制档必须能够验证并提交全员材料，不能在错误路径放行。
const raidStartEntryCostGuardEnv = "DNFBRIDGE_RAID_START_ENTRY_COST_GUARD"

const (
	raidStartEntryCostGuardLog     = "log"
	raidStartEntryCostGuardEnforce = "enforce"
	raidStartEntryCostGuardOff     = "off"
)

// raidStartEntryCostGuardMode 解析档位。只有显式写回退字面量才关闭。
func raidStartEntryCostGuardMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(raidStartEntryCostGuardEnv))) {
	case raidStartEntryCostGuardEnforce, "1", "true", "on":
		return raidStartEntryCostGuardEnforce
	case raidStartEntryCostGuardOff, "0", "false", "no", "disable", "disabled", "legacy", "none":
		return raidStartEntryCostGuardOff
	default:
		return raidStartEntryCostGuardLog
	}
}

// The official client uses the production admission rule. Observation and
// rollback switches belong to the legacy profile; they must not authorize an
// official raid without committing every member's actual entry material.
func (s *Service) raidStartEntryCostMode() string {
	if s != nil && s.options.currentClientVersion == "official-24.9" {
		return raidStartEntryCostGuardEnforce
	}
	return raidStartEntryCostGuardMode()
}

// raidEntryCostOwnedCount 统计一个角色**主背包(list 0)**里该材料的总数，
// 上限 uint16（与 658 状态体同口径）。抽出来是为了让 658 与开战闸门用同一个读法。
func raidEntryCostOwnedCount(inventory dnfrepo.InventoryRecord, itemID int64) int64 {
	if itemID <= 0 {
		return 0
	}
	count := int64(0)
	for key, stack := range inventory.Slots {
		list, slot, valid := parseSceneInventorySlotKey(key)
		if valid && list == 0 && slot >= 0 && stack.ItemID == itemID && stack.Count > 0 {
			count += min(stack.Count, int64(math.MaxUint16)-count)
		}
	}
	return count
}

// raidStartMemberCost 是一个成员当前的持有量（纯数据，便于单测）。
type raidStartMemberCost struct {
	CharacterID uint16
	Owned       int64
}

// evaluateRaidStartCost 是纯判定：够/不够 + 谁不够。
func evaluateRaidStartCost(rule raid.AdmissionRule, members []raidStartMemberCost) (statuses []raid.EntryCostStatus, short []uint16) {
	statuses = make([]raid.EntryCostStatus, 0, len(members))
	for _, member := range members {
		owned := member.Owned
		if owned < 0 {
			owned = 0
		}
		if owned > int64(math.MaxUint16) {
			owned = int64(math.MaxUint16)
		}
		ready := owned >= rule.Count
		statuses = append(statuses, raid.EntryCostStatus{
			CharacterID: member.CharacterID,
			Ready:       ready,
			OwnedCount:  uint16(owned),
		})
		if !ready {
			short = append(short, member.CharacterID)
		}
	}
	return statuses, short
}

// raidStartEntryCostMembers 只读地读出每个成员当前的持有量。
func (s *Service) raidStartEntryCostMembers(ctx context.Context, state runtimeRaidState, rule raid.AdmissionRule) ([]raidStartMemberCost, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil || repos.Character == nil {
		return nil, fmt.Errorf("raid entry cost repositories unavailable")
	}
	members := make([]raidStartMemberCost, 0, len(state.Members))
	for _, member := range state.Members {
		id := strconv.FormatUint(uint64(member.CharID), 10)
		inventory, found, err := repos.Inventory.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			// 没有背包记录 ⇒ 持有量按 0 计（不伪造记录，也不放过判定）。
			members = append(members, raidStartMemberCost{CharacterID: member.CharID})
			continue
		}
		members = append(members, raidStartMemberCost{
			CharacterID: member.CharID,
			Owned:       raidEntryCostOwnedCount(inventory, int64(rule.ItemID)),
		})
	}
	return members, nil
}

// consumeRaidEntryCostForMember retains the single-owner test/helper entrypoint.
// Production raid admission uses the same group transaction for every member.
func (s *Service) consumeRaidEntryCostForMember(ctx context.Context, accountID, characterID string, rule raid.AdmissionRule) (int64, error) {
	return s.consumeRaidEntryCostForCharacters(ctx, []string{characterID}, characterID, accountID, rule)
}

func (s *Service) consumeRaidEntryCostForCharacters(ctx context.Context, ids []string, leaderID, leaderAccount string, rule raid.AdmissionRule, holdings ...raid.AdmissionRule) (int64, error) {
	return s.consumeRaidEntryCostAndDaily(ctx, ids, leaderID, leaderAccount, rule, nil, holdings...)
}

func (s *Service) consumeRaidEntryCostAndDaily(ctx context.Context, ids []string, leaderID, leaderAccount string, rule raid.AdmissionRule, daily *currentRaidDailyRule, holdings ...raid.AdmissionRule) (int64, error) {
	if rule.ItemID == 0 || rule.Count <= 0 || strings.TrimSpace(leaderAccount) == "" {
		return 0, fmt.Errorf("raid entry cost rule or owner unsupported")
	}
	ids, err := dnfrepo.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return 0, err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return 0, fmt.Errorf("raid entry cost repositories unavailable")
	}
	unit, ok := repos.CharacterTrade.(dnfrepo.CharacterInventoryGroupUnitOfWork)
	if !ok {
		return 0, fmt.Errorf("raid group inventory transaction unavailable")
	}
	mutate := func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, settings dnfrepo.SettingsRepository) error {
		// Stable owner order matches other trade transactions. Validate every member
		// under row locks before saving; a late shortage or save error rolls back all.
		updated := make([]dnfrepo.InventoryRecord, 0, len(ids))
		accounts := make([]string, 0, len(ids))
		for _, id := range ids {
			character, found, err := characters.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || character.CharacterID != id || strings.TrimSpace(character.AccountID) == "" || (id == leaderID && character.AccountID != leaderAccount) {
				return fmt.Errorf("raid entry cost character/account mismatch: %s", id)
			}
			inventory, found, err := inventories.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || inventory.CharacterID != id {
				return fmt.Errorf("raid entry cost inventory missing: %s", id)
			}
			inventory = dnfrepo.CloneInventory(inventory)
			for _, holding := range holdings {
				if holding.ItemID == 0 || holding.Count <= 0 || raidEntryCostOwnedCount(inventory, int64(holding.ItemID)) < holding.Count {
					return fmt.Errorf("raid member %s: entrance holding requirement not met", id)
				}
			}
			if err := takeRaidEntryCost(&inventory, rule); err != nil {
				return fmt.Errorf("raid member %s: %w", id, err)
			}
			updated = append(updated, inventory)
			accounts = append(accounts, character.AccountID)
		}
		if daily != nil {
			record, _, err := settings.Load(ctx, daily.Scope)
			if err != nil {
				return err
			}
			next, err := reserveCurrentRaidDaily(record, accounts, daily)
			if err != nil {
				return err
			}
			if err := settings.Save(ctx, next); err != nil {
				return err
			}
		}
		for _, inventory := range updated {
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		return nil
	}
	if daily != nil {
		assets, ok := repos.CharacterTrade.(dnfrepo.RaidEntryAssetUnitOfWork)
		if !ok {
			return 0, fmt.Errorf("团本日次数与门票原子事务不可用")
		}
		err = assets.WithinRaidEntryAssets(ctx, ids, daily.Scope, mutate)
	} else {
		err = unit.WithinCharacterInventoryGroup(ctx, ids, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository) error { return mutate(c, i, nil) })
	}
	if err != nil {
		return 0, err
	}
	return rule.Count * int64(len(ids)), nil
}

func takeRaidEntryCost(inventory *dnfrepo.InventoryRecord, rule raid.AdmissionRule) error {
	owned := raidEntryCostOwnedCount(*inventory, int64(rule.ItemID))
	if owned < rule.Count {
		return fmt.Errorf("entry cost short at commit: owned=%d required=%d", owned, rule.Count)
	}
	remaining := rule.Count
	keys := make([]string, 0, len(inventory.Slots))
	for key := range inventory.Slots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if remaining == 0 {
			break
		}
		list, slot, valid := parseSceneInventorySlotKey(key)
		stack := inventory.Slots[key]
		if !valid || list != 0 || slot < 0 || stack.ItemID != int64(rule.ItemID) || stack.Count <= 0 {
			continue
		}
		take := min(stack.Count, remaining)
		stack.Count -= take
		remaining -= take
		if stack.Count == 0 {
			delete(inventory.Slots, key)
		} else {
			inventory.Slots[key] = stack
		}
	}
	if remaining != 0 {
		return fmt.Errorf("entry cost incomplete: remaining=%d", remaining)
	}
	return nil
}

// Caller holds raidMu through authorization, group commit and Started update.
// log/off keep their no-write behavior; enforce fails closed on every dependency.
func (s *Service) guardRaidStartEntryCost(session *gameSession, state runtimeRaidState) (bool, error) {
	mode := s.raidStartEntryCostMode()
	if mode == raidStartEntryCostGuardOff {
		return true, nil
	}
	dependencyFailure := func(reason string, err error) (bool, error) {
		decision := "allow"
		if mode == raidStartEntryCostGuardEnforce {
			decision = "reject"
		}
		s.logGameEvent(session, "game-raid-start-entry-cost-guard", "decision", decision, "mode", mode, "raid_key", state.RaidKey, "reason", reason, "error", err)
		if mode == raidStartEntryCostGuardEnforce {
			return false, err
		}
		return true, nil
	}
	rule, err := s.currentRaidAdmissionRule(session)
	if err != nil {
		return dependencyFailure("rule_unavailable", err)
	}
	if mode == raidStartEntryCostGuardEnforce {
		minimum := rule.MinimumMembers
		single := state.TailFlag
		if state.CurrentLayout {
			single = state.CurrentUIFlags[0]
		}
		if single == 1 {
			minimum = rule.SoloMinimumMembers
		}
		if single > 1 || len(state.Members) < minimum {
			return dependencyFailure("raid_member_minimum", fmt.Errorf("members=%d minimum=%d solo_flag=%d", len(state.Members), minimum, single))
		}
		if state.CurrentLayout {
			if rule.MaximumMembers < minimum || rule.MaximumMembers > raid.MaxAttackPartyMembers || len(state.Members) > rule.MaximumMembers || (single == 1 && len(state.Members) != 1) {
				return dependencyFailure("current_raid_member_maximum", fmt.Errorf("members=%d configured_max=%d single=%d", len(state.Members), rule.MaximumMembers, single))
			}
			for _, member := range state.Members {
				if int(member.GroupIndex) > (rule.MaximumMembers+runtimeRaidGroupSize-1)/runtimeRaidGroupSize {
					return dependencyFailure("current_raid_group_maximum", fmt.Errorf("group exceeds actual PVF maximum"))
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	members, err := s.raidStartEntryCostMembers(ctx, state, rule)
	if err != nil {
		return dependencyFailure("member_read_failed", err)
	}
	statuses, short := evaluateRaidStartCost(rule, members)
	owned := make([]string, 0, len(statuses))
	for _, status := range statuses {
		owned = append(owned, fmt.Sprintf("%d:%d/%d", status.CharacterID, status.OwnedCount, rule.Count))
	}
	decision, reason := "allow", "all_members_ready"
	if len(short) != 0 {
		reason = "entry_cost_short"
		decision = "observe"
		if mode == raidStartEntryCostGuardEnforce {
			decision = "reject"
		}
	}
	s.logGameEvent(session, "game-raid-start-entry-cost-guard", "decision", decision, "mode", mode, "raid_key", state.RaidKey, "item_id", rule.ItemID, "required", rule.Count, "member_count", len(statuses), "short_count", len(short), "owned", strings.Join(owned, ","), "reason", reason)
	if decision == "reject" {
		return false, nil
	}
	if mode != raidStartEntryCostGuardEnforce {
		return true, nil
	}
	ids := make([]string, len(members))
	for i, member := range members {
		ids[i] = strconv.Itoa(int(member.CharacterID))
	}
	holdings, err := s.currentRaidHoldingRequirements(ctx, session, state)
	if err != nil {
		return dependencyFailure("entrance_requirements", err)
	}
	var daily *currentRaidDailyRule
	if s.options.currentClientVersion == "official-24.9" {
		mode := byte(0)
		if session.channel.Type == dnfenum.RaidChannelType {
			mode = 1
		}
		if session.channel.Type == dnfenum.HiddenRaidType {
			mode = 2
		}
		if mode != 0 {
			daily, err = s.currentRaidDailyRule(mode)
			if err != nil {
				return dependencyFailure("daily_rule_unavailable", err)
			}
		}
	}
	consumed, err := s.consumeRaidEntryCostAndDaily(ctx, ids, strconv.Itoa(int(state.LeaderID)), s.accountIDForSession(session), rule, daily, holdings...)
	if err != nil {
		return dependencyFailure("group_commit_failed", err)
	}
	s.logGameEvent(session, "game-raid-start-entry-cost-consumed", "raid_key", state.RaidKey, "item_id", rule.ItemID, "required_each", rule.Count, "members", len(members), "consumed_total", consumed, "transaction", "all_members")
	return true, nil
}
