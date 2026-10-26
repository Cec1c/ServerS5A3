package channelcatalog

import "testing"

// 帝国可以手动进入，但不能占据普通自动登录落点。
func TestEmpireChannelCannotReplaceOrdinaryResident(t *testing.T) {
	for _, group := range []string{"tagtournament", "tag_tournament", "custom_empire"} {
		empire := Channel{ServerID: 1, ID: 187, Type: 39, Group: group}
		ordinary := Channel{ServerID: 1, ID: 11, Type: 11, Group: "granfloris"}
		catalog := &Catalog{channels: []Channel{empire, ordinary}, byID: map[int]Channel{187: empire, 11: ordinary}}
		if IsResidentChannel(empire) {
			t.Fatalf("帝国被当作普通落点：%s", group)
		}
		if got, ok := catalog.ResidentFor(empire); !ok || got.ID != ordinary.ID {
			t.Fatalf("帝国返回普通频道失败：%+v %t", got, ok)
		}
		if got, ok := catalog.ResidentFor(ordinary); !ok || got.ID != ordinary.ID {
			t.Fatalf("普通频道落点被改变：%+v %t", got, ok)
		}
		catalog.channels = []Channel{empire}
		if _, ok := catalog.ResidentFor(empire); ok {
			t.Fatal("没有普通频道时使用了帝国落点")
		}
	}
}
