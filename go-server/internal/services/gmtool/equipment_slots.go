package gmtool

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是「开启特殊装备槽（左/右槽、耳环槽）」的唯一实现。
//
// 数据面（真档 Script.pvf 全量实测 + 线上库实测）：
//
//	持久列只有一个：dnf_characters.ex_equip_slot_stat（TINYINT，见
//	repository/mysql/mysql_char.go:47）。没有 dimension 表、没有独立的槽位表，
//	「开左槽 / 开右槽 / 开耳环槽」在数据上是同一张表同一个字段的不同 bit。
//
//	bit 与 PVF 授权任务都由运行时 PVF 决定，写死在工具里会随 PVF 换代而失效：
//	  - modules/dnf/quest/catalog.go:1281-1303 是服务端权威位表：
//	    support=0x01、magic stone=0x02、earring=0x10、全部=0x13
//	    （当前客户端的位**不连续**：耳环是 bit4 而不是 bit2）；
//	  - 真档 `[reward type] `[slot expansion]`` 的任务只有三个，索引写在
//	    `[reward int data]`：0=support(649)、1=magic stone(650)、2=earring(2636)。
//
// 这里刻意不复制那三个任务 id：id 只作为「PVF 里核对用的锚点」出现，真正写库的
// id 一律来自运行时 PVF 的 `[reward type] `[slot expansion]`` + `[reward int data]`
// 索引，所以 PVF 换版后本功能自动跟着走（对不上就直接报错，不会写错位）。
//
// 历史缺陷（本文件修复的）：旧实现无条件写 `ex_equip_slot_stat = 7`，即
// 0x01|0x02 —— 对 90CN 客户端来说它只开了左槽与右槽，**耳环槽（0x10）从来没被
// 打开过**；而它同时又用 `&7 == 7` 判定「已全部开启」，于是点了按钮会提示成功、
// 实际漏开耳环位。线上 Lv90 角色的 ex_equip_slot_stat 至今只落在 {7,16,19,23}
// 四个值上（2026-09-29 实测，见 _dist/earring-slot-quest.md 的 SQL 原文），
// dnf_equipment_entries 里也没有任何 slot_index=25（耳环）行，就是这个原因。
//
// 写库范围（默认档 = 位 + 整条授权链的任务行）：四个**独立参考实现**都只写
// `ex_equip_slot_stat` 的位，一行任务都不碰 ——
//
//	1. A21 C# 工具 `Services/GmService.Characters.cs:450,476`：`SET ex_equip_slot_stat =
//	   7`，没有任务写入；
//	2. 上游 0.1.8 `internal/services/dnfbridge/gm.go:445-503`
//	   （`GMUnlockExtraEquipmentSlots`）：`current | unlockMask` 后只
//	   `SaveCharacterFields(..., CharacterFieldStats)`，同样不碰任务；
//	3. 本仓既有 `cmd/scratch/grantlevel90loadout/main.go:219`：
//	   `UPDATE dnf_characters SET ex_equip_slot_stat = ex_equip_slot_stat | 0x13`；
//	4. 90CN 的新版供货方 GM 工具 `GmTool90CN/GmService90.cs:1296-1312`：
//	   `ex_equip_slot_stat | 0x13`，注释明写「只做位或」。
//
// **但现场不接受这个口径**：运维实测（2026-09-29 05:0x）「gm工具开启耳环槽后
// 任务依然存在」，配图是外传「穿戴耳环 (1/2)」（任务 2635）。开槽的位写完之后
// 那条授权链的前置任务仍然 active，客户端任务手册里就挂着一条永远做不完的外传。
// 所以本文件的**默认档改为「位 + 授权的槽位任务 + 它的前置链一起 completed」**
// （链路见 modules/dnf/quest/slot_expansion_chain.go）：
//
//	2635 `穿戴耳环 (1/2)`（[side] [hunt monster]，[attribute] slot expansion）
//	  -> 2636 `穿戴耳环 (2/2)`（[reward type] `[slot expansion]` [reward int data] 2）
//
// 逐字节回退：`DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN=legacy`（见下面
// EquipSlotQuestChainFromEnv）时回到改动前的语义 —— 只写位，且只有
// `DNFBRIDGE_GM_EQUIP_SLOT_QUEST_SYNC=on` 才额外写**授权任务本身**（不含前置链）。
// 这条回退档的取舍（跳过任务本身的收集流程、不补发任务 EXP）与改动前完全一样。
//
// 本文件唯一**不能**从参考实现照抄的是位掩码：A21 自己的口径是左槽 0x1 / 右槽 0x2 /
// 耳环 0x4 / 全开 7（A21 服务端的槽位空间），90CN 客户端是 0x01 / 0x02 / 0x10 /
// 全开 0x13。两者**不要互相抄**。

