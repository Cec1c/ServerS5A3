package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
)

// character_option_words.go —— 512 字节「每角色选项」里**已证字段**的只读解码器。
//
// 载体：op447 SAVE_CHARACTER_OPTION（C2S，u32 512 + 512B）与 class0/390（S2C，同一帧形状）。
// 落库与回放都在 game_option_settings.go，本文件只解码、不改写一个字节。
//
// 字段表来自固定磁盘客户端 DNF.exe（SHA256
// bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531，即 45 批原生取证用的
// 同一下载客户端）。三个 reader/writer 的基址都是 owner+0x1CC，也就是这份 512B blob 本身：
//
//	技能锁表（面板 SEAL_ON / SEAL_OFF）
//	  写 0x39A6BA0、读 0x39A6C00：index = (bank+2)*64 + entry，bank0/1 各 64 项，
//	  ⇒ word 128..191 = 树0、word 192..255 = 树1；值 = **客户端技能槽号**（不是 PVF
//	  模板技能 ID），空项 0xFFFF。导出 0x2255AA0（面板 0x293AFD0 的 SEAL_ON/SEAL_OFF 分支在
//	  0x293B24F 调它；导出里还会写 word0/word1，本文件只读不改），恢复 0x2255880
//	  （S2C390 读取端 0x2255DB0 在复制 512B 之后调用）。
//
//	CD 提示列表
//	  导出 0x22555F0：word 2..21 = 树0 的 20 项、word 22..41 = 树1 的 20 项，空项 0xFFFF；
//	  恢复 0x2254FC0（先对全部 512 槽清标记，再按两树列表恢复，含技能可用性保护）。
//	  面板入口 0x1E923E4..3F9（读 0x292AFA0 后调 0x292AFC0(flag, 1)）下发同一份 op447。
//
//	word43 = ExportScenarioDifficulty（写端 0x2255CA0）。
//	word126/127 = 全身时装可见 / 称号不透明度（厂商 AccountSettings.ProjectCharacterOptionBlob）。
//
// 边界（照 43 批报告的口径，不许外推）：其余 word 与其余字节**没有**被本文件赋予语义，
// 不重定义、不清除、不填默认值。解码只服务两件事 ——
//
//  1. 日志可观测性：客户端不会上报「点了哪个菜单」，一次锁定 / 一次 CD 提示开关在服务端只
//     表现为一份新的 512B。把「这次保存相对上一份存档改了哪些 word」和「锁表/CD 列表现在是
//     什么」写进日志，现场 A/B 才能把「包存在」升级成「字段对应」；
//  2. 回归断言：用真实捕获帧把 word 索引钉死，防止以后改保存/回放时把它们抹掉。
//
// 落库与回放**逐字节不变**：解码结果只进日志，不参与写入决策、不做保存门禁。
const (
	// currentCharacterOptionWordCount 是 512B / 2。
	currentCharacterOptionWordCount = 256
	// currentCharacterOptionInvalidWord 是「这个槽位没有值」的填充字。
	currentCharacterOptionInvalidWord = 0xFFFF

	// 锁表：word 128..191 = 树0，192..255 = 树1，每 bank 64 项。
	currentCharacterOptionLockFirstIndex  = 128
	currentCharacterOptionLockBankEntries = 64
	currentCharacterOptionLockBankCount   = 2

	// CD 提示列表：word 2..21 = 树0，22..41 = 树1，每树 20 项。
	currentCharacterOptionCDListFirstIndex  = 2
	currentCharacterOptionCDListTreeEntries = 20
	currentCharacterOptionCDListTreeCount   = 2

	// characterOptionLogSlotLimit 限制单条日志里每个列表 / 每个索引列表打印多少项。
	characterOptionLogSlotLimit = 8

	// characterOptionEncodingTag 是日志里可 grep 的编码口径标记。
	characterOptionEncodingTag = "pinned_v97_lock_words_128_255_cd_words_2_41"
)

// characterOptionSkillLists 是解码出来的四张「客户端技能槽号」列表。空项 0xFFFF 已被丢掉，
// 每棵树按 blob 中出现的顺序保留，值就是槽号本身（不做模板技能 ID 映射）。
type characterOptionSkillLists struct {
	LockTree0 []uint16
	LockTree1 []uint16
	CDTree0   []uint16
	CDTree1   []uint16
}

