package dnfbridge

import (
	"fmt"
	"math"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 使用与真实道具不同的编号和概率，防止实现偷偷依赖固定 ID 或 200/400。
func avatarCompoundMaterialRateFixture(t *testing.T) currentAvatarCompoundVariant {
	t.Helper()
	text := "[character type]\n`[swordman]`\n" +
		"[grade]\n1\n[upper grade]\n2\n" +
		"[rare rate]\n`hat` 950\n[/rare rate]\n" +
		"[upper rare rate]\n`hat` 325\n[/upper rare rate]\n" +
		"[hat avatar]\n1 811 300 812 1000\n[/hat avatar]\n" +
		"[material]\n1 700 1\n[/material]\n" +
		"[material]\n1 71 1\n[/material]\n" +
		"[material_upper]\n1 72 1\n[/material_upper]\n" +
		"[material_master]\n1 73 1\n[/material_master]\n"
	variant, err := parseCurrentAvatarCompoundVariant("etc/compoundavatar_swordman.etc", text, "[swordman]")
	if err != nil {
		t.Fatal(err)
	}
	return variant
}

func TestAvatarCompoundMaterialRolesKeepLastDeclaration(t *testing.T) {
	variant := avatarCompoundMaterialRateFixture(t)
	config := currentAvatarCompoundConfig{Variants: []currentAvatarCompoundVariant{variant}}
	for id, want := range map[uint32]string{71: "material", 72: "material_upper", 73: "material_master"} {
		if got := variant.MaterialRoles[id]; got != want || !config.acceptsMaterial(id) {
			t.Fatalf("合成器 %d 的角色为 %q，期望 %q", id, got, want)
		}
	}
	if config.acceptsMaterial(700) || config.acceptsMaterial(0) {
		t.Fatal("早先被覆盖的材料声明或空编号不应准入")
	}
}

func TestAvatarCompoundMaterialRateBonusUsesDeclaredRoleAndSelectedField(t *testing.T) {
	variant := avatarCompoundMaterialRateFixture(t)
	config := currentAvatarCompoundConfig{Variants: []currentAvatarCompoundVariant{variant}}
	tests := []struct {
		name string
		id   uint32
		text string
		want int64
		role string
	}{
		{"普通不需要配置", 71, "", 0, "material"},
		{"普通忽略损坏配置", 71, "[upper bind cube rate bonus]\n`未闭合", 0, "material"},
		{"黄金读取变化数值", 72, "[upper bind cube rate bonus]\n137\n", 137, "material_upper"},
		{"钻石读取变化数值", 73, "[master bind cube rate bonus]\n615\n", 615, "material_master"},
		{"黄金只依赖自己的字段", 72, "[upper bind cube rate bonus]\n321\n[master bind cube rate bonus]\nbad\n", 321, "material_upper"},
		{"钻石只依赖自己的字段", 73, "[upper bind cube rate bonus]\nbad\n[master bind cube rate bonus]\n543\n", 543, "material_master"},
		{"明确零加成", 72, "[upper bind cube rate bonus]\n0\n", 0, "material_upper"},
		{"最大整数留待合并限幅", 73, "[master bind cube rate bonus]\n9223372036854775807\n", math.MaxInt64, "material_master"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := bridgePVFSource{}
			if test.text != "" {
				source[currentAvatarCompoundServerParameterPath] = test.text
			}
			got, role, err := config.materialRateBonus(source, variant, test.id)
			if err != nil || got != test.want || role != test.role {
				t.Fatalf("加成=%d 角色=%q 错误=%v，期望 %d/%s", got, role, err, test.want, test.role)
			}
		})
	}
	if got, role, err := config.materialRateBonus(nil, variant, 71); err != nil || got != 0 || role != "material" {
		t.Fatalf("普通合成器不应读取概率来源：%d %s %v", got, role, err)
	}
}

