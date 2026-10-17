package inventory

import (
	"context"
	"reflect"
	"testing"

	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// op272 成功路径回的是 class0/0x000D list 0 整表刷新（op13 = 替换语义）。领域侧
// 只负责把"哪张容器表必须由桥侧权威投影重建"标出来；钱包行、账号共享的
// 0:354..0:365、装备品级种子/孔位/符文/期限都不在角色快照里，复刻一遍必然漂移
// （v1 就是那条 501 行的域内投影，已被本方案取代）。见
// dnfbridge/authoritative_list_refresh.go 与 native-next914/enchant-gold-fix/v2。
func TestEnchantMarksList0ForAuthoritativeRebuild(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveEnchantFixture(t, ctx, repos, 2, 0, 0)

	got := handleEnchantRequest(t, repos, coatEnchantResolver(9001, nil), enchantRequestBody(listTypeMain, 12, listTypeMain, 30))
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 2 {
		t.Fatalf("op272 result = %+v", got)
	}
	if !reflect.DeepEqual(got.AuthoritativeListRefreshes, []byte{listTypeMain}) {
		t.Fatalf("authoritative refreshes = %v, want [%d]", got.AuthoritativeListRefreshes, listTypeMain)
	}
	refresh := got.UpperResponses[0]
	if refresh.MsgID != msgItemListRefresh || refresh.Classification != 0 {
		t.Fatalf("first response = class%d msg0x%04X, want class0 0x000D then the ACK", refresh.Classification, refresh.MsgID)
	}
	if refresh.AllowCodec != true {
		t.Fatalf("refresh AllowCodec = %t, want true (bridge keeps the response envelope)", refresh.AllowCodec)
	}

	// 拒绝路径不许要求重建：会被 ACK 的错误码取代，客户端不该收到整表刷新。
	rejected := handleEnchantRequest(t, repos, coatEnchantResolver(0, nil), enchantRequestBody(listTypeMain, 12, listTypeMain, 30))
	if len(rejected.AuthoritativeListRefreshes) != 0 {
		t.Fatalf("rejected enchant must not ask for a rebuild: %v", rejected.AuthoritativeListRefreshes)
	}
	parseFailure := handleEnchantRequest(t, repos, coatEnchantResolver(9001, nil), []byte{1, 2})
	if len(parseFailure.AuthoritativeListRefreshes) != 0 {
		t.Fatalf("parse failure must not ask for a rebuild: %v", parseFailure.AuthoritativeListRefreshes)
	}
}

// 宠物分支只发 op14 行（目标行 + 宝珠行），没有整表刷新，因此也不该点名任何列表。
func TestEnchantPetCreatureDoesNotAskForList0Rebuild(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	savePetEnchantFixture(t, ctx, repos, 1)

	got := handleEnchantRequest(t, repos, creatureEnchantResolver(10008663, 400990168), enchantRequestBody(listTypeMain, 12, listTypePet, 24))
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 3 {
		t.Fatalf("pet op272 result = %+v", got)
	}
	if len(got.AuthoritativeListRefreshes) != 0 {
		t.Fatalf("pet enchant answers with op14 rows only, not a list-0 rebuild: %v", got.AuthoritativeListRefreshes)
	}
}