func (l characterOptionSkillLists) lockCount() int { return len(l.LockTree0) + len(l.LockTree1) }

func (l characterOptionSkillLists) cdCount() int { return len(l.CDTree0) + len(l.CDTree1) }

func (l characterOptionSkillLists) empty() bool { return l.lockCount() == 0 && l.cdCount() == 0 }

// summary 是单行、无空格、可直接 grep 的列表摘要（空列表记 -）。
func (l characterOptionSkillLists) summary() string {
	return "lock0=" + characterOptionSlotsField(l.LockTree0) +
		";lock1=" + characterOptionSlotsField(l.LockTree1) +
		";cd0=" + characterOptionSlotsField(l.CDTree0) +
		";cd1=" + characterOptionSlotsField(l.CDTree1)
}

// characterOptionPayload 从「完整帧」（u32 长度前缀 + blob）里取出 512B blob。
//
// 只接受声明长度正好 512 的帧：固定客户端的 S2C390 读取端（0x2255DCA）就是
// cmp [ebp-4], 0x200 之后才复制，非 512 的帧客户端根本不认，保存端也已按同一条件拒绝。
func characterOptionPayload(frame []byte) ([]byte, bool) {
	if len(frame) < 4 {
		return nil, false
	}
	declared := int(binary.LittleEndian.Uint32(frame[0:4]))
	if declared != currentCharacterOptionWordCount*2 || len(frame) < 4+declared {
		return nil, false
	}
	return frame[4 : 4+declared], true
}

// characterOptionWordsFromBlob 把 512B blob 拆成 256 个 u16（小端）。长度不是 512 返回 false。
func characterOptionWordsFromBlob(blob []byte) ([]uint16, bool) {
	if len(blob) != currentCharacterOptionWordCount*2 {
		return nil, false
	}
	words := make([]uint16, currentCharacterOptionWordCount)
	for index := range words {
		words[index] = binary.LittleEndian.Uint16(blob[index*2 : index*2+2])
	}
	return words, true
}

// characterOptionWordAt 读单个 word；长度不符或越界返回 false。
func characterOptionWordAt(blob []byte, index int) (uint16, bool) {
	if index < 0 || index >= currentCharacterOptionWordCount || len(blob) != currentCharacterOptionWordCount*2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(blob[index*2 : index*2+2]), true
}

// decodeCharacterOptionSkillLists 解码锁表（128..255）与 CD 提示列表（2..41）。
// 长度不是 512 时返回零值 + false，调用方必须当成「不知道」，不能用空列表冒充。
func decodeCharacterOptionSkillLists(blob []byte) (characterOptionSkillLists, bool) {
	words, ok := characterOptionWordsFromBlob(blob)
	if !ok {
		return characterOptionSkillLists{}, false
	}
	lists := characterOptionSkillLists{
		LockTree0: characterOptionSlotList(words, currentCharacterOptionLockFirstIndex, currentCharacterOptionLockBankEntries),
		LockTree1: characterOptionSlotList(words, currentCharacterOptionLockFirstIndex+currentCharacterOptionLockBankEntries, currentCharacterOptionLockBankEntries),
		CDTree0:   characterOptionSlotList(words, currentCharacterOptionCDListFirstIndex, currentCharacterOptionCDListTreeEntries),
		CDTree1:   characterOptionSlotList(words, currentCharacterOptionCDListFirstIndex+currentCharacterOptionCDListTreeEntries, currentCharacterOptionCDListTreeEntries),
	}
	return lists, true
}

// characterOptionSlotList 取 [first, first+entries) 里所有非 0xFFFF 的 word，保持原始顺序。
// 注意 0 是**合法槽号**（固定客户端的第一格），只有 0xFFFF 才算空。
func characterOptionSlotList(words []uint16, first, entries int) []uint16 {
	if first < 0 || entries <= 0 || first+entries > len(words) {
		return nil
	}
	slots := make([]uint16, 0, entries)
	for index := first; index < first+entries; index++ {
		if words[index] == currentCharacterOptionInvalidWord {
			continue
		}
		slots = append(slots, words[index])
	}
	return slots
}

