package repository

import "testing"

// TestCharacterGrowTypeDistinguishesMissingFromUntransferred 锁定本 accessor 存在的
// 唯一理由：`grow_type` 的 "0" 与 "键缺失" 在分支技能门禁里结论完全相反。
//
// 线上历史证据（2026-09-24 09:24 的 `skill=16 grow=0`）曾被读成"grow_type 从未
// 落库"，而该角色当时确实未转职；只有 presence 才能把这两种情况分开。
func TestCharacterGrowTypeDistinguishesMissingFromUntransferred(t *testing.T) {
	tests := []struct {
		name     string
		stats    map[string]int64
		want     int
		present  bool
		inDomain bool
	}{
		{name: "nil stats", stats: nil, want: 0, present: false, inDomain: false},
		{name: "key missing", stats: map[string]int64{"exp": 12}, want: 0, present: false, inDomain: false},
		{name: "legal untransferred", stats: map[string]int64{"grow_type": 0}, want: 0, present: true, inDomain: true},
		{name: "first transfer", stats: map[string]int64{"grow_type": 1}, want: 1, present: true, inDomain: true},
		{name: "second awakening packed", stats: map[string]int64{"grow_type": 0x22}, want: 0x22, present: true, inDomain: true},
		{name: "negative is not present", stats: map[string]int64{"grow_type": -1}, want: 0, present: false, inDomain: false},
		{name: "above tinyint domain", stats: map[string]int64{"grow_type": 256}, want: 256, present: true, inDomain: false},
	}
	for _, test := range tests {
		record := CharacterRecord{Stats: test.stats}
		got, present := CharacterGrowType(record)
		if got != test.want || present != test.present {
			t.Fatalf("%s: CharacterGrowType() = (%d, %t), want (%d, %t)", test.name, got, present, test.want, test.present)
		}
		inDomainValue, inDomain := CharacterGrowTypeInDomain(record)
		if inDomain != test.inDomain {
			t.Fatalf("%s: CharacterGrowTypeInDomain() present = %t, want %t", test.name, inDomain, test.inDomain)
		}
		if inDomain && inDomainValue != test.want {
			t.Fatalf("%s: CharacterGrowTypeInDomain() = %d, want %d", test.name, inDomainValue, test.want)
		}
	}
}

// TestDecodeCharacterGrowType 钉住打包语义：低四位=转职分支，高四位=觉醒段。
// 真档实测值来自第七批的现场（圣职者 job 14）：0x00 圣职者 / 0x01 圣骑士 /
// 0x11 福音传道者 / 0x21 炽天使；角色 5 的 0x13 = 分支 3 + 一觉。
func TestDecodeCharacterGrowType(t *testing.T) {
	tests := []struct {
		packed int
		branch int
		awaken int
	}{
		{packed: 0x00, branch: 0, awaken: 0},
		{packed: 0x01, branch: 1, awaken: 0},
		{packed: 0x11, branch: 1, awaken: 1},
		{packed: 0x21, branch: 1, awaken: 2},
		{packed: 0x13, branch: 3, awaken: 1},
		{packed: -1, branch: 0, awaken: 0},
	}
	for _, test := range tests {
		branch, awaken := DecodeCharacterGrowType(test.packed)
		if branch != test.branch || awaken != test.awaken {
			t.Fatalf("decode(%#x) = (%d, %d), want (%d, %d)", test.packed, branch, awaken, test.branch, test.awaken)
		}
		if got := CharacterGrowTypeBranch(test.packed); got != test.branch {
			t.Fatalf("CharacterGrowTypeBranch(%#x) = %d, want %d", test.packed, got, test.branch)
		}
		if got := CharacterGrowTypeAwakeningStage(test.packed); got != test.awaken {
			t.Fatalf("CharacterGrowTypeAwakeningStage(%#x) = %d, want %d", test.packed, got, test.awaken)
		}
	}
}

// TestCharacterGrowTypeStatKeyIsThePersistedColumn 保证规范键名与
// `dnf_characters.grow_type` 列名一致（mysql 侧以该字符串做 mirror 映射）。
func TestCharacterGrowTypeStatKeyIsThePersistedColumn(t *testing.T) {
	if CharacterGrowTypeStatKey != "grow_type" {
		t.Fatalf("CharacterGrowTypeStatKey = %q, want %q", CharacterGrowTypeStatKey, "grow_type")
	}
}