// 特殊装备槽位掩码：必须与 modules/dnf/quest 的 ExEquipSlot* 常量保持同一口径。
// 这里不直接引用 dnfquest 的常量，是为了让「工具写出去的位」在本包内可被钉死
// 断言（quest 包的同名常量由 catalog_test.go 钉死为 0x13）。
const (
	ExtraEquipmentSlotsSupport    = 0x01
	ExtraEquipmentSlotsMagicStone = 0x02
	ExtraEquipmentSlotsEarring    = 0x10
	// ExtraEquipmentSlotsAll 是当前客户端「三个特殊装备槽全开」的掩码（0x13）。
	ExtraEquipmentSlotsAll = ExtraEquipmentSlotsSupport | ExtraEquipmentSlotsMagicStone | ExtraEquipmentSlotsEarring
)

// 面板/接口用的槽位分组标识。
const (
	ExtraEquipmentSlotGroupSupport    = "support"
	ExtraEquipmentSlotGroupMagicStone = "magicstone"
	ExtraEquipmentSlotGroupEarring    = "earring"
	ExtraEquipmentSlotGroupAll        = "all"
	ExtraEquipmentSlotGroupLeftRight  = "leftright"
)

// extraEquipmentSlotMeta 把一个特殊装备槽的「面板标识 / 显示名 / PVF 授权索引 /
// 持久位 / 核对锚点」绑在一起。
type extraEquipmentSlotMeta struct {
	Group string
	ID    string
	Label string
	// Index 是 PVF `[reward int data]` 里的 slot expansion 索引（0/1/2）。
	Index uint32
	Bit   byte
	// AnchorQuestID 是真档 Script.pvf 里带该索引的 `[slot expansion]` 任务 id，
	// 只用于启动/诊断为「PVF 对不上」时给出人能核对的证据。
	AnchorQuestID int64
}

var extraEquipmentSlotMetas = []extraEquipmentSlotMeta{
	{Group: ExtraEquipmentSlotGroupSupport, ID: "support", Label: "辅助装备槽（左槽）", Index: 0, Bit: ExtraEquipmentSlotsSupport, AnchorQuestID: 649},
	{Group: ExtraEquipmentSlotGroupMagicStone, ID: "magicStone", Label: "魔法石槽（右槽）", Index: 1, Bit: ExtraEquipmentSlotsMagicStone, AnchorQuestID: 650},
	{Group: ExtraEquipmentSlotGroupEarring, ID: "earring", Label: "耳环槽", Index: 2, Bit: ExtraEquipmentSlotsEarring, AnchorQuestID: 2636},
}

// 面板显示顺序：三个槽 + 合并项。
var extraEquipmentSlotOrder = []string{"support", "magicStone", "earring", "leftRight", "all"}