// characterOptionChangedWordIndexes 返回 before → after 之间哪些 word 变了（升序）。
//
// 这是现场 A/B 的关键证据：客户端不会告诉你它点了哪个菜单，但保存时它会送上整份 512B；
// 「这次保存改了哪些 word」就是菜单动作在服务端的可读投影。两份长度都必须是 512。
func characterOptionChangedWordIndexes(before, after []byte) ([]int, bool) {
	if len(before) != currentCharacterOptionWordCount*2 || len(after) != currentCharacterOptionWordCount*2 {
		return nil, false
	}
	changed := make([]int, 0, 4)
	for index := 0; index < currentCharacterOptionWordCount; index++ {
		if binary.LittleEndian.Uint16(before[index*2:]) == binary.LittleEndian.Uint16(after[index*2:]) {
			continue
		}
		changed = append(changed, index)
	}
	return changed, true
}

// characterOptionClearedSkillLists 描述「上一份存档里有、这一份里没有了」的列表，
// 用于把「玩家的锁定 / CD 提示被默认模板刷掉」这件事在服务端日志里点名。
//
// 只比较**非空 → 空**这一个方向：反方向（空 → 非空）是正常菜单操作，不需要点名。
func characterOptionClearedSkillLists(before, after characterOptionSkillLists) []string {
	cleared := make([]string, 0, 4)
	if len(before.LockTree0) > 0 && len(after.LockTree0) == 0 {
		cleared = append(cleared, "lock_tree0")
	}
	if len(before.LockTree1) > 0 && len(after.LockTree1) == 0 {
		cleared = append(cleared, "lock_tree1")
	}
	if len(before.CDTree0) > 0 && len(after.CDTree0) == 0 {
		cleared = append(cleared, "cd_tree0")
	}
	if len(before.CDTree1) > 0 && len(after.CDTree1) == 0 {
		cleared = append(cleared, "cd_tree1")
	}
	return cleared
}

