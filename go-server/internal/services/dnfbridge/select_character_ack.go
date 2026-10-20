package dnfbridge

import (
	"encoding/binary"
	"os"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentSelectAckResultOffset       = 0
	currentSelectAckCreatedTimeOffset  = 5
	currentSelectAckCharacterIDOffset  = 9
	currentSelectAckFatigueUsedOffset  = 11
	currentSelectAckFatigueLimitOffset = 13
	currentSelectAckFatigueAuxOffset   = 15
	currentSelectAckPremiumCountOffset = 17

	currentSelectAckFixedQuestCount                    = 30
	currentSelectAckQuestRowSize                       = 6
	currentSelectAckStateU32Count                      = 4
	currentSelectAckStateByteSize                      = currentSelectAckStateU32Count * 4
	currentSelectAckMinimumBodyLen                     = 233
	currentSelectAckAccountLoopDungeonTutorialIndex    = 13
	currentSelectAckAccountLoopDungeonTutorialMinLevel = 20
	currentSelectAckPage1RouteIndex                    = 31

	// The generated enum still carries the old SET_USER_AREA name. The current
	// class-0/op36 handler is the five-u16 fatigue-state reader at sub_1D7ABE0.
	currentFatigueMsgID = uint16(dnfenum.CmdPacketSetUserArea)
)

type currentSelectAckQuestRow struct {
	questID      uint16
	triggerValue uint32
}

type currentFatigueState struct {
	used        uint16
	limit       uint16
	actorAux    uint16
	displayUsed uint16
	actorExtra  uint16
}

func buildCurrentSelectCharacterAckBody(character dnfrepo.CharacterRecord, hasCharacter bool, quests dnfrepo.QuestRecord, hasQuests bool, characterID uint16, initialTownID uint8, accountCera uint32, premiumEntries []byte) []byte {
	fatigue := currentCharacterFatigueState(character, hasCharacter)
	rows := currentSelectAckQuestRows(quests, hasQuests)
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint32(0) // Account registration time is not consumed by the current handler.
	writer.writeUint32(currentSelectAckCreatedTime(character, hasCharacter))
	writer.writeUint16(characterID)
	writer.writeUint16(fatigue.used)
	writer.writeUint16(fatigue.limit)
	writer.writeUint16(fatigue.actorAux)

	// Premium entries are type:u8 + remaining seconds:i64LE from the
	// account-owned premium contracts in account metadata. Devil slots are
	// folded to type 58 with their longest remaining duration. Cera is the
	// account-shared pool from account metadata, identical for every character
	// on the account.
	writer.writeBytes(premiumEntries)
	writer.writeUint32(accountCera)

	fixedCount := len(rows)
	if fixedCount > currentSelectAckFixedQuestCount {
		fixedCount = currentSelectAckFixedQuestCount
	}
	for idx := 0; idx < currentSelectAckFixedQuestCount; idx++ {
		if idx < fixedCount {
			writer.writeUint16(rows[idx].questID)
			writer.writeUint32(rows[idx].triggerValue)
			continue
		}
		writer.writeUint16(0xffff)
		writer.writeUint32(0)
	}

	overflowCount := len(rows) - fixedCount
	writer.writeUint32(uint32(overflowCount))
	for idx := fixedCount; idx < len(rows); idx++ {
		writer.writeUint16(rows[idx].questID)
		writer.writeUint32(rows[idx].triggerValue)
	}

	// Current sub_1A0C3E0 always consumes four u32 values after the overflow
	// quest rows and before the initial town ID. Fresh characters have no
	// persisted owner for these states, so their real initialized values are 0.
	for idx := 0; idx < currentSelectAckStateU32Count; idx++ {
		writer.writeUint32(0)
	}

	// Current EXE 01D0224E reads town:u8, then sub_345A0B0 resolves its town
	// descriptor through [051B26D8]. The valid branch passes this same town
	// to sub_3411790 (world-map context +1E4). This is NOT the mode2 roster
	// key: slot 0 resolves NULL and skips initial scene construction on a
	// cold client, leaving the later mode0 actor collection unavailable.
	// Ported back from the lost 2026-09-15 fix (archive commit 6be4c236);
	// see docs/evidence/cold-login-town-descriptor-20260914.md.
	writer.writeByte(initialTownID)

	// Current sub_33C28A0 consumes this first byte but never uses it. The
	// following list is the real tutorial-flag set: every index is written to
	// tutorial_flags[index]=1 before the client derives its current tutorial
	// route from the maximum index. Index31 remains the proved page-1 route:
	// it reaches sub_1CF2A70(1) and sub_226D5F0 without synchronously entering
	// the >38 sub_33C04C0 branch, and the later sub_33C6430 state dispatcher
	// maps state 31 to sub_33C04C0, while state 0 maps to the observed
	// null-actor crash owner sub_33C5AB0.
	//
	// 角色此前只收到 [31]，于是更早的账号级教程 tutorialtextaccount ID13
	// （循环副本教程，Lv20）一直是未置位状态，客户端会在副本 Loading 期间重新
	// 弹出赛丽亚教程。这里只补这一条已被证实的账号教程，不伪造 0..31 的其余位。
	// 索引 13 < 31，列表最大值不变 ⇒ 教程路由仍是 31 那一档，逐字节只多一个元素。
	// Project only the durable server decision here; pending and unresolved
	// characters keep the empty list and follow the practice-dungeon route.
	writer.writeByte(0)
	tutorialIndexes := currentSelectAckTutorialIndexesForCharacter(character, hasCharacter)
	writer.writeByte(byte(len(tutorialIndexes)))
	writer.writeBytes(tutorialIndexes)
	writer.writeUint16(statU16(character, "ack_fatigue_battery", 0))
	writer.writeUint16(fatigue.displayUsed)
	writer.writeByte(statU8(character, "trade_punish_flag", 0))
	writer.writeUint16(fatigue.actorExtra)
	writer.writeByte(0) // No typed source for the current secondary u16+u32 list yet.
	return writer.bytes()
}

