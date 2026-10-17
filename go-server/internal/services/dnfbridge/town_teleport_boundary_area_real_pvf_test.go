package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档 Script.pvf 端到端复验：用线上同哈希的档（sha256 cc517a6e…，
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1
// ./internal/services/dnfbridge -run RealScriptPVFTeleportPotionBoundaryArea）跑完整
// op237 → op36 → op24 提交链路，钉住操作者报障的那三条现场请求：
//
//	packet_log.txt  town=31 x=675  y=342  → 旧代码 area 0 `Town/CentralPark/Gate_CentralPark.map`（假死）
//	packet_log.txt  town=31 x=727  y=370  → 同上
//	packet_log.txt  town=31 x=1831 y=370  → 同上
//	packet_log.txt  town=30 x=1299 y=431  → 旧代码 area 0 `Town/MetroCenter/Gate_MetroCenter.map`（假死）
//	packet_log.txt  town=9  x=706  y=296  → 旧代码 area 0 `Town/LuftHafen/LuftHafent_Market.map`（本来就对，必须不变）
func TestRealScriptPVFTeleportPotionBoundaryAreaLanding(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to replay the live op237 requests on the real town catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}

	cases := []struct {
		name        string
		townID      byte
		clickX      int64
		clickY      int64
		legacyArea  int64
		legacyMap   string
		wantArea    int64
		wantMap     string
		liveFrozePC bool
	}{
		{
			name: "town31_centralpark_live_675_342", townID: 31, clickX: 675, clickY: 342,
			legacyArea: 0, legacyMap: "Town/CentralPark/Gate_CentralPark.map",
			wantArea: 1, wantMap: "Town/CentralPark/CentralPark.map", liveFrozePC: true,
		},
		{
			name: "town31_centralpark_live_1831_370", townID: 31, clickX: 1831, clickY: 370,
			legacyArea: 0, legacyMap: "Town/CentralPark/Gate_CentralPark.map",
			wantArea: 1, wantMap: "Town/CentralPark/CentralPark.map", liveFrozePC: true,
		},
		{
			name: "town30_metrocenter_live_1299_431", townID: 30, clickX: 1299, clickY: 431,
			legacyArea: 0, legacyMap: "Town/MetroCenter/Gate_MetroCenter.map",
			wantArea: 1, wantMap: "Town/MetroCenter/Metro_Time.map", liveFrozePC: true,
		},
		{
			name: "town9_lufthafen_live_706_296_unchanged", townID: 9, clickX: 706, clickY: 296,
			legacyArea: 0, legacyMap: "Town/LuftHafen/LuftHafent_Market.map",
			wantArea: 0, wantMap: "Town/LuftHafen/LuftHafent_Market.map", liveFrozePC: false,
		},
	}
	for _, item := range cases {
		service, session, repositories := newTeleportPotionTest(t)
		service.townCatalog = table

		// 旧代码的选择：真档上确实会把玩家丢进 Gate_ 边界图（前三条），
		// 或本来就落在主图（第四条）。这条断言是本回归的"红"基线。
		legacy, found := service.townTeleportPotionLandingArea(int64(item.townID))
		if !found || legacy.ID != item.legacyArea || legacy.MapPath != item.legacyMap {
			t.Fatalf("%s: legacy selector moved: (%+v,%t) want area %d %s",
				item.name, legacy, found, item.legacyArea, item.legacyMap)
		}
		if item.liveFrozePC && legacy.ID == 0 && legacy.MapPath == item.wantMap {
			t.Fatalf("%s: live freeze case must have landed off the main map before the fix", item.name)
		}

		// Reconstructed main-map fixture: explicitly name the map under test.
		// Older fixtures left body[8] zero because the parser ignored it.
		// Exact captured bodies are covered by RequestedAreaLanding separately.
		if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, item.townID, byte(item.wantArea), item.clickX, item.clickY)); err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		splitTownTransitionAndPostState(
			t,
			session.conn.(*bufferConn).write.Bytes(),
			session,
			29,
			item.townID,
			byte(item.wantArea),
			uint16(item.clickX),
			uint16(item.clickY),
			0,
			3,
			townMoveSkillProjectionBody(t, repositories, "29"),
			false,
		)
		stored, found, err := repositories.Character.Load(context.Background(), "29")
		if err != nil || !found {
			t.Fatalf("%s: load stored character found=%t err=%v", item.name, found, err)
		}
		if stored.Stats["town_id"] != int64(item.townID) || stored.Stats["area_id"] != item.wantArea ||
			stored.Stats["pos_x"] != item.clickX || stored.Stats["pos_y"] != item.clickY {
			t.Fatalf("%s: committed location=%+v want town=%d area=%d pos=(%d,%d) map=%s",
				item.name, stored.Stats, item.townID, item.wantArea, item.clickX, item.clickY, item.wantMap)
		}
		area, ok := service.townArea(int64(item.townID), item.wantArea)
		if !ok || area.MapPath != item.wantMap {
			t.Fatalf("%s: committed area map=%q want %q", item.name, area.MapPath, item.wantMap)
		}
		inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
		if err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if inventory.Slots["0:65"].Count != 1 {
			t.Fatalf("%s: potion after consume=%+v", item.name, inventory.Slots["0:65"])
		}
	}
}
