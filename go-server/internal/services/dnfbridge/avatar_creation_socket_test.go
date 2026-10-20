package dnfbridge

import "testing"

// 创建期徽章孔由运行时 PVF 的声明决定，而不是槽位：
//   - 光环 [aurora avatar] 用 [emblem socket default]（国庆光环 = 2 × M）；
//   - 皮肤 [skin avatar] 用 [avatar emblem socket num] 2 + [avatar type select]
//     末尾的两个 `[M socket]`（真实 415580009「兽人英雄白嫩皮肤」就是这个形状）；
//   - 普通部位两者都没有，[avatar type select] 只是打孔可选项，绝不能预开。
func TestCurrentParseAvatarCreationSocketTypes(t *testing.T) {
	const skinText = "[equipment type]\n`[skin avatar]`\n[avatar emblem socket num]\n2\n" +
		"[avatar type select]\n7 0 0 1000 0 0 0 30 0 0 2000 0 0 0 0 0 0 4000 0 0 0 0 0 0 4400 0 0 2 `[M socket]` `[M socket]`\n[/avatar type select]\n"
	const hatText = "[equipment type]\n`[hat avatar]`\n" +
		"[avatar type select]\n7 0 0 300 0 0 0 30 0 0 660 0 0 0 0 0 0 1980 0 0 0 0 0 0 4500 0 0 2 `[A socket]` `[A socket]`\n[/avatar type select]\n"
	const auraText = "[equipment type]\n`[aurora avatar]`\n" +
		"[emblem socket default]\n`[M socket]` `[M socket]`\n[/emblem socket default]\n"
	const plainText = "[equipment type]\n`[coat avatar]`\n"

	for _, test := range []struct {
		name string
		text string
		want []byte
	}{
		{name: "skin declares two M sockets", text: skinText, want: []byte{0xEF, 0xEF}},
		{name: "aura declares emblem socket default", text: auraText, want: []byte{0xEF, 0xEF}},
		{name: "ordinary hat lists drillable types only", text: hatText, want: nil},
		{name: "ordinary coat declares nothing", text: plainText, want: nil},
	} {
		got := currentParseAvatarCreationSocketTypes(test.text)
		if len(got) != len(test.want) {
			t.Fatalf("%s: creation sockets=%v, want %v", test.name, got, test.want)
		}
		for index := range got {
			if got[index] != test.want[index] {
				t.Fatalf("%s: creation sockets=%v, want %v", test.name, got, test.want)
			}
		}
	}
	if got := currentResolveDeclaredAvatarSocketNum(hatText); got != 0 {
		t.Fatalf("ordinary part declared sockets=%d, want 0", got)
	}
	if got := currentResolveDeclaredAvatarSocketNum(skinText); got != 2 {
		t.Fatalf("skin declared sockets=%d, want 2", got)
	}
}

// currentAvatarCreationSocketTypes 是三条发放路径（点券行/礼包/邮件领取）共用的
// 取孔入口：皮肤按声明取孔，普通部位返回 nil，PVF 读取失败也不能把发放整体变成错误。
func TestCurrentAvatarCreationSocketTypes(t *testing.T) {
	source := bridgePVFSource{
		"equipment/avatar/skin.equ":     "[equipment type]\n`[skin avatar]`\n[avatar emblem socket num]\n2\n[avatar type select]\n0 2 `[M socket]` `[M socket]`\n[/avatar type select]\n",
		"equipment/avatar/aura.equ":     "[equipment type]\n`[aurora avatar]`\n[emblem socket default]\n`[M socket]` `[M socket]`\n[/emblem socket default]\n",
		"equipment/avatar/coat.equ":     "[equipment type]\n`[coat avatar]`\n[avatar type select]\n0 2 `[M socket]` `[M socket]`\n[/avatar type select]\n",
		"equipment/avatar/skinless.equ": "[equipment type]\n`[skin avatar]`\n[avatar type select]\n0 2 `[M socket]` `[M socket]`\n[/avatar type select]\n",
	}
	skinRule, _ := currentEquipmentPlacementRuleForPVFType("[skin avatar]")
	auraRule, _ := currentEquipmentPlacementRuleForPVFType("[aurora avatar]")
	coatRule, _ := currentEquipmentPlacementRuleForPVFType("[coat avatar]")

	if got := currentAvatarCreationSocketTypes(source, "equipment/avatar/skin.equ", skinRule); len(got) != 2 {
		t.Fatalf("skin creation sockets=%v, want two opened sockets", got)
	}
	if got := currentAvatarCreationSocketTypes(source, "equipment/avatar/aura.equ", auraRule); len(got) != 2 {
		t.Fatalf("aura creation sockets=%v, want two opened sockets", got)
	}
	if got := currentAvatarCreationSocketTypes(source, "equipment/avatar/coat.equ", coatRule); len(got) != 0 {
		t.Fatalf("ordinary avatar part pre-opened sockets=%v", got)
	}
	if got := currentAvatarCreationSocketTypes(source, "equipment/avatar/skinless.equ", skinRule); len(got) != 0 {
		t.Fatalf("a skin without a declared socket num must stay closed, got %v", got)
	}
	if got := currentAvatarCreationSocketTypes(source, "equipment/avatar/missing.equ", skinRule); len(got) != 0 {
		t.Fatalf("unreadable PVF must grant the avatar closed, got %v", got)
	}
	if got := currentAvatarCreationSocketTypes(nil, "equipment/avatar/skin.equ", skinRule); len(got) != 0 {
		t.Fatalf("nil source must not pre-open sockets, got %v", got)
	}
}