// 选角 ACK town:u8 的口径开关。**默认 = 修复档**（写初始城镇 ID）。
//
// 为什么是"默认修复 + 只留回滚"：这一个字节不是观感调优，而是客户端建立初始场景的
// 前置条件 —— 当前 EXE 01D0224E 读它，`035F4B90` 用 [051B26D8] 查 town descriptor，
// 查不到就跳 01D024A3 跳过初始化（只继续教程与后续 ACK）。角色列表 slot 0（本机唯一角色
// 就是 slot 0）解析不到 descriptor ⇒ 冷启动客户端不构造初始场景 ⇒ 之后的 mode0 actor
// 集合拿不到 owner ⇒ `025AC973 mov eax,[ecx+10]` 读 NULL+10（见
// docs/evidence/cold-login-town-descriptor-20260914.md:32,36-46）。所以这个字节必须是
// 真实登录房间城镇 38，而不是 slot。
//
//	DNFBRIDGE_SELECT_ACK_TOWN
//	  ""（默认）/ 任何其它值：写 newCharacterInitialTownID（38），与
//	    currentCharacterListLoginTransition 的共享登录房间 38/1 同源。
//	  "roster-slot"/"slot"/"legacy"/"old"：**回滚档**，逐字节恢复修复前的行为
//	    （把 currentSelectAckTownID 收到的角色列表 slot 写进这个字节），
//	    仅用于实机 A/B 对照，任何情况下都不该留在线上。
//
// 只影响这一个字节：包大小、四个 u32、相邻教程字段与后续路由都不经过这里。
const currentSelectAckTownEnv = "DNFBRIDGE_SELECT_ACK_TOWN"

const currentSelectAckTownRollbackValue = "roster-slot"

// 判读用来源标签：ACK 日志里的 initial_town_source 直接说明这个字节来自哪一档。
const (
	currentSelectAckTownSourceInitialTown    = "initial_town_id"
	currentSelectAckTownSourceRosterRollback = "roster_slot_rollback"
)

func currentSelectAckTownRollbackToRosterSlot() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentSelectAckTownEnv))) {
	case currentSelectAckTownRollbackValue, "slot", "legacy", "old":
		return true
	default:
		return false
	}
}

// currentSelectAckTownID 决定 buildCurrentSelectCharacterAckBody 的 town:u8 取什么。
//
// 返回 (townID, source, ok)：ok=false 只在回滚档且 slot 超出 u8 时出现，调用方按
// 修复前的口径阻断这次选角初始化。
func currentSelectAckTownID(slot int) (uint8, string, bool) {
	if currentSelectAckTownRollbackToRosterSlot() {
		if slot < 0 || slot > int(^uint8(0)) {
			return 0, currentSelectAckTownSourceRosterRollback, false
		}
		return uint8(slot), currentSelectAckTownSourceRosterRollback, true
	}
	return newCharacterInitialTownID, currentSelectAckTownSourceInitialTown, true
}

