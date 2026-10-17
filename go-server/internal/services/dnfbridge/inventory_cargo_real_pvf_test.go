package dnfbridge

import (
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFCargoInsertMatchesObservedMaterialDefinitions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置DNFBRIDGE_REAL_PVF_SMOKE后核对实际材料的两仓存入限制")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{pvfItemCatalog: catalog}
	for _, id := range []uint32{3175, 3161} {
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("实际材料: item=%d path=%s attach=%s forbidden=%v", id, definition.PVFPath, definition.AttachType, definition.ImpossibleContents)
		// 这两张现场使用券在当前PVF中明确为[trade]；86的账号金库
		// 白名单仅free/account/sealing，不能因其在材料页就假定账号金库允许。
		if normalizeMagicBoxPVFType(definition.AttachType) != "trade" {
			t.Fatalf("实际PVF定义已变化，需要重新核对使用券存入契约: item=%d %+v", id, definition)
		}
		for _, list := range []byte{2, 12} {
			err := service.currentInventoryCargoInsert(dnfrepo.ItemStack{ItemID: int64(id), Count: 1}, list)
			if (err != nil) != (list == 12) {
				t.Fatalf("使用券应允许个人存入，按86拒绝账号存入: item=%d list=%d %v", id, list, err)
			}
		}
	}
}