// extraEquipmentSlotQuestIDs 返回当前运行时 PVF 里「授权该槽位」的任务 id。
//
// 判据（不发明数据）：任务必须同时满足
//  1. `[reward type] `[slot expansion]“；
//  2. `[reward int data]` 恰好一个值，且等于该槽位的索引；
//  3. catalog 解析出的 SlotExpansionBit 等于该槽位的持久位。
//
// 找不到就返回错误 —— 宁可让操作者看到「PVF 与工具口径不一致」，也不写一个
// 恰好把别的槽打开的位。
func extraEquipmentSlotQuestIDs(catalog *dnfquest.Catalog, meta extraEquipmentSlotMeta) ([]int64, error) {
	if catalog == nil {
		return nil, fmt.Errorf("任务目录不可用")
	}
	ids := make([]int64, 0, 2)
	for _, questID := range catalog.QuestIDsBySlotExpansionIndex(meta.Index) {
		definition, found := catalog.Find(questID)
		if !found {
			continue
		}
		plan, err := catalog.PlanFinishReward(dnfquest.CharacterEligibility{Level: 1, Job: 0}, questID, 0, false)
		if err != nil {
			// 该 id 的 `[slot expansion]` 奖励形状不合法（例如缺 [reward int data]）：
			// 这是 PVF/工具不一致，必须报出来，不能静默跳过。
			return nil, fmt.Errorf(
				"PVF 任务 %d（%s）的 [slot expansion] 奖励不合法: %v",
				questID, definition.Path, err,
			)
		}
		if !plan.HasSlotExpansion || plan.SlotExpansionIndex != meta.Index || plan.SlotExpansionBit != meta.Bit {
			return nil, fmt.Errorf(
				"PVF 任务 %d（%s）声明的槽位与工具口径不一致: index=%d bit=%#x, want index=%d bit=%#x",
				questID, definition.Path, plan.SlotExpansionIndex, plan.SlotExpansionBit, meta.Index, meta.Bit,
			)
		}
		ids = append(ids, questID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf(
			"当前 PVF 里找不到授权「%s」的任务（[reward type] `[slot expansion]` + [reward int data] %d，真档锚点任务 %d）：PVF 与工具口径不一致，未做任何写入",
			meta.Label, meta.Index, meta.AnchorQuestID,
		)
	}
	return ids, nil
}

// extraEquipmentSlotMetaForRequest 解析请求里的槽位分组；空值按 A21 语义回落到
// 「左右槽」（保持旧前端 `POST .../equipment-slots/unlock` 不带 body 的行为不变）。
func extraEquipmentSlotMetaForRequest(value string) ([]extraEquipmentSlotMeta, error) {
	group := strings.ToLower(strings.TrimSpace(value))
	group = strings.NewReplacer("-", "", "_", "", " ", "").Replace(group)
	if group == "" || group == ExtraEquipmentSlotGroupLeftRight {
		return extraEquipmentSlotMetas[:2], nil
	}
	for _, meta := range extraEquipmentSlotMetas {
		normalized := strings.ToLower(meta.Group)
		normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
		if group == normalized {
			return []extraEquipmentSlotMeta{meta}, nil
		}
	}
	if group == ExtraEquipmentSlotGroupAll {
		return append([]extraEquipmentSlotMeta(nil), extraEquipmentSlotMetas...), nil
	}
	return nil, fmt.Errorf("未知的槽位分组: %s（可用: support / magicstone / earring / leftright / all）", value)
}

// extraEquipmentStatePayload 是角色详情里的槽位状态，字段名与面板一致：
// 旧字段 exEquipSlotStat / extraEquipmentSlotsUnlocked 保留（前端仍在读），
// 新增逐槽布尔与掩码，让面板不必再自己算位。
func extraEquipmentStatePayload(exEquip byte) map[string]any {
	mask := exEquip & ExtraEquipmentSlotsAll
	return map[string]any{
		"mask":       int(mask),
		"allMask":    int(ExtraEquipmentSlotsAll),
		"all":        mask == ExtraEquipmentSlotsAll,
		"support":    mask&ExtraEquipmentSlotsSupport != 0,
		"magicStone": mask&ExtraEquipmentSlotsMagicStone != 0,
		"earring":    mask&ExtraEquipmentSlotsEarring != 0,
		"leftRight":  mask&(ExtraEquipmentSlotsSupport|ExtraEquipmentSlotsMagicStone) == ExtraEquipmentSlotsSupport|ExtraEquipmentSlotsMagicStone,
		"missing":    missingExtraEquipmentSlotIDs(mask),
		"slotOrder":  append([]string(nil), extraEquipmentSlotOrder...),
	}
}

func missingExtraEquipmentSlotIDs(mask byte) []string {
	missing := make([]string, 0, len(extraEquipmentSlotMetas))
	for _, meta := range extraEquipmentSlotMetas {
		if mask&meta.Bit == 0 {
			missing = append(missing, meta.ID)
		}
	}
	return missing
}

// extraEquipmentSlotUnlockPlan 是「本次要写什么」的纯计算结果，不碰仓储。
// Granted 记录的是**本次真正新置位**的槽（而不是整组），否则「只开耳环时把左右槽
// 也报成刚开启」就会让面板显示与真实写入不符。
type extraEquipmentSlotUnlockPlan struct {
	Slots      []extraEquipmentSlotMeta
	AlreadyOn  []string
	Granted    []string
	QuestIDs   []int64
	GrantedBit byte
	ResultMask byte
	// NoWrite 为真表示位已经全开，本次连任务行都不需要动。
	NoWrite bool
}

func planExtraEquipmentSlotUnlock(currentMask byte, slots []extraEquipmentSlotMeta) extraEquipmentSlotUnlockPlan {
	plan := extraEquipmentSlotUnlockPlan{Slots: slots}
	for _, meta := range slots {
		if currentMask&meta.Bit != 0 {
			plan.AlreadyOn = append(plan.AlreadyOn, meta.ID)
			continue
		}
		plan.GrantedBit |= meta.Bit
		plan.Granted = append(plan.Granted, meta.ID)
	}
	plan.ResultMask = currentMask | plan.GrantedBit
	plan.NoWrite = plan.GrantedBit == 0
	return plan
}

// extraEquipmentSlotQuestChainIDs 把「授权该槽位的任务」扩展成「授权任务 + 它的
// `[pre required quest]` 前置链」，升序去重。
//
// 只走 modules/dnf/quest 的 `SlotExpansionQuestChainIDs`（PVF 判据写在那边的文件头），
// 本函数只做多槽合并与排序，不自己判断哪些 id 该收。
func extraEquipmentSlotQuestChainIDs(catalog *dnfquest.Catalog, questIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(questIDs)*2)
	chained := make([]int64, 0, len(questIDs)*2)
	for _, questID := range questIDs {
		for _, id := range catalog.SlotExpansionQuestChainIDs(questID) {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			chained = append(chained, id)
		}
	}
	sort.Slice(chained, func(left, right int) bool { return chained[left] < chained[right] })
	return chained
}

