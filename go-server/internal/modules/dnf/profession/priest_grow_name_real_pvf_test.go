package profession

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFATPriestGrowTypeNames 用真档 Script.pvf 钉死 2026-09-25
// "选角界面显示炽天使、进场后显示圣骑士" 这条显示不一致里的名称来源：
//
//	job 14 = character/character.lst 第 14 项 = `Priest/ATPriest.chr`
//	[growtype name] = 圣职者 / 圣骑士 / 异端审判者 / 巫女 / 诱魔者   （低四位 = 下标）
//	[growtype 2]（= 分支 1 = 圣骑士）的 [awakening name] = 福音传道者 / 炽天使
//	                                                            （高四位 = 觉醒次数）
//
// ⇒ 持久化 grow_type 0x00→圣职者、0x01→圣骑士、0x11→福音传道者、0x21→炽天使。
// 「圣骑士」是转职（一转分支）名，不是一觉名；一觉名是「福音传道者」。
func TestRealScriptPVFATPriestGrowTypeNames(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the ATPriest grow/awakening name table")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText("character/Priest/ATPriest.chr")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parseJobProfile(text)
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.displayNames[0]; got != "圣职者" {
		t.Fatalf("ATPriest [growtype name][0] = %q, want 圣职者", got)
	}
	if got := profile.displayNames[1]; got != "圣骑士" {
		t.Fatalf("ATPriest [growtype name][1] = %q, want 圣骑士", got)
	}
	if got := profile.awakeningNames[1][1]; got != "福音传道者" {
		t.Fatalf("ATPriest [growtype 2][awakening name][1] = %q, want 福音传道者", got)
	}
	if got := profile.awakeningNames[1][2]; got != "炽天使" {
		t.Fatalf("ATPriest [growtype 2][awakening name][2] = %q, want 炽天使", got)
	}

	profiles := &Profiles{jobs: map[byte]jobProfile{14: profile}}
	for _, tc := range []struct {
		growType byte
		want     string
	}{
		{growType: 0x00, want: "圣职者"},
		{growType: 0x01, want: "圣骑士"},
		{growType: 0x11, want: "福音传道者"},
		{growType: 0x21, want: "炽天使"},
	} {
		name, ok := profiles.DisplayName(14, tc.growType)
		if !ok || name != tc.want {
			t.Fatalf("DisplayName(job=14, grow_type=%#x) = %q found=%t, want %q", tc.growType, name, ok, tc.want)
		}
		// op2 mode0 actor entry 与选角列表都必须发持久化值本身：低四位=分支、
		// 高四位=觉醒次数，两个半字节都不能丢（Encode∘Decode 必须是恒等）。
		first, awakening := Decode(tc.growType)
		if first != tc.growType&0x0f || awakening != (tc.growType>>4)&0x0f {
			t.Fatalf("Decode(%#x) = (%d,%d)", tc.growType, first, awakening)
		}
		packed, encodeErr := Encode(first, awakening)
		if encodeErr != nil || packed != tc.growType {
			t.Fatalf("Encode(Decode(%#x)) = %#x err=%v, want the persisted value itself", tc.growType, packed, encodeErr)
		}
	}
}
