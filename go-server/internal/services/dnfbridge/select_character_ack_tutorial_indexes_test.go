package dnfbridge

// 选角 ACK 教程索引列表（账号循环副本教程索引 13）的回归测试。
//
// 这是一个**回补失落实现**的修复：同一血统的 2026-09-24 归档里
// `select_character_ack.go` 有 currentSelectAckTutorialIndexesForCharacter()，
// 会为 Lv20+ 的既有角色发 [13,31]；在役树把这一段写死成单元素 [31]，
// 索引 13 从未被编码过 ⇒ 角色的 tutorial_flags[13] 永远是 0。
//
// 证据：
//   - 归档 PROTOCOL_INDEX.md:55 条目 "class1/op4 select ACK: initial town, not
//     roster slot (2026-09-15)"：`for an established character below Lv20 keep
//     [31]; for Lv20+ keep [13,31], where account tutorialtextaccount ID13 is the
//     loop-dungeon tutorial and index31 remains the proved page-1 route.
//     New/practice-tutorial characters still keep the empty list. Live retest
//     confirms [13,31] is actually received by the Lv64 client ...`
//   - 归档实现（同一函数）+ 归档测试断言 wantTutorial = {0, 2, 13, 31}。
//   - 线上 packet_log.txt 183/183 条
//     `game-upper-select-character-ack-current-fields` 全是
//     `tutorial_count=1 tutorial_indexes=[31]`，其中 0 条含 13。
//   - 线上 dnf_characters：角色 6/1（Lv90, tutorial_completed=1）—— 都落在 Lv20+ 档。
//
// 索引 13 < 31 ⇒ 列表最大值不变 ⇒ 教程路由仍是已证实的 page-1 档 31，
// 逐字节差别只有"多一个 0x0d 索引 + 计数 1→2"。

import (
	"bytes"
	"encoding/hex"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// selectAckTutorialLegacyGoldenHex 是**修复前**的整包字节（与 selectAckTownGoldenHex
// 同一个确定性 fixture，只是走回滚档）。它的存在就是为了证明 `legacy` 是**逐字节**
// 回退，而不是"数值接近"：回滚档必须重建出这一串，一个 bit 都不许差。
const selectAckTutorialLegacyGoldenHex = "010000000000000000010000009c00000000393000006400030000002c0101000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff0000000000000000000000000000000000000000000000002600011f0000000000000000"

func selectAckTutorialTestCharacter(level int, tutorialCompleted bool) dnfrepo.CharacterRecord {
	flag := int64(0)
	if tutorialCompleted {
		flag = currentDungeonTutorialCompleteFlag
	}
	return dnfrepo.CharacterRecord{Level: level, Stats: map[string]int64{
		currentDungeonTutorialCompletedKey: flag,
		"fatigue":                          156,
		"fatigue_limit":                    156,
	}}
}

// TestSelectAckTutorialIndexesAccountLoopDungeonLevelGate 钉死"哪一档发哪些索引"：
// Lv20 以下只发 page-1 路由 31；Lv20 及以上先发账号循环副本教程 13 再发 31；
// 待教程角色（tutorial_completed=0）保持空列表。
func TestSelectAckTutorialIndexesAccountLoopDungeonLevelGate(t *testing.T) {
	t.Setenv(currentSelectAckTutorialIndexesEnv, "")
	cases := []struct {
		name  string
		level int
		want  []byte
	}{
		{"level_1_below_gate", 1, []byte{currentSelectAckPage1RouteIndex}},
		{"level_19_below_gate", 19, []byte{currentSelectAckPage1RouteIndex}},
		{"level_20_at_gate", 20, []byte{currentSelectAckAccountLoopDungeonTutorialIndex, currentSelectAckPage1RouteIndex}},
		{"level_86_established", 86, []byte{currentSelectAckAccountLoopDungeonTutorialIndex, currentSelectAckPage1RouteIndex}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildCurrentSelectCharacterAckBody(
				selectAckTutorialTestCharacter(tc.level, true), true,
				dnfrepo.QuestRecord{}, false, 6, newCharacterInitialTownID, 0, []byte{0})
			// 被忽略标志 1 字节 + count 1 字节 + count 个索引，必须逐字节等于期望。
			want := make([]byte, 0, 2+len(tc.want))
			want = append(want, 0, byte(len(tc.want)))
			want = append(want, tc.want...)
			tail := selectAckTownOffset(body) + 1
			if got := body[tail : tail+len(want)]; !bytes.Equal(got, want) {
				t.Fatalf("select ACK tutorial tail = % x, want % x", got, want)
			}
			flag, count, ok := currentSelectAckTutorialState(body)
			if !ok || flag != 0 || int(count) != len(tc.want) {
				t.Fatalf("tutorial state flag=%d count=%d ok=%t, want 0/%d/true", flag, count, ok, len(tc.want))
			}
			indexes, ok := currentSelectAckTutorialIndexes(body)
			if !ok || !bytes.Equal(indexes, tc.want) {
				t.Fatalf("tutorial indexes=%v ok=%t, want %v/true", indexes, ok, tc.want)
			}
			// 教程路由 = 列表最大值。补回 13 之后它必须仍然是已证实的 31。
			maximum := byte(0)
			for _, index := range indexes {
				if index > maximum {
					maximum = index
				}
			}
			if maximum != currentSelectAckPage1RouteIndex {
				t.Fatalf("tutorial route maximum=%d, want the proved page-1 route %d", maximum, currentSelectAckPage1RouteIndex)
			}
		})
	}
}

