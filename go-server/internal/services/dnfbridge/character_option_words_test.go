package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCharacterOptionFailedSaveDoesNotReportListsCleared(t *testing.T) {
	s, session, repos := scope44Fixture(t)
	scope44Select(t, s, session, 29)
	locked := pinnedNativeLockedFrame(t)
	scope44Save(t, s, session, locked)
	logPath := filepath.Join(t.TempDir(), "failed-save.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	failing := repos
	stub := &countingSettings567r4{base: repos.Settings, failSave: true}
	failing.Settings = stub
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
	cleared := append([]byte(nil), pinnedNativeLockedBlob(t)...)
	characterOptionPutWord(cleared, 128, 0xffff)
	scope44Save(t, s, session, characterOptionFrame(cleared))
	if stub.saveCalls == 0 {
		t.Fatal("failed save was not exercised")
	}
	if got := scope44Stored(t, repos, "29"); got != hex.EncodeToString(locked) {
		t.Fatal("failed save changed persisted options")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "game-character-option-skill-lists-cleared") {
		t.Fatal("failed save was reported as a committed clearing of skill lists")
	}
}

// 真实捕获：45 批原生取证（H:/YKCN90工作区/调查资料/续修-D33-20261011/native-skill-observation45）
// 里，操作者对角色 q7a 在技能面板点「锁定」后，客户端实际发出的 op447 被逐字节落库的那份
// 512B payload（option-state45b.local.json 的 character_option_hex）。
//
// 结构（EXE 已证 word 语义）：word0/word1 = 0（锁表导出会写它们），word128 = 5
// （树0 锁表第一项，值就是**客户端技能槽号** 5），其余全部 0xFFFF（word43 难度为 0）。
const pinnedNativeLockedCharacterOptionBlobHex = "00000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff0000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff0500ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

const pinnedNativeLockedCharacterOptionBlobSHA256 = "4fe7d38648f9d5a19c372a73c684c7d66bd473dc9db6245da8bc0e9a3d7e297b"

func pinnedNativeLockedBlob(t *testing.T) []byte {
	t.Helper()
	blob, err := hex.DecodeString(strings.TrimSpace(pinnedNativeLockedCharacterOptionBlobHex))
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != currentCharacterOptionWordCount*2 {
		t.Fatalf("pinned native blob = %d B, want 512", len(blob))
	}
	if got := characterOptionPayloadSHA256(blob); got != pinnedNativeLockedCharacterOptionBlobSHA256 {
		t.Fatalf("pinned native blob sha256 = %s, want %s", got, pinnedNativeLockedCharacterOptionBlobSHA256)
	}
	return blob
}

// characterOptionFrame 把 512B payload 包成客户端原帧形状（u32 512 + blob）。
func characterOptionFrame(blob []byte) []byte {
	frame := make([]byte, 4+len(blob))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(blob)))
	copy(frame[4:], blob)
	return frame
}

func pinnedNativeLockedFrame(t *testing.T) []byte {
	t.Helper()
	return characterOptionFrame(pinnedNativeLockedBlob(t))
}

// characterOptionTemplateBlob 是「没写过任何选项」的模板：全部 0xFFFF。
func characterOptionTemplateBlob() []byte {
	return bytes.Repeat([]byte{0xFF}, currentCharacterOptionWordCount*2)
}

func characterOptionPutWord(blob []byte, index int, value uint16) {
	binary.LittleEndian.PutUint16(blob[index*2:], value)
}