// handleUnlockEquipmentSlots 开启特殊装备槽。
//
// 请求：`POST /api/characters/{id}/equipment-slots/unlock`
// body（可空）：{"slots":"leftright"|"support"|"magicstone"|"earring"|"all"}
// 空 body / 空字符串 / "leftright" 都按「左右槽」处理，与 A21 前台旧调用完全兼容；
// 其余无法识别的分组一律拒绝（400 语义的业务失败），不做「猜一个」的回退。
//
// 写入（两处，都在同一个角色上）：
//  1. dnf_characters.ex_equip_slot_stat |= 目标位（先按位或，绝不覆盖已开的位）；
//  2. 任务行：**默认档**（`DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN` 未设/非 legacy）把
//     PVF 里授权该槽位的任务**连同它的 `[pre required quest]` 前置链**标成
//     completed（复用 handleSetGrowType 已经使用的同一段计划/写库逻辑
//     syncProfessionQuestStates，避免第二套开槽逻辑）。这样操作者点完按钮之后，
//     外传「穿戴耳环 (1/2)」（任务 2635）之类的残留任务不会继续挂在任务手册里。
//     `legacy` 档逐字节回到改动前：只有 `DNFBRIDGE_GM_EQUIP_SLOT_QUEST_SYNC=on`
//     才写任务，而且只写授权任务本身（不含前置链）。
//
// 幂等：位已经全开时不写位、不重复写任务行，只如实回报 alreadyOn；但**任务链
// 仍然同步一次**（见下），因为线上 8 个角色的 ex_equip_slot_stat 都带 0x10 位
// （2026-09-29 实测：1/3/4/6/10/14/15/16，其中只有 6 号是**真的走完** 2636 拿到
// 的，见 dnf_quest_states 与 settlement_receipt_v1），残留的外传任务只能靠再点
// 一次这个按钮收掉。
func (s *Server) handleUnlockEquipmentSlots(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[equipmentSlotUnlockRequest](w, r)
	if !ok {
		return
	}
	slots, err := extraEquipmentSlotMetaForRequest(body.Slots)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if character.Level < extraEquipmentSlotMinLevel {
		writeFailure(w, fmt.Sprintf("角色等级达到 %d 级后才能开启特殊装备槽", extraEquipmentSlotMinLevel))
		return
	}
	// 开关在请求时自读环境（与 online_guard.go / api_quest_mutations.go 同一取舍：
	// 不进改动最密集的 dnfbridge/options.go）。
	questChain := EquipSlotQuestChainFromEnvironment()
	questSync := questChain || s.cfg.EquipSlotQuestSync
	currentMask := byte(statInt(character, extraEquipmentSlotStatColumn))
	plan := planExtraEquipmentSlotUnlock(currentMask, slots)

	// legacy 档（questChain=false 且 questSync=false）连任务目录都不读：这条路径只写
	// 一个整型列，与四个参考实现同口径，因此不引入任何新依赖，也不会因为任务模块
	// 不可用而失败。
	questPlan := newProfessionQuestSyncPlan()
	if questSync {
		// 任务目录与任务仓储必须在写任何数据之前就绪：这一档承诺「开槽的位与授权
		// 任务的完成态一起落地」，只写位会让客户端的任务位图继续显示未完成。
		catalog, err := s.pvf.questCatalog(s)
		if err != nil {
			writeFailure(w, err.Error())
			return
		}
		if group.Quest == nil {
			writeFailure(w, "任务仓储不可用，无法同步开槽任务状态")
			return
		}
		// 授权任务 id 只从运行时 PVF 取（`[slot expansion]` + `[reward int data]`
		// 索引），取不到 / 对不上直接失败，绝不猜。
		for _, meta := range plan.Slots {
			questIDs, err := extraEquipmentSlotQuestIDs(catalog, meta)
			if err != nil {
				writeFailure(w, err.Error())
				return
			}
			plan.QuestIDs = append(plan.QuestIDs, questIDs...)
		}
		if questChain {
			plan.QuestIDs = extraEquipmentSlotQuestChainIDs(catalog, plan.QuestIDs)
		}
		// 任务列表取自整组 slots：已经开着的槽其任务行也要补齐（幂等，已 completed
		// 的行不会被重写）。位已经全开时也走这一段 —— 现场角色的位早就被旧工具打开，
		// 而残留的 2635「穿戴耳环 (1/2)」只有再点一次按钮才会被收掉。
		questPlan, err = s.syncProfessionQuestStates(ctx, group, character.CharacterID, plan.QuestIDs, nil)
		if err != nil {
			writeFailure(w, "开启特殊装备槽失败（任务状态）: "+err.Error())
			return
		}
	}

	if plan.NoWrite {
		writeOK(w, map[string]any{
			"characterId":         panelID,
			"exEquipSlotStat":     int(currentMask),
			"unlockedGroups":      []string{},
			"alreadyUnlocked":     plan.AlreadyOn,
			"completedQuestIds":   questPlan.Completed,
			"createdQuestIds":     questPlan.Created,
			"alreadyDoneQuestIds": questPlan.AlreadyDone,
			"questIds":            append([]int64{}, plan.QuestIDs...),
			"questSync":           questSync,
			"questChain":          questChain,
			"extraEquipmentState": extraEquipmentStatePayload(currentMask),
		})
		return
	}

	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Stats[extraEquipmentSlotStatColumn] = int64(currentMask | plan.GrantedBit)
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, "开启特殊装备槽失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId":         panelID,
		"exEquipSlotStat":     int(plan.ResultMask),
		"unlockedGroups":      plan.Granted,
		"alreadyUnlocked":     plan.AlreadyOn,
		"completedQuestIds":   questPlan.Completed,
		"createdQuestIds":     questPlan.Created,
		"alreadyDoneQuestIds": questPlan.AlreadyDone,
		// 默认档下 QuestIDs 是 nil；这里刻意用非 nil 空切片，让面板永远收到 []
		// 而不是 null（前端把 null 当成"没有这个字段"）。
		"questIds":            append([]int64{}, plan.QuestIDs...),
		"questSync":           questSync,
		"questChain":          questChain,
		"extraEquipmentState": extraEquipmentStatePayload(plan.ResultMask),
	})
}