// TestSelectAckTutorialIndexesPendingCharacterKeepsEmptyList 覆盖新建/待教程角色：
// 任何等级都必须保持空列表，且与修复前逐字节相同（{0, 0}）。
func TestSelectAckTutorialIndexesPendingCharacterKeepsEmptyList(t *testing.T) {
	t.Setenv(currentSelectAckTutorialIndexesEnv, "")
	for _, level := range []int{1, 19, 20, 86} {
		body := buildCurrentSelectCharacterAckBody(
			selectAckTutorialTestCharacter(level, false), true,
			dnfrepo.QuestRecord{}, false, 6, newCharacterInitialTownID, 0, []byte{0})
		tail := selectAckTownOffset(body) + 1
		if got := body[tail : tail+2]; !bytes.Equal(got, []byte{0, 0}) {
			t.Fatalf("pending character level=%d tutorial tail = % x, want 00 00", level, got)
		}
		indexes, ok := currentSelectAckTutorialIndexes(body)
		if !ok || len(indexes) != 0 {
			t.Fatalf("pending character level=%d indexes=%v ok=%t, want empty/true", level, indexes, ok)
		}
	}
}

// TestSelectAckTutorialIndexesLegacyRollbackIsByteIdentical 是逐字节回退证明：
// 回退字面量（legacy/off/0/false/no，大小写与空白不敏感）必须重建出修复前的整包字节。
func TestSelectAckTutorialIndexesLegacyRollbackIsByteIdentical(t *testing.T) {
	character, quests := selectAckTownGoldenFixture()
	for _, literal := range []string{"legacy", "LEGACY", " off ", "0", "false", "no"} {
		t.Run(literal, func(t *testing.T) {
			t.Setenv(currentSelectAckTutorialIndexesEnv, literal)
			if !currentSelectAckTutorialIndexesRollbackToPage1Only() {
				t.Fatalf("rollback literal %q did not select the legacy tier", literal)
			}
			if got := currentSelectAckTutorialIndexesMode(); got != "legacy_page1_only_31" {
				t.Fatalf("rollback literal %q mode=%q", literal, got)
			}
			body := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, newCharacterInitialTownID, 12345, []byte{0})
			if got := hex.EncodeToString(body); got != selectAckTutorialLegacyGoldenHex {
				t.Fatalf("legacy rollback is not byte-identical:\n got=%s\nwant=%s", got, selectAckTutorialLegacyGoldenHex)
			}
		})
	}
}

// TestSelectAckTutorialIndexesDefaultDiffersFromLegacyByExactlyTheMissingIndex
// 把"修复"与"回退"的差值钉成一个字面量：长度 +1，且回滚包在 count 之后插入
// `0d 1f`（13,31）替换掉原来的 `1f`（31）后，必须与默认档逐字节相等。
func TestSelectAckTutorialIndexesDefaultDiffersFromLegacyByExactlyTheMissingIndex(t *testing.T) {
	character, quests := selectAckTownGoldenFixture()
	t.Setenv(currentSelectAckTutorialIndexesEnv, "legacy")
	legacy := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, newCharacterInitialTownID, 12345, []byte{0})
	if got := hex.EncodeToString(legacy); got != selectAckTutorialLegacyGoldenHex {
		t.Fatalf("legacy baseline drifted:\n got=%s\nwant=%s", got, selectAckTutorialLegacyGoldenHex)
	}
	t.Setenv(currentSelectAckTutorialIndexesEnv, "")
	if got := currentSelectAckTutorialIndexesMode(); got != "account_loop_dungeon_13_plus_page1_31" {
		t.Fatalf("default mode=%q", got)
	}
	fixed := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, newCharacterInitialTownID, 12345, []byte{0})
	if len(fixed) != len(legacy)+1 {
		t.Fatalf("fix changed ACK size by %d, want exactly +1 byte", len(fixed)-len(legacy))
	}
	tail := selectAckTownOffset(legacy) + 1
	rebuilt := append([]byte(nil), legacy[:tail]...)
	rebuilt = append(rebuilt, byte(0), byte(2), currentSelectAckAccountLoopDungeonTutorialIndex, currentSelectAckPage1RouteIndex)
	rebuilt = append(rebuilt, legacy[tail+3:]...)
	if !bytes.Equal(rebuilt, fixed) {
		t.Fatalf("default packet is not legacy plus exactly the account loop dungeon index:\n got=%x\nwant=%x", fixed, rebuilt)
	}
}

// TestSelectAckTutorialIndexesReachTheWireForEstablishedLv20Character 走真实选角入口，
// 证明修好的索引真的出现在下发帧里（验收口径：线上日志必须是 [13,31] 而不是 [31]）。
func TestSelectAckTutorialIndexesReachTheWireForEstablishedLv20Character(t *testing.T) {
	t.Setenv(currentSelectAckTutorialIndexesEnv, "")
	// selectAckFixtureFor 的 fixture 角色固定 Level 86 + tutorial_completed=1。
	_, service, freshSession := selectAckFixtureFor(t, []int{0}, currentDungeonTutorialCompleteFlag, 38)
	body := selectAckBodyForSlot(t, service, freshSession(), 0)
	indexes, ok := currentSelectAckTutorialIndexes(body)
	if !ok || !bytes.Equal(indexes, []byte{currentSelectAckAccountLoopDungeonTutorialIndex, currentSelectAckPage1RouteIndex}) {
		t.Fatalf("wire tutorial indexes=%v ok=%t, want [13 31]/true", indexes, ok)
	}
	if _, count, ok := currentSelectAckTutorialState(body); !ok || count != 2 {
		t.Fatalf("wire tutorial count=%d ok=%t, want 2/true", count, ok)
	}
}