func TestAvatarCompoundSpecialMaterialRateRejectsUnavailableOrMalformedField(t *testing.T) {
	variant := avatarCompoundMaterialRateFixture(t)
	config := currentAvatarCompoundConfig{Variants: []currentAvatarCompoundVariant{variant}}
	for _, special := range []struct {
		id    uint32
		field string
		role  string
	}{
		{72, "upper bind cube rate bonus", "material_upper"},
		{73, "master bind cube rate bonus", "material_master"},
	} {
		for name, body := range map[string]string{
			"空值": "", "负数": "-1", "小数": "1.5", "字符串": "`400`",
			"多个整数": "200 400", "文字": "bad", "超出整数": "9223372036854775808",
		} {
			t.Run(special.role+"/"+name, func(t *testing.T) {
				source := bridgePVFSource{currentAvatarCompoundServerParameterPath: fmt.Sprintf("[%s]\n%s\n", special.field, body)}
				if _, role, err := config.materialRateBonus(source, variant, special.id); err == nil || role != special.role {
					t.Fatalf("畸形概率必须明确失败，角色=%s 错误=%v", role, err)
				}
			})
		}
		for name, source := range map[string]dnfpvf.Source{
			"来源缺失": nil,
			"文件缺失": bridgePVFSource{},
			"字段缺失": bridgePVFSource{currentAvatarCompoundServerParameterPath: "[lower bind cube rate bonus]\n4000\n"},
			"文本损坏": bridgePVFSource{currentAvatarCompoundServerParameterPath: "`未闭合"},
			"重复字段": bridgePVFSource{currentAvatarCompoundServerParameterPath: fmt.Sprintf("[%s]\n1\n[%s]\n2\n", special.field, special.field)},
		} {
			t.Run(special.role+"/"+name, func(t *testing.T) {
				if _, role, err := config.materialRateBonus(source, variant, special.id); err == nil || role != special.role {
					t.Fatalf("不应静默降级普通概率，角色=%s 错误=%v", role, err)
				}
			})
		}
	}
	if _, _, err := config.materialRateBonus(nil, variant, 999); err == nil {
		t.Fatal("未声明的合成器不应得到默认概率")
	}
}

func TestAvatarCompoundMaterialRolePreservesExistingVariantAdmission(t *testing.T) {
	classic := avatarCompoundMaterialRateFixture(t)
	cn := currentAvatarCompoundVariant{Path: "etc/chn_compound/compoundavatar_swordman.etc", MaterialIDs: []uint32{81}, MaterialRoles: map[uint32]string{81: "material"}}
	config := currentAvatarCompoundConfig{Variants: []currentAvatarCompoundVariant{classic, cn}}
	if got, role, err := config.materialRateBonus(nil, cn, 71); err != nil || got != 0 || role != "material" {
		t.Fatalf("保留普通合成器对已可用变体的准入：%d %s %v", got, role, err)
	}
	source := bridgePVFSource{currentAvatarCompoundServerParameterPath: "[upper bind cube rate bonus]\n137\n"}
	if got, role, err := config.materialRateBonus(source, cn, 72); err != nil || got != 137 || role != "material_upper" {
		t.Fatalf("跨变体材料仍应保留来源角色：%d %s %v", got, role, err)
	}
	// 同一编号在不同变体有不同角色时，选中池的显式声明具有优先权。
	cn.MaterialRoles[72] = "material"
	if got, role, err := config.materialRateBonus(nil, cn, 72); err != nil || got != 0 || role != "material" {
		t.Fatalf("未优先采用所选配置的角色：%d %s %v", got, role, err)
	}
}

func TestAvatarCompoundMaterialBonusCombinesWithBaseAndClamps(t *testing.T) {
	variant := avatarCompoundMaterialRateFixture(t)
	for _, test := range []struct {
		name                        string
		grade1, grade2, bonus, want int64
	}{
		{"普通不变", 1, 1, 0, 950},
		{"高级不变", 2, 2, 0, 1275},
		{"混合整除不变", 1, 2, 0, 1112},
		{"普通加成", 1, 1, 137, 1087},
		{"高级加成", 2, 2, 615, 1890},
		{"混合加成", 2, 1, 615, 1727},
		{"恰到上限", 1, 1, 9050, 10000},
		{"超过上限", 2, 2, 9999, 10000},
		{"巨大数值不溢出", 1, 1, math.MaxInt64, 10000},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := variant.bonusRateWithMaterial("hat", test.grade1, test.grade2, test.bonus); got != test.want {
				t.Fatalf("合并概率=%d，期望 %d", got, test.want)
			}
		})
	}
}