// 选角 ACK 教程索引列表的口径开关。**默认 = 修复档**（Lv20+ 发 [13,31]）。
//
// 为什么默认修复：这一段是客户端 tutorial_flags 的**置位集合**，客户端再从中取
// 最大值决定当前教程路由。索引 31 是已证实的 page-1 路由，索引 13 是账号级
// "循环副本教程"（tutorialtextaccount ID13，Lv20 开放）。旧行为只发 [31]，
// 等于 13 这一位永远是 0 —— 客户端因此会在副本 Loading 期间重新弹出赛丽亚教程。
// 补回 13 不会改变路由：13 < 31，列表最大值仍是 31。
//
//	DNFBRIDGE_SELECT_ACK_TUTORIAL_INDEXES
//	  ""（默认）/ 任何其它值：Lv>=20 的既有角色发 [13,31]，Lv<20 发 [31]，
//	    待教程角色发空列表（与修复前的空列表逐字节相同）。
//	  "legacy"/"off"/"0"/"false"/"no"：**回滚档**，逐字节恢复修复前行为
//	    （既有角色一律只发 [31]），仅用于实机 A/B 对照。
//
// 只影响紧跟被忽略标志之后那一个 u8 计数与它的索引列表；包大小会因多一个索引而
// +1（这正是修复本身），其余字段、后续路由与事务时机都不经过这里。
const currentSelectAckTutorialIndexesEnv = "DNFBRIDGE_SELECT_ACK_TUTORIAL_INDEXES"

// currentSelectAckTutorialIndexesRollbackToPage1Only 复用同包统一的档位判定
// （town_copresence_actor_resend.go 的 currentTownPresencePublishDisabled），
// 回退字面量与全仓其它开关保持同一组。
func currentSelectAckTutorialIndexesRollbackToPage1Only() bool {
	return currentTownPresencePublishDisabled(currentSelectAckTutorialIndexesEnv)
}

// currentSelectAckTutorialIndexesMode 是判读事件/测试用的档位串。
func currentSelectAckTutorialIndexesMode() string {
	if currentSelectAckTutorialIndexesRollbackToPage1Only() {
		return "legacy_page1_only_31"
	}
	return "account_loop_dungeon_13_plus_page1_31"
}

// currentSelectAckTutorialIndexesForCharacter 是教程索引列表的唯一构建点。
//
// 移植自 2026-09-24 归档（同一血统的 9/24 快照）的
// go-server/internal/services/dnfbridge/select_character_ack.go 同名函数，该函数在
// 在役树里从未存在过 —— 在役实现把列表写死成单元素 [31]。归档 PROTOCOL_INDEX.md:55
// 条目 "class1/op4 select ACK: initial town, not roster slot (2026-09-15)" 明确：
// "for an established character below Lv20 keep [31]; for Lv20+ keep [13,31] ...
// New/practice-tutorial characters still keep the empty list."
func currentSelectAckTutorialIndexesForCharacter(character dnfrepo.CharacterRecord, hasCharacter bool) []byte {
	if !hasCharacter || selectedCharacterStartsInTutorial(character, true) {
		return nil
	}
	if currentSelectAckTutorialIndexesRollbackToPage1Only() {
		return []byte{currentSelectAckPage1RouteIndex}
	}
	indexes := make([]byte, 0, 2)
	if character.Level >= currentSelectAckAccountLoopDungeonTutorialMinLevel {
		indexes = append(indexes, currentSelectAckAccountLoopDungeonTutorialIndex)
	}
	indexes = append(indexes, currentSelectAckPage1RouteIndex)
	return indexes
}

func currentSelectAckCreatedTime(character dnfrepo.CharacterRecord, hasCharacter bool) uint32 {
	if !hasCharacter || character.CreatedAt.IsZero() {
		return 0
	}
	seconds := character.CreatedAt.Unix()
	if seconds <= 0 {
		return 0
	}
	if seconds > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(seconds)
}

