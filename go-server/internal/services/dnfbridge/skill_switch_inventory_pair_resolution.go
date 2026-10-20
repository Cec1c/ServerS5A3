package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「Buff 强化 / 技能换装」重发帧里**每一组 pair 的 value 到底指得到什么**：只读诊断
// （`DNFBRIDGE_SKILL_SWITCH_PAIR_RESOLUTION`）。
//
// # 为什么需要它（第五轮：`BINDING_RESTORE` 上线后界面仍然为空）
//
// 第五轮已确证：还原档真的在跑（`binding_restore=restore restored_count=11
// restored_bindings=slot_12=0c003600,…`），重发帧也确实带上了操作者那 11 条绑定
// （`pairs=(12,54)(13,62)…(23,55)`，逐字见 `_dist/buff-swap-round5.md` §1），
// 但界面仍然空。剩下的分歧只有一处：**value 这个数到底该在哪个容器里查**。
//
//   - 若 value 是**主背包（list 0）格号**：char 6 的 11 个 value `54..64` 与
//     `0:54..0:64` 那 11 件装备的**类型**逐一吻合（11/11，第四轮 SQL）；
//   - 若 value 是**换装栏/装扮容器（list 1）格号**：char 1 在 18:38:59Z 亲手绑的
//     `slot 12 -> 20` 只在 `1:20` 查得到，而 `0:20` **根本不存在**。
//
// 两种读法各自都有一份现场证据，**报文本身分不出来**。这个探针把
// "同一组 pair 在 list 0 / list 1 里各查得到几组、查到的是什么物品"直接写进
// `packet_log.txt`，于是下一次实机**只靠日志**就能判定该往哪个容器查
// （以及 value 是否根本查不到任何东西）。
//
// # 契约（与其它档一致）
//
//   - **不改任何字节**：帧的构造、内容、顺序与开关无关；本档只多发**一条 EVENT**；
//   - 默认档 `resolve` = 记这条 EVENT；回退字面量 `off`（别名见下）= **一个字段都不多写**
//     （与改动前逐字节相同的日志）；
//   - 仓储缺失 / 角色没有背包记录 ⇒ 只记 `inventory=missing`，绝不因此中断边界；
//   - 只读：`repos.Inventory.Load` 一个写路径都不碰。
const skillSwitchPairResolutionEnv = "DNFBRIDGE_SKILL_SWITCH_PAIR_RESOLUTION"

const (
	// skillSwitchPairResolutionResolve 是**默认档（诊断档）**。
	skillSwitchPairResolutionResolve = "resolve"
	// skillSwitchPairResolutionOff 是**一键回退字面量**：不记本 EVENT。
	skillSwitchPairResolutionOff = "off"
)

// skillSwitchPairResolutionEvent 是本档唯一的新增日志行。
const skillSwitchPairResolutionEvent = "game-skill-switch-inventory-pair-resolution"

// normalizeSkillSwitchPairResolution 归一化本档（只有回退字面量才离开诊断档）。
func normalizeSkillSwitchPairResolution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchPairResolutionOff, "today", "live", "legacy", "rollback", "0", "disable", "none":
		return skillSwitchPairResolutionOff
	default:
		return skillSwitchPairResolutionResolve
	}
}

// currentSkillSwitchPairResolution 直接自读 env（本档刻意不进 options.go）。
func currentSkillSwitchPairResolution() string {
	return normalizeSkillSwitchPairResolution(os.Getenv(skillSwitchPairResolutionEnv))
}

// skillSwitchPairListNumbers 是本档要对照的容器号：0 = 主背包，1 = 换装栏/装扮容器。
// 两个都查、都写进日志 —— 判据是"哪一边查得到、查到的是什么"，不是猜。
var skillSwitchPairListNumbers = []byte{0, 1}

// skillSwitchPairResolutionRow 是一组 pair 的落点读数。
type skillSwitchPairResolutionRow struct {
	Slot       int
	Value      int
	Resolved   map[byte]string
	Unresolved bool
}

