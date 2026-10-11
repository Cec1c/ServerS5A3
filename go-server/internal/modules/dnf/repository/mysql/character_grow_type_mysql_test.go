package mysql

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// TestCharacterMirrorStatKeysCoverTheWholeColumnSet 保证"哪些键住在列里"这一判断
// 完全由 mirror 列集合派生，不会有第二份清单漂移。
func TestCharacterMirrorStatKeysCoverTheWholeColumnSet(t *testing.T) {
	if !isMySQLCharacterMirrorStatKey(repository.CharacterGrowTypeStatKey) {
		t.Fatalf("%s must be a mirror (column-backed) stat key", repository.CharacterGrowTypeStatKey)
	}
	if !isMySQLCharacterMirrorStatKey("exp") || !isMySQLCharacterMirrorStatKey("stat_strength") {
		t.Fatal("known mirror columns are not recognised")
	}
	for _, key := range []string{"dungeon_settlement_receipt_version", "honor_expert_level", "prev_village_town_id"} {
		if isMySQLCharacterMirrorStatKey(key) {
			t.Fatalf("%s is an extra-stats key and must not be treated as a mirror column", key)
		}
	}
	if len(mysqlCharacterMirrorStatKeys) != len(mysqlCharacterStatColumns) {
		t.Fatalf("mirror key set = %d entries, columns = %d", len(mysqlCharacterMirrorStatKeys), len(mysqlCharacterStatColumns))
	}
}

// TestMergeCharacterExtraStatKeepsTheAuthoritativeColumn 是"grow_type 权威列不得被
// `dnf_character_stats` 影子行覆盖"的单元回归。
//
// 线上该表按设计永远没有 grow_type 行（replaceCharacterExtraStats 跳过 mirror 键），
// 但只要出现过一行（历史 stats_json 迁移 / 手工 SQL），旧实现就会让它在 mirror 之后
// 覆盖权威列 —— 一个已转职角色会被静默改回未转职。
func TestMergeCharacterExtraStatKeepsTheAuthoritativeColumn(t *testing.T) {
	// 默认档：权威列优先，影子行被丢弃。
	record := repository.CharacterRecord{
		Stats: map[string]int64{repository.CharacterGrowTypeStatKey: 0x21},
	}
	if applied := mergeCharacterExtraStat(&record, repository.CharacterGrowTypeStatKey, 0, true); applied {
		t.Fatal("shadow grow_type row must not be applied while the mirror column is authoritative")
	}
	if got := record.Stats[repository.CharacterGrowTypeStatKey]; got != 0x21 {
		t.Fatalf("authoritative grow_type = %#x, want 0x21", got)
	}
	// 非 mirror 的额外键照常合并（线上真实存在的键）。
	if applied := mergeCharacterExtraStat(&record, "honor_expert_level", 7, true); !applied {
		t.Fatal("extra stats keys must still merge")
	}
	if got := record.Stats["honor_expert_level"]; got != 7 {
		t.Fatalf("honor_expert_level = %d, want 7", got)
	}

	// 回退档（DNFBRIDGE_CHARACTER_MIRROR_STATS_AUTHORITATIVE=off 的语义）：旧行为，
	// 影子行覆盖权威列。
	legacy := repository.CharacterRecord{
		Stats: map[string]int64{repository.CharacterGrowTypeStatKey: 0x21},
	}
	if applied := mergeCharacterExtraStat(&legacy, repository.CharacterGrowTypeStatKey, 0, false); !applied {
		t.Fatal("legacy mode must apply the shadow row")
	}
	if got := legacy.Stats[repository.CharacterGrowTypeStatKey]; got != 0 {
		t.Fatalf("legacy grow_type = %d, want the shadow row 0", got)
	}
}

// TestCharacterMirrorStatsAuthoritativeSwitchDefaultsOn 锁定一键回退开关的默认值与
// 解析口径（未设置=on；off/0/false/no=off；大小写与空白无关）。
func TestCharacterMirrorStatsAuthoritativeSwitchDefaultsOn(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
	if !characterMirrorStatsAuthoritative() {
		t.Fatal("unset switch must default to authoritative columns")
	}
	for _, value := range []string{"off", "OFF", " 0 ", "false", "no"} {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, value)
		if characterMirrorStatsAuthoritative() {
			t.Fatalf("%q must roll back to the legacy merge order", value)
		}
	}
	for _, value := range []string{"on", "1", "true", "yes"} {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, value)
		if !characterMirrorStatsAuthoritative() {
			t.Fatalf("%q must keep the authoritative column", value)
		}
	}
}