func currentCharacterFatigueState(character dnfrepo.CharacterRecord, hasCharacter bool) currentFatigueState {
	if !hasCharacter {
		return currentFatigueState{}
	}
	limit := statU16(character, "fatigue_limit", 0)
	remaining, hasRemaining := statInt64OK(character, "fatigue")
	if hasRemaining {
		if remaining < 0 {
			remaining = 0
		}
		if remaining > int64(^uint16(0)) {
			remaining = int64(^uint16(0))
		}
		if limit == 0 {
			limit = uint16(remaining)
		}
	}

	used := statU16(character, "fatigue_used", 0)
	if hasRemaining && int64(limit) >= remaining {
		used = limit - uint16(remaining)
	}
	displayUsed := statU16(character, "fatigue_display_update", used)
	return currentFatigueState{
		used:        used,
		limit:       limit,
		actorAux:    statU16(character, "fatigue_update", 0),
		displayUsed: displayUsed,
		actorExtra:  statU16(character, "ack_fatigue_grownup_buff", 0),
	}
}

func buildCurrentFatigueBody(character dnfrepo.CharacterRecord, hasCharacter bool) []byte {
	state := currentCharacterFatigueState(character, hasCharacter)
	var writer packetWriter
	writer.writeUint16(state.used)
	writer.writeUint16(state.limit)
	writer.writeUint16(state.actorAux)
	writer.writeUint16(state.displayUsed)
	writer.writeUint16(state.actorExtra)
	return writer.bytes()
}

func currentSelectAckQuestRows(record dnfrepo.QuestRecord, hasRecord bool) []currentSelectAckQuestRow {
	if !hasRecord || (len(record.States) == 0 && len(record.Progress) == 0) {
		return nil
	}
	rows := make([]currentSelectAckQuestRow, 0, len(record.States)+len(record.Progress))
	seen := make(map[int64]struct{}, len(record.States)+len(record.Progress))
	collect := func(states map[int64]dnfrepo.QuestState) {
		for questID, state := range states {
			if _, ok := seen[questID]; ok {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(state.Status), "active") || questID <= 0 || questID >= 0xffff {
				continue
			}
			triggerValue := uint32(0)
			if state.ProgressValue > 0 {
				triggerValue = uint32(state.ProgressValue)
				if state.ProgressValue > int64(^uint32(0)) {
					triggerValue = ^uint32(0)
				}
			}
			rows = append(rows, currentSelectAckQuestRow{questID: uint16(questID), triggerValue: triggerValue})
			seen[questID] = struct{}{}
		}
	}
	// States is the canonical active-quest owner for new writes.  Progress is
	// still populated by older dungeon/quest code paths and by migrated rows,
	// so selection ACK / op574 must merge both or the client sees an empty task
	// tracker immediately after relogging or completing a chain step.
	collect(record.States)
	collect(record.Progress)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].questID < rows[j].questID
	})
	return rows
}

func currentSelectAckQuestLayout(body []byte) (int, bool) {
	if len(body) <= currentSelectAckPremiumCountOffset {
		return 0, false
	}
	premiumCount := int(body[currentSelectAckPremiumCountOffset])
	offset := currentSelectAckPremiumCountOffset + 1 + premiumCount*9 + 4
	minimum := offset + currentSelectAckFixedQuestCount*currentSelectAckQuestRowSize + 4
	return offset, minimum <= len(body)
}

func currentSelectAckTailOffsets(body []byte) (int, int, bool) {
	questOffset, ok := currentSelectAckQuestLayout(body)
	if !ok {
		return 0, 0, false
	}
	overflowOffset := questOffset + currentSelectAckFixedQuestCount*currentSelectAckQuestRowSize
	if overflowOffset+4 > len(body) {
		return 0, 0, false
	}
	overflowCount := uint64(binary.LittleEndian.Uint32(body[overflowOffset : overflowOffset+4]))
	overflowRowsOffset := overflowOffset + 4
	availableRows := uint64((len(body) - overflowRowsOffset) / currentSelectAckQuestRowSize)
	if overflowCount > availableRows {
		return 0, 0, false
	}
	stateOffset := overflowRowsOffset + int(overflowCount)*currentSelectAckQuestRowSize
	if stateOffset+currentSelectAckStateByteSize > len(body) {
		return 0, 0, false
	}
	tailOffset := stateOffset + currentSelectAckStateByteSize
	if tailOffset+3 > len(body) {
		return 0, 0, false
	}
	tutorialCount := int(body[tailOffset+2])
	postTutorialOffset := tailOffset + 3 + tutorialCount
	if postTutorialOffset+8 > len(body) {
		return 0, 0, false
	}
	secondaryCount := int(body[postTutorialOffset+7])
	if secondaryCount > (len(body)-(postTutorialOffset+8))/currentSelectAckQuestRowSize {
		return 0, 0, false
	}
	return tailOffset, postTutorialOffset, true
}

