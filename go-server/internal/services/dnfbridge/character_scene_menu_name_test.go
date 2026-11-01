package dnfbridge

import (
	"bytes"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentSceneMenuAdventureNameKeepsNativeRecordBoundaryAndGuild(t *testing.T) {
	character := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Name: "Actor19", Job: "0", Level: 90}
	guild := currentSceneGuildProjection{id: 7, name: "公会名称"}
	build := func(name string) []byte {
		return buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(19, character, true, character.Name, currentEquippedCreatureSnapshot{}, name, 10, 0, guild)
	}
	baseline := build("")
	for _, tc := range []struct{ name, want string }{
		{"冒险团名称", "冒险团名称"},
		{strings.Repeat("字", 8), strings.Repeat("字", 8)},
		{strings.Repeat("A", 16), strings.Repeat("A", 16)},
		{" 名称 ", "名称"},
		{"", ""},
		{strings.Repeat("A", 17), ""},
		{strings.Repeat("字", 9), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := build(tc.name)
			// 固定公会使可变长尾部保持一致，整包只允许17字节名称缓冲变化。
			want := append([]byte(nil), baseline...)
			copy(want[5+0x32:5+0x43], rosterNameBytes(tc.want))
			if !bytes.Equal(body, want) {
				t.Fatalf("人物菜单名称字段或相邻入场资料不符：got=%x want=%x", body, want)
			}
			if body[5+0x42] != 0 {
				t.Fatal("人物菜单名称缺少结尾NUL")
			}
			if _, name := currentSceneObjectOrganizationNameForTest(t, body); !bytes.Equal(name, rosterNameBytes(guild.name)) {
				t.Fatalf("人物菜单名称覆盖了公会名：%x", name)
			}
		})
	}
}