// logSkillSwitchPairResolution 把这一帧每一组 pair 的落点写进日志。
//
// `pairsMode` 与帧构造用的是同一个参数（`record` 档取 `record.Value`，`identity`
// 档取 `record.Slot`），所以日志里的 value 与真正发出去的字节永远一致。
func (s *Service) logSkillSwitchPairResolution(
	session *gameSession,
	characterID string,
	records []skillswitch.Record,
	pairsMode string,
) {
	if s == nil || session == nil || len(records) == 0 {
		return
	}
	if currentSkillSwitchPairResolution() != skillSwitchPairResolutionResolve {
		return
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return
	}
	resolutions := make([]skillSwitchPairResolutionRow, 0, len(records))
	slots := make(map[string]dnfrepo.ItemStack)
	inventoryState := "missing"
	if repos.Inventory != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		record, found, err := repos.Inventory.Load(ctx, characterID)
		switch {
		case err != nil:
			inventoryState = "load_error"
		case !found:
			inventoryState = "missing"
		default:
			inventoryState = "ok"
			for key, stack := range record.Slots {
				slots[key] = stack
			}
		}
	}
	useRecordValue := normalizeSkillSwitchLoadoutPairsMode(pairsMode) != skillSwitchLoadoutPairsIdentity
	for _, record := range records {
		if record.Sentinel() {
			continue
		}
		value := int(record.Slot)
		if useRecordValue {
			value = int(record.Value)
		}
		row := skillSwitchPairResolutionRow{
			Slot:     int(record.Slot),
			Value:    value,
			Resolved: make(map[byte]string, len(skillSwitchPairListNumbers)),
		}
		for _, list := range skillSwitchPairListNumbers {
			key := strconv.Itoa(int(list)) + ":" + strconv.Itoa(value)
			stack, present := slots[key]
			if !present {
				continue
			}
			row.Resolved[list] = skillSwitchPairItemLabel(key, stack)
		}
		row.Unresolved = len(row.Resolved) == 0
		resolutions = append(resolutions, row)
	}
	counts := make([]string, 0, len(skillSwitchPairListNumbers))
	details := make([]string, 0, len(skillSwitchPairListNumbers))
	for _, list := range skillSwitchPairListNumbers {
		resolved := 0
		parts := make([]string, 0, len(resolutions))
		for _, row := range resolutions {
			label, present := row.Resolved[list]
			if !present {
				continue
			}
			resolved++
			parts = append(parts, label)
		}
		counts = append(counts, "list_"+strconv.Itoa(int(list))+"="+strconv.Itoa(resolved))
		if len(parts) == 0 {
			details = append(details, "list_"+strconv.Itoa(int(list))+"_items=-")
			continue
		}
		details = append(details, "list_"+strconv.Itoa(int(list))+"_items="+strings.Join(parts, ","))
	}
	unresolved := make([]string, 0, len(resolutions))
	for _, row := range resolutions {
		if row.Unresolved {
			unresolved = append(unresolved, strconv.Itoa(row.Slot)+"->"+strconv.Itoa(row.Value))
		}
	}
	unresolvedText := "-"
	if len(unresolved) > 0 {
		unresolvedText = strings.Join(unresolved, ",")
	}
	s.logPacketEvent(skillSwitchPairResolutionEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"inventory", inventoryState,
		"pair_rows", len(resolutions),
		"pair_value_source", pairsMode,
		"probe_lists", "0=main_inventory,1=switch_or_avatar_container",
		"resolved_counts", strings.Join(counts, " "),
		"resolved_details", strings.Join(details, " "),
		"unresolved_pairs", unresolvedText,
		"byte_effect", "none_log_only",
		"switch", currentSkillSwitchPairResolution(),
		"switch_env", skillSwitchPairResolutionEnv)
}

// skillSwitchPairItemLabel 把一格物品渲染成 `slot=item:[type]`（缺字段就省掉）。
func skillSwitchPairItemLabel(key string, stack dnfrepo.ItemStack) string {
	label := key + "=" + strconv.FormatInt(stack.ItemID, 10)
	if kind := strings.TrimSpace(stack.Extra["item_kind"]); kind != "" {
		label += "/" + kind
	}
	if equipmentType := strings.TrimSpace(stack.Extra["equipment_type"]); equipmentType != "" {
		label += equipmentType
	}
	return label
}