// characterOptionPayloadSHA256 是 payload（只要 512B，不带长度前缀）的 sha256，
// 与 45 批原生取证的 saved_blob_sha256 同口径，便于跨证据比对。
func characterOptionPayloadSHA256(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// characterOptionSlotsField 把一个槽号列表压成日志字段值；空列表记 -，
// 超过 characterOptionLogSlotLimit 个时以 +N 收尾（不打印无上限的值）。
func characterOptionSlotsField(slots []uint16) string {
	if len(slots) == 0 {
		return "-"
	}
	parts := make([]string, 0, characterOptionLogSlotLimit+1)
	for index, slot := range slots {
		if index >= characterOptionLogSlotLimit {
			parts = append(parts, "+"+strconv.Itoa(len(slots)-characterOptionLogSlotLimit))
			break
		}
		parts = append(parts, strconv.FormatUint(uint64(slot), 10))
	}
	return strings.Join(parts, ",")
}

// characterOptionWordIndexesField 把 word 索引列表压成日志字段值；空列表记 -。
func characterOptionWordIndexesField(indexes []int) string {
	if len(indexes) == 0 {
		return "-"
	}
	parts := make([]string, 0, characterOptionLogSlotLimit+1)
	for index, value := range indexes {
		if index >= characterOptionLogSlotLimit {
			parts = append(parts, "+"+strconv.Itoa(len(indexes)-characterOptionLogSlotLimit))
			break
		}
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

// characterOptionSkillListLogFields 是四张列表 + 两个计数的日志字段（供键值对展开）。
func characterOptionSkillListLogFields(lists characterOptionSkillLists) []any {
	return []any{
		"lock_tree0_slots", characterOptionSlotsField(lists.LockTree0),
		"lock_tree1_slots", characterOptionSlotsField(lists.LockTree1),
		"cd_tree0_slots", characterOptionSlotsField(lists.CDTree0),
		"cd_tree1_slots", characterOptionSlotsField(lists.CDTree1),
		"lock_slot_count", lists.lockCount(),
		"cd_slot_count", lists.cdCount(),
	}
}

// characterOptionSaveLogFields 生成 op447 保存事件的「字段对应」证据字段。
//
// previous 是这次保存之前该角色的 512B 存档（没有存档 / 长度不符时为 nil）。
// 解码失败时只报 option_words_decoded=false，绝不用「空列表」冒充「不知道」。
func characterOptionSaveLogFields(previous, current []byte) []any {
	lists, decoded := decodeCharacterOptionSkillLists(current)
	fields := []any{
		"previous_saved", len(previous) > 0,
		"payload_sha256", characterOptionPayloadSHA256(current),
		"option_words_decoded", decoded,
	}
	if changed, ok := characterOptionChangedWordIndexes(previous, current); ok {
		fields = append(fields,
			"changed_word_count", len(changed),
			"changed_word_indexes", characterOptionWordIndexesField(changed))
	} else {
		fields = append(fields, "changed_word_count", -1, "changed_word_indexes", "-")
	}
	fields = append(fields, characterOptionSkillListLogFields(lists)...)
	return append(fields, "option_word_encoding", characterOptionEncodingTag)
}

// characterOptionNoticeLogFields 生成 class0/390 回放事件的同一套证据字段。
// 帧不是 u32 512 + 512B 时照实报 option_words_decoded=false。
func characterOptionNoticeLogFields(frame []byte) []any {
	blob, ok := characterOptionPayload(frame)
	if !ok {
		return []any{"option_words_decoded", false, "option_word_encoding", characterOptionEncodingTag}
	}
	lists, decoded := decodeCharacterOptionSkillLists(blob)
	fields := []any{
		"option_words_decoded", decoded,
		"payload_sha256", characterOptionPayloadSHA256(blob),
	}
	fields = append(fields, characterOptionSkillListLogFields(lists)...)
	return append(fields, "option_word_encoding", characterOptionEncodingTag)
}

// loadSavedCharacterOptionBlob 只读读取该角色上一次落库的 512B 选项 blob（best-effort）。
//
// 读不到（没有存档 / 仓储错误 / 长度不是 512）时返回 nil：保存路径的落库语义不受影响，
// 它只用来在日志里点名「这次保存相对上一份存档改了哪些 word」。
func (s *Service) loadSavedCharacterOptionBlob(characterID string) []byte {
	if s == nil || strings.TrimSpace(characterID) == "" {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repos.Settings.Load(ctx, characterOptionScope(characterID))
	if err != nil || !found {
		return nil
	}
	blob := decodeSettingsHex(record.Values["character_option_hex"])
	if _, ok := characterOptionWordsFromBlob(blob); !ok {
		return nil
	}
	return blob
}

// logCharacterOptionSkillListsCleared 只在「上一份存档里的锁定 / CD 提示列表在新的一份里
// 消失」时记一条具名事件。
//
// 这是「客户端把玩家设置刷回默认模板」的现场探针（43 批报告后续取证清单的第二、三条）：
// 它**只记日志、不拦保存** —— 保存端「逐字节落库」的契约不能动，否则会误杀玩家刚改完的合法保存。
// 缺这条日志时，「到底是没保存、还是被刷掉、还是没回放」只能靠猜。
func (s *Service) logCharacterOptionSkillListsCleared(session *gameSession, characterID string, previous, current []byte) {
	if s == nil || len(previous) == 0 {
		return
	}
	before, beforeOK := decodeCharacterOptionSkillLists(previous)
	after, afterOK := decodeCharacterOptionSkillLists(current)
	if !beforeOK || !afterOK {
		return
	}
	cleared := characterOptionClearedSkillLists(before, after)
	if len(cleared) == 0 {
		return
	}
	s.logGameEvent(session, "game-character-option-skill-lists-cleared",
		"char_id", characterID,
		"cleared", strings.Join(cleared, ","),
		"previous_lock_slot_count", before.lockCount(),
		"current_lock_slot_count", after.lockCount(),
		"previous_cd_slot_count", before.cdCount(),
		"current_cd_slot_count", after.cdCount(),
		"previous_payload_sha256", characterOptionPayloadSHA256(previous),
		"current_payload_sha256", characterOptionPayloadSHA256(current),
		"option_word_encoding", characterOptionEncodingTag,
		"note", "diagnostic_only_no_write_guard")
}