// equipSlotQuestSyncEnv 是「开启特殊装备槽是否顺带写任务完成态」的开关名。
const equipSlotQuestSyncEnv = "DNFBRIDGE_GM_EQUIP_SLOT_QUEST_SYNC"

// EquipSlotQuestSyncFromEnv 解析 DNFBRIDGE_GM_EQUIP_SLOT_QUEST_SYNC。
// **只有显式 `on` 才启用**；未设置/空/其它任何值（含 `true`/`1`/`yes`）都取默认档
// false = 只写 ex_equip_slot_stat 的位。这样"默认 = 与四个参考实现逐字节同口径"
// 这条承诺不会被一个手滑的别名破坏（与 dnfbridge 的 readHellBlockedSlotsMode 同风格）。
//
// 注意：在 **legacy 档**（equipSlotQuestChainEnv=legacy）下它是唯一的闸门；
// 默认档（本开关不设）下任务写入由 equipSlotQuestChainEnv 决定，本开关不再起作用。
func EquipSlotQuestSyncFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

// equipSlotQuestChainEnv 是「开启特殊装备槽要不要把整条授权任务链标完成」的开关名。
const equipSlotQuestChainEnv = "DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN"

// equipSlotQuestChainLegacy 是唯一的逐字节回退字面量（改动前的语义）。
const equipSlotQuestChainLegacy = "legacy"