func currentSelectAckIntermediateState(body []byte) ([currentSelectAckStateU32Count]uint32, int, bool) {
	var values [currentSelectAckStateU32Count]uint32
	questOffset, ok := currentSelectAckQuestLayout(body)
	if !ok {
		return values, 0, false
	}
	overflowOffset := questOffset + currentSelectAckFixedQuestCount*currentSelectAckQuestRowSize
	if overflowOffset+4 > len(body) {
		return values, 0, false
	}
	overflowCount := uint64(binary.LittleEndian.Uint32(body[overflowOffset : overflowOffset+4]))
	overflowRowsOffset := overflowOffset + 4
	availableRows := uint64((len(body) - overflowRowsOffset) / currentSelectAckQuestRowSize)
	if overflowCount > availableRows {
		return values, 0, false
	}
	stateOffset := overflowRowsOffset + int(overflowCount)*currentSelectAckQuestRowSize
	if stateOffset+currentSelectAckStateByteSize > len(body) {
		return values, 0, false
	}
	for idx := range values {
		offset := stateOffset + idx*4
		values[idx] = binary.LittleEndian.Uint32(body[offset : offset+4])
	}
	return values, stateOffset, true
}

func currentSelectAckQuestRowCounts(body []byte) (int, int, bool) {
	questOffset, ok := currentSelectAckQuestLayout(body)
	if !ok {
		return 0, 0, false
	}
	fixedCount := 0
	for idx := 0; idx < currentSelectAckFixedQuestCount; idx++ {
		offset := questOffset + idx*currentSelectAckQuestRowSize
		if binary.LittleEndian.Uint16(body[offset:offset+2]) != 0xffff {
			fixedCount++
		}
	}
	overflowOffset := questOffset + currentSelectAckFixedQuestCount*currentSelectAckQuestRowSize
	overflowCount := int(binary.LittleEndian.Uint32(body[overflowOffset : overflowOffset+4]))
	if _, _, ok := currentSelectAckTailOffsets(body); !ok {
		return fixedCount, 0, false
	}
	return fixedCount, overflowCount, true
}

func currentSelectAckCharacterID(body []byte) (uint16, bool) {
	if len(body) < currentSelectAckCharacterIDOffset+2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(body[currentSelectAckCharacterIDOffset : currentSelectAckCharacterIDOffset+2]), true
}

func currentSelectAckInitialTownIDFromBody(body []byte) (uint8, bool) {
	tailOffset, _, ok := currentSelectAckTailOffsets(body)
	if !ok {
		return 0, false
	}
	return body[tailOffset], true
}

func currentSelectAckTutorialState(body []byte) (uint8, uint8, bool) {
	tailOffset, _, ok := currentSelectAckTailOffsets(body)
	if !ok {
		return 0, 0, false
	}
	return body[tailOffset+1], body[tailOffset+2], true
}

func currentSelectAckTutorialIndexes(body []byte) ([]byte, bool) {
	tailOffset, _, ok := currentSelectAckTailOffsets(body)
	if !ok {
		return nil, false
	}
	count := int(body[tailOffset+2])
	start := tailOffset + 3
	if count > len(body)-start {
		return nil, false
	}
	return append([]byte(nil), body[start:start+count]...), true
}

// selectedCharacterStartsInTutorial decides only the first scene after
// character selection. Newly created characters persist tutorial_completed=0;
// the completed tutorial path persists 1 before returning to town. Older
// records that predate the marker fall back to level 1 so an established
// character is not silently forced back into the tutorial.
func selectedCharacterStartsInTutorial(character dnfrepo.CharacterRecord, hasCharacter bool) bool {
	if !hasCharacter {
		return false
	}
	if character.Stats != nil {
		if completed, ok := character.Stats[currentDungeonTutorialCompletedKey]; ok {
			return completed != currentDungeonTutorialCompleteFlag
		}
	}
	return character.Level <= 1
}