func characterOptionEqualWords(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func characterOptionEqualInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// requireLogLine 在 packet log 文本里找一条同时命中 kind 与 char 字段的事件行，
// 并要求这行包含全部 want 字段；找不到就打印实际行，便于定位。
func requireLogLine(t *testing.T, content, kind, charField string, want ...string) {
	t.Helper()
	candidates := make([]string, 0, 4)
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "kind="+kind) {
			continue
		}
		if charField != "" && !strings.Contains(line, charField) {
			continue
		}
		missing := 0
		for _, field := range want {
			if !strings.Contains(line, field) {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		candidates = append(candidates, line)
	}
	t.Fatalf("%s %s has no line with %v; candidates: %s", kind, charField, want, strings.Join(candidates, " | "))
}

// 真实锁定帧解码：锁表 tree0 = [5]，其余三张列表为空；word43/126/127 不被本解码器赋予技能语义。
func TestCharacterOptionWordsDecodePinnedNativeLockFrame(t *testing.T) {
	blob := pinnedNativeLockedBlob(t)
	lists, ok := decodeCharacterOptionSkillLists(blob)
	if !ok {
		t.Fatal("pinned native blob failed to decode")
	}
	if !characterOptionEqualWords(lists.LockTree0, []uint16{5}) {
		t.Fatalf("lock tree0 = %v, want [5] (client skill slot written by the lock export)", lists.LockTree0)
	}
	if len(lists.LockTree1) != 0 || len(lists.CDTree0) != 0 || len(lists.CDTree1) != 0 {
		t.Fatalf("unexpected lists: lock1=%v cd0=%v cd1=%v", lists.LockTree1, lists.CDTree0, lists.CDTree1)
	}
	if lists.lockCount() != 1 || lists.cdCount() != 0 {
		t.Fatalf("counts lock=%d cd=%d", lists.lockCount(), lists.cdCount())
	}
	// word43 是 ExportScenarioDifficulty，word126/127 是时装可见/称号不透明度：本解码器不动它们。
	for _, index := range []int{43, 126, 127} {
		if _, ok := characterOptionWordAt(blob, index); !ok {
			t.Fatalf("word %d unreadable", index)
		}
	}
	if word, _ := characterOptionWordAt(blob, 128); word != 5 {
		t.Fatalf("word128 = %d, want 5", word)
	}
	payload, ok := characterOptionPayload(characterOptionFrame(blob))
	if !ok || !bytes.Equal(payload, blob) {
		t.Fatal("u32 512 + 512B frame did not yield the pinned payload")
	}
}

// 四张列表的索引必须按树分开，且槽号 0 是合法值（只有 0xFFFF 才算空）。
func TestCharacterOptionWordsReadPerTreeListsAndKeepSlotZero(t *testing.T) {
	blob := characterOptionTemplateBlob()
	characterOptionPutWord(blob, 2, 4)
	characterOptionPutWord(blob, 21, 5)
	characterOptionPutWord(blob, 22, 6)
	characterOptionPutWord(blob, 41, 7)
	characterOptionPutWord(blob, 128, 0)
	characterOptionPutWord(blob, 191, 64)
	characterOptionPutWord(blob, 192, 65)
	characterOptionPutWord(blob, 255, 128)
	lists, ok := decodeCharacterOptionSkillLists(blob)
	if !ok {
		t.Fatal("decode failed")
	}
	if !characterOptionEqualWords(lists.CDTree0, []uint16{4, 5}) {
		t.Fatalf("cd tree0 = %v, want [4 5] (word2..21)", lists.CDTree0)
	}
	if !characterOptionEqualWords(lists.CDTree1, []uint16{6, 7}) {
		t.Fatalf("cd tree1 = %v, want [6 7] (word22..41)", lists.CDTree1)
	}
	if !characterOptionEqualWords(lists.LockTree0, []uint16{0, 64}) {
		t.Fatalf("lock tree0 = %v, want [0 64] (word128..191, slot 0 is legal)", lists.LockTree0)
	}
	if !characterOptionEqualWords(lists.LockTree1, []uint16{65, 128}) {
		t.Fatalf("lock tree1 = %v, want [65 128] (word192..255)", lists.LockTree1)
	}
	if _, ok := characterOptionWordAt(blob, currentCharacterOptionWordCount); ok {
		t.Fatal("word index 256 was accepted")
	}
	if _, ok := characterOptionWordAt(blob[:511], 0); ok {
		t.Fatal("511-byte payload was accepted as a word source")
	}
}

// 非 512 的形状一律「不知道」：客户端 S2C390 读取端只接受 512，保存端也已按同一条件拒绝。
func TestCharacterOptionWordsRejectNonNativeLengths(t *testing.T) {
	for _, size := range []int{0, 1, 510, 511, 513, 514} {
		if _, ok := characterOptionWordsFromBlob(bytes.Repeat([]byte{0}, size)); ok {
			t.Fatalf("payload size %d was accepted", size)
		}
		if _, ok := decodeCharacterOptionSkillLists(bytes.Repeat([]byte{0}, size)); ok {
			t.Fatalf("payload size %d decoded as skill lists", size)
		}
	}
	blob := pinnedNativeLockedBlob(t)
	if _, ok := characterOptionChangedWordIndexes(blob[:511], blob); ok {
		t.Fatal("511-byte previous payload was accepted for diffing")
	}
	for _, declared := range []uint32{0, 511, 513, 1024} {
		frame := characterOptionFrame(blob)
		binary.LittleEndian.PutUint32(frame[0:4], declared)
		if _, ok := characterOptionPayload(frame); ok {
			t.Fatalf("frame declaring %d bytes was accepted", declared)
		}
	}
	frame := characterOptionFrame(blob)
	if _, ok := characterOptionPayload(frame[:7]); ok {
		t.Fatal("truncated frame was accepted")
	}
}

// 「这次保存改了哪些 word」是现场 A/B 的关键：客户端不上报菜单，只送整份 512B。
func TestCharacterOptionChangedWordIndexesTracksMenuEdits(t *testing.T) {
	template := characterOptionTemplateBlob()
	pinned := pinnedNativeLockedBlob(t)
	if changed, ok := characterOptionChangedWordIndexes(template, pinned); !ok ||
		!characterOptionEqualInts(changed, []int{0, 1, 43, 128}) {
		t.Fatalf("template -> locked frame changed %v, want [0 1 43 128] (export also writes word0/1, word43 is difficulty)", changed)
	}
	cdBlob := append([]byte(nil), pinned...)
	characterOptionPutWord(cdBlob, 2, 4)
	characterOptionPutWord(cdBlob, 22, 6)
	characterOptionPutWord(cdBlob, currentCharacterOptionLockFirstIndex, currentCharacterOptionInvalidWord)
	if changed, ok := characterOptionChangedWordIndexes(pinned, cdBlob); !ok ||
		!characterOptionEqualInts(changed, []int{2, 22, 128}) {
		t.Fatalf("locked -> cd-only frame changed %v, want [2 22 128]", changed)
	}
	if changed, ok := characterOptionChangedWordIndexes(pinned, append([]byte(nil), pinned...)); !ok || len(changed) != 0 {
		t.Fatalf("identical payloads reported changes %v", changed)
	}
}

// 「上一份存档里有、这一份里没了」必须能在日志里点名；反方向不点名。
func TestCharacterOptionClearedSkillListsDetectsDefaultTemplateOverwrite(t *testing.T) {
	locked, ok := decodeCharacterOptionSkillLists(pinnedNativeLockedBlob(t))
	if !ok {
		t.Fatal("pinned blob decode failed")
	}
	empty, ok := decodeCharacterOptionSkillLists(characterOptionTemplateBlob())
	if !ok {
		t.Fatal("template decode failed")
	}
	if cleared := characterOptionClearedSkillLists(locked, empty); !characterOptionEqualStrings(cleared, []string{"lock_tree0"}) {
		t.Fatalf("locked -> template cleared = %v, want [lock_tree0]", cleared)
	}
	if cleared := characterOptionClearedSkillLists(empty, locked); len(cleared) != 0 {
		t.Fatalf("empty -> locked cleared = %v, want none (that is a normal menu action)", cleared)
	}
	cdBlob := characterOptionTemplateBlob()
	characterOptionPutWord(cdBlob, 2, 4)
	cdLists, ok := decodeCharacterOptionSkillLists(cdBlob)
	if !ok {
		t.Fatal("cd blob decode failed")
	}
	if cleared := characterOptionClearedSkillLists(cdLists, empty); !characterOptionEqualStrings(cleared, []string{"cd_tree0"}) {
		t.Fatalf("cd -> template cleared = %v, want [cd_tree0]", cleared)
	}
	both := characterOptionTemplateBlob()
	characterOptionPutWord(both, 2, 4)
	characterOptionPutWord(both, 128, 5)
	bothLists, ok := decodeCharacterOptionSkillLists(both)
	if !ok {
		t.Fatal("both blob decode failed")
	}
	if cleared := characterOptionClearedSkillLists(bothLists, empty); !characterOptionEqualStrings(cleared, []string{"lock_tree0", "cd_tree0"}) {
		t.Fatalf("both -> template cleared = %v, want [lock_tree0 cd_tree0]", cleared)
	}
}

func characterOptionEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// 端到端（受控帧，不是玩家捕获）：真实 op4 选角 + 真实 op447 分发 + 真实 390 回放。
// 断言三件事 ——
//  1. 保存端逐字节落库、回放端逐字节发回，含 EXE 已证的 word128 锁槽；
//  2. 每个角色只回放自己的那份帧（不串角色）；
//  3. 日志里能读出「改了哪些 word」「锁表/CD 列表是什么」，以及「锁定列表被清掉」的具名事件。
func TestCharacterOptionSkillWordsSurviveSaveAndReplayWithEvidenceFields(t *testing.T) {
	s, se, r := scope44Fixture(t)
	logPath := filepath.Join(t.TempDir(), "option-words.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })

	lockFrame := pinnedNativeLockedFrame(t)
	cdBlob := append([]byte(nil), pinnedNativeLockedBlob(t)...)
	characterOptionPutWord(cdBlob, 2, 4)
	characterOptionPutWord(cdBlob, 22, 6)
	characterOptionPutWord(cdBlob, currentCharacterOptionLockFirstIndex, currentCharacterOptionInvalidWord)
	cdFrame := characterOptionFrame(cdBlob)

	// 角色29：第一次保存真实锁定帧（此前没有存档）。
	scope44Select(t, s, se, 29)
	if !gameOptionSaveWindowOpen(se) {
		t.Fatal("controlled route did not open the save window")
	}
	scope44Save(t, s, se, lockFrame)
	if got := scope44Stored(t, r, "29"); got != hex.EncodeToString(lockFrame) {
		t.Fatalf("stored frame = %s, want verbatim op447 frame", got)
	}
	scope44Frames(t, se, "words-save-lock-29")

	// 角色29：第二次保存只改 CD 提示列表（并清掉锁定）。
	scope44Save(t, s, se, cdFrame)
	if got := scope44Stored(t, r, "29"); got != hex.EncodeToString(cdFrame) {
		t.Fatalf("stored frame after cd save = %s", got)
	}
	scope44Frames(t, se, "words-save-cd-29")

	// 同连接返回选角再进同一角色：390 必须逐字节回放**已保存**的那一帧（含 CD 列表，锁表已清）。
	// 这一条同时是 DOC-060/114/053「重选后技能锁定/CD 提示被重置」的源码回归。
	scope44Upper(t, s, se, 7, nil)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 29)
	scope44NoticeOrder(t, scope44Frames(t, se, "words-reselect-29"), cdFrame)

	// 冷进入：390 必须逐字节回放该角色自己的帧。
	cold29 := scope44Fresh(se, "words-cold-29")
	scope44Select(t, s, cold29, 29)
	scope44NoticeOrder(t, scope44Frames(t, cold29, "words-cold-29"), cdFrame)

	// 另一个角色保存真实锁定帧；两个角色互不继承。
	other := scope44Fresh(se, "words-other-30")
	scope44Select(t, s, other, 30)
	scope44Save(t, s, other, lockFrame)
	cold30 := scope44Fresh(se, "words-cold-30")
	scope44Select(t, s, cold30, 30)
	scope44NoticeOrder(t, scope44Frames(t, cold30, "words-cold-30"), lockFrame)

	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)

	// 第一次保存：没有上一份存档 ⇒ previous_saved=false，changed_word_indexes=-（不是「没变化」）。
	requireLogLine(t, log, "game-character-option-saved", "char_id=29",
		"previous_saved=false", "changed_word_count=-1", "changed_word_indexes=-",
		"lock_tree0_slots=5", "lock_tree1_slots=-", "cd_tree0_slots=-", "cd_tree1_slots=-",
		"lock_slot_count=1", "cd_slot_count=0", "option_words_decoded=true",
		"option_word_encoding="+characterOptionEncodingTag)
	// 第二次保存：相对上一份只改了 CD 两个词并把 word128 还原，且锁定列表消失必须差点名。
	requireLogLine(t, log, "game-character-option-saved", "char_id=29",
		"previous_saved=true", "changed_word_count=3", "changed_word_indexes=2,22,128",
		"cd_tree0_slots=4", "cd_tree1_slots=6", "lock_tree0_slots=-", "lock_slot_count=0", "cd_slot_count=2")
	requireLogLine(t, log, "game-character-option-skill-lists-cleared", "char_id=29",
		"cleared=lock_tree0", "previous_lock_slot_count=1", "current_lock_slot_count=0",
		"previous_cd_slot_count=0", "current_cd_slot_count=2", "note=diagnostic_only_no_write_guard")
	// 回放日志：两个角色各自的 390 都带着对应列表。
	requireLogLine(t, log, "game-character-option-notice-send", "char_id=29", "cd_tree0_slots=4", "cd_tree1_slots=6", "lock_slot_count=0")
	requireLogLine(t, log, "game-character-option-notice-send", "char_id=30", "lock_tree0_slots=5", "lock_slot_count=1", "cd_slot_count=0")
}
