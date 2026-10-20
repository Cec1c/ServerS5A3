package dnfbridge

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestCharacterGrowTypeHelpersMatchTheLegacyStatReader 是本轮"统一访问器"的兼容性
// 回归：新的 helper 必须与原 `numericCharacterStatValue(record, "grow_type")`
// （及其外层 byte()/int() 转换）逐位相同，否则 30+ 个投影点会输出不同字节。
func TestCharacterGrowTypeHelpersMatchTheLegacyStatReader(t *testing.T) {
	tests := []struct {
		name  string
		stats map[string]int64
	}{
		{name: "nil stats", stats: nil},
		{name: "key missing", stats: map[string]int64{"exp": 5}},
		{name: "untransferred", stats: map[string]int64{"grow_type": 0}},
		{name: "first transfer", stats: map[string]int64{"grow_type": 1}},
		{name: "second awakening", stats: map[string]int64{"grow_type": 0x21}},
		{name: "negative", stats: map[string]int64{"grow_type": -3}},
		{name: "above domain", stats: map[string]int64{"grow_type": 300}},
	}
	for _, test := range tests {
		record := dnfrepo.CharacterRecord{Stats: test.stats}
		legacy := numericCharacterStatValue(record, "grow_type")
		if got := characterGrowTypeStat(record); got != legacy {
			t.Fatalf("%s: characterGrowTypeStat = %d, legacy = %d", test.name, got, legacy)
		}
		if got := characterGrowTypeInt(record); got != int(legacy) {
			t.Fatalf("%s: characterGrowTypeInt = %d, legacy = %d", test.name, got, int(legacy))
		}
		if got := characterGrowTypeByte(record); got != byte(legacy) {
			t.Fatalf("%s: characterGrowTypeByte = %#x, legacy = %#x", test.name, got, byte(legacy))
		}
	}
}

// TestCharacterGrowTypePresenceSeparatesMissingFromZero 锁定 presence 语义，并给
// 审计事件提供稳定字段。
func TestCharacterGrowTypePresenceSeparatesMissingFromZero(t *testing.T) {
	missing := characterGrowType(dnfrepo.CharacterRecord{Stats: map[string]int64{"exp": 1}})
	if missing.Present || missing.InDomain {
		t.Fatalf("missing key must not be reported as present: %+v", missing)
	}
	if missing.Packed != 0 || missing.Branch != 0 || missing.Awakening != 0 {
		t.Fatalf("missing key must render as the legacy zero value: %+v", missing)
	}

	untransferred := characterGrowType(dnfrepo.CharacterRecord{Stats: map[string]int64{"grow_type": 0}})
	if !untransferred.Present || !untransferred.InDomain {
		t.Fatalf("an explicit 0 is a legal persisted value: %+v", untransferred)
	}

	packed := characterGrowType(dnfrepo.CharacterRecord{Stats: map[string]int64{"grow_type": 0x13}})
	if packed.Branch != 3 || packed.Awakening != 1 {
		t.Fatalf("0x13 must decode to branch 3 / awakening 1: %+v", packed)
	}

	outOfDomain := characterGrowType(dnfrepo.CharacterRecord{Stats: map[string]int64{"grow_type": 256}})
	if !outOfDomain.Present || outOfDomain.InDomain {
		t.Fatalf("256 is outside the TINYINT domain: %+v", outOfDomain)
	}
}

// TestCharacterGrowTypeAuditStatePinsTheJudgementFields 钉住判读串里必须出现的字段，
// 让操作者一行日志就能回答"这个 0 是哪来的、算不算合法"。
func TestCharacterGrowTypeAuditStatePinsTheJudgementFields(t *testing.T) {
	record := dnfrepo.CharacterRecord{Stats: map[string]int64{"grow_type": 0x21}}
	state := characterGrowTypeAuditState(record)
	fields := map[string]any{}
	for idx := 0; idx+1 < len(state); idx += 2 {
		key, ok := state[idx].(string)
		if !ok {
			t.Fatalf("audit field key %d is not a string: %#v", idx, state[idx])
		}
		fields[key] = state[idx+1]
	}
	if fields["grow_type"] != 0x21 {
		t.Fatalf("grow_type = %#v, want 0x21", fields["grow_type"])
	}
	if fields["grow_type_present"] != true || fields["grow_type_in_domain"] != true {
		t.Fatalf("presence flags = %#v", fields)
	}
	if fields["grow_branch"] != 1 || fields["grow_awakening"] != 2 {
		t.Fatalf("decoded components = %#v", fields)
	}
	if fields["grow_type_source"] != "dnf_characters.grow_type" {
		t.Fatalf("grow_type_source = %#v", fields["grow_type_source"])
	}
}

// TestFormatSkillIDsMatchesTheExistingEventStyle 保证审计事件的列表字段可 grep。
func TestFormatSkillIDsMatchesTheExistingEventStyle(t *testing.T) {
	if got := formatSkillIDs(nil); got != "[]" {
		t.Fatalf("empty = %q, want []", got)
	}
	if got := formatSkillIDs([]uint16{27, 33, 110}); got != "[27 33 110]" {
		t.Fatalf("list = %q, want [27 33 110]", got)
	}
}

// TestSkillGrowTypeAuditSwitchDefaultsOn 锁定新判读事件的开关默认值与回退口径。
func TestSkillGrowTypeAuditSwitchDefaultsOn(t *testing.T) {
	t.Setenv(skillGrowTypeAuditEnv, "")
	if !skillGrowTypeAuditEnabled() {
		t.Fatal("audit must default to on (it is read-only)")
	}
	t.Setenv(skillGrowTypeAuditEnv, "off")
	if skillGrowTypeAuditEnabled() {
		t.Fatal("off must disable the audit event")
	}
}