// EquipSlotQuestChainFromEnv 解析 DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN。
//
// **默认 = 修复后的行为**（与 GMMailGrantDialogFromEnv 同一组字面量约定）：
// 未设置/空/未知值（笔误）都取"开槽时把授权任务 + 它的 `[pre required quest]`
// 前置链一起标 completed"；只有显式
// off/0/false/no/disable/disabled/none/legacy 才回退。
//
// 未设置时为什么不是"与四个参考实现同口径的只写位"：现场（2026-09-29 05:0x）
// 明确要求「gm工具开启耳环槽后任务依然存在」要修掉，而只写位正是那条现象的原因。
// 回退靠显式字面量，见本文件顶部「写库范围」。
func EquipSlotQuestChainFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no", "disable", "disabled", "none", equipSlotQuestChainLegacy:
		return false
	default:
		return true
	}
}

// EquipSlotQuestChainFromEnvironment 读取进程环境里的同一个开关。
// 直接 `os.Getenv` 而不经 dnfbridge/options.go：与 online_guard.go、
// api_quest_mutations.go、api_items_grant_dialog.go 同一取舍（开关自读环境，
// 不进改动最密集的包）。
func EquipSlotQuestChainFromEnvironment() bool {
	return EquipSlotQuestChainFromEnv(os.Getenv(equipSlotQuestChainEnv))
}

// 日志/报错里用的列名与等级门槛（A21 前台同样写「角色达到 70 级后才能开启特殊
// 装备槽」，见 GmService.Characters.cs:405-409）。
const (
	extraEquipmentSlotStatColumn = "ex_equip_slot_stat"
	extraEquipmentSlotMinLevel   = 70
)

// equipmentSlotUnlockRequest 与 A21 前台的调用完全兼容：slots 缺省（旧前端不带
// body，或带空 body）时按「左右槽」处理，与旧行为一致。
type equipmentSlotUnlockRequest struct {
	Slots string `json:"slots"`
}
