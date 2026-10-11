package gmtool

import (
	"errors"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type grantDurabilityTestSource map[string]string

func (s grantDurabilityTestSource) ReadText(relativePath string) (string, error) {
	text, ok := s[relativePath]
	if !ok {
		return "", errors.New("test PVF document missing: " + relativePath)
	}
	return text, nil
}

// 发放端：装备附件必须在发放当时就带 durability/max_durability（运行时 PVF 的
// [durability]），堆叠物一律不写 —— 这正是"GM 发的装备 0 耐久、修不了"的修复面。
func TestGrantAttachmentExtraCarriesEquipmentDurabilityOnly(t *testing.T) {
	equipment := grantAttachmentExtra(itemEntry{
		ID:      9001,
		Kind:    "equipment",
		Tag:     "weapon",
		PVFPath: "weapon/test_sword.equ",
	}, 57)
	if equipment[grantExtraKeyDurability] != "57" || equipment[grantExtraKeyMaxDurability] != "57" {
		t.Fatalf("grant equipment durability = %+v, want both keys 57", equipment)
	}
	if equipment[grantExtraKeyPVFKind] != "equipment" || equipment[grantExtraKeyEquipmentType] != "weapon" {
		t.Fatalf("grant equipment framing evidence = %+v", equipment)
	}

	// 堆叠物：没有耐久语义，即使调用方传了值也不许写。
	stackable := grantAttachmentExtra(itemEntry{
		ID:      490701731,
		Kind:    "stackable",
		Tag:     "material",
		PVFPath: "material/chn_490701731.stk",
	}, 57)
	if _, ok := stackable[grantExtraKeyDurability]; ok {
		t.Fatalf("stackable grant gained durability: %+v", stackable)
	}
	if _, ok := stackable[grantExtraKeyMaxDurability]; ok {
		t.Fatalf("stackable grant gained max_durability: %+v", stackable)
	}

	// 装备但 PVF 没声明耐久：不发明一个值。
	noDurability := grantAttachmentExtra(itemEntry{ID: 9002, Kind: "equipment", Tag: "coat"}, 0)
	if _, ok := noDurability[grantExtraKeyDurability]; ok {
		t.Fatalf("equipment without PVF durability invented one: %+v", noDurability)
	}
}

// 发放端读耐久：路径按 kind 归一化后读运行时 PVF，与 dungeon_drop_catalog 同源；
// 读不到就返回 0（不阻断发放），领取端还会再补一次。
func TestGrantPVFDurabilityReadsRuntimePVF(t *testing.T) {
	source := grantDurabilityTestSource{
		"equipment/weapon/test_sword.equ": "[name] `Test Sword`\n[durability] 57\n",
		"equipment/weapon/bare_coat.equ":  "[name] `Bare Coat`\n",
		"equipment/weapon/max_only.equ":   "[name] `Max Only`\n[maximum durability] 45\n",
	}
	server := &Server{
		deps: Deps{PVF: func() (dnfpvf.Source, error) { return source, nil }},
		cfg:  Config{Logger: func(string, ...any) {}},
	}
	tests := map[string]struct {
		entry itemEntry
		want  uint16
	}{
		"durability": {
			entry: itemEntry{ID: 9001, Kind: "equipment", PVFPath: "weapon/test_sword.equ"},
			want:  57,
		},
		"maximum durability fallback": {
			entry: itemEntry{ID: 9004, Kind: "equipment", PVFPath: "weapon/max_only.equ"},
			want:  45,
		},
		"no durability declared": {
			entry: itemEntry{ID: 9002, Kind: "equipment", PVFPath: "weapon/bare_coat.equ"},
			want:  0,
		},
		"unreadable document degrades to no evidence": {
			entry: itemEntry{ID: 9003, Kind: "equipment", PVFPath: "weapon/missing.equ"},
			want:  0,
		},
		"stackable is never probed": {
			entry: itemEntry{ID: 490701731, Kind: "stackable", PVFPath: "material/chn.stk"},
			want:  0,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := server.grantPVFDurability(test.entry); got != test.want {
				t.Fatalf("grantPVFDurability = %d, want %d", got, test.want)
			}
		})
	}
}
