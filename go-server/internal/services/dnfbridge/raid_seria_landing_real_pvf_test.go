package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 2026-10-02 (V22)：**真实 PVF 上的团本入场所在地**。
//
// 为什么单独钉这一条：四条主线里的"组队频道落地"只有安徒恩被真实玩家走过
// （线上 `game-raid-seria-location-used` 11 次、全是 town 19）；卢克（隐藏团本）
// 的 type-32 频道虽然存在（`luke_raid` 28/241/242/243），但日志里所有 type-32 连接
// 都是境外 IP 每 6 小时 `:40:2x` 的端口扫描（连上即 game-close、无选角），
// 所以**卢克落点从未被真实会话验证过**。
//
// 本用例把两个分支都用真实 Script.pvf 跑一遍，钉住：
//   - RaidChannelType(23)      → town 19 `BattleShip_Raid.twn` area 0 `Town/BattleShip/Gate_BattleShip.map` @474/234；
//   - HiddenRaidType(32)       → town 32 `Jelva_raid.twn`    area 0 `Town/Jelva_raid/Gate_Jelva.map` @474/234；
//   - 该房间是**每角色私有实例**（townAreaInstanceForMapPath == charID）；
//   - 两个分支都会写 `game-raid-seria-location-used`（线上自证点）。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test ./internal/services/dnfbridge -run RealScriptPVFRaidSeriaLanding -v
func TestRealScriptPVFRaidSeriaLandingCoversAntonAndLuke(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the raid Seria landings")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}

	for _, row := range []struct {
		name     string
		channel  byte
		townID   byte
		areaID   byte
		mapPath  string
		position [2]int64
	}{
		{name: "anton", channel: dnfenum.RaidChannelType, townID: 19, areaID: 0,
			mapPath: "Town/BattleShip/Gate_BattleShip.map", position: [2]int64{474, 234}},
		{name: "luke", channel: dnfenum.HiddenRaidType, townID: 32, areaID: 0,
			mapPath: "Town/Jelva_raid/Gate_Jelva.map", position: [2]int64{474, 234}},
	} {
		t.Run(row.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "packet.log")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{townCatalog: table, packetLog: logger}
			session := &gameSession{connID: "raid-landing-" + row.name,
				channel: channelcatalog.Channel{ID: int(row.channel), Type: row.channel}}
			character := dnfrepo.CharacterRecord{CharacterID: "29", Stats: map[string]int64{
				"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234,
			}}
			projected, town, area, sceneRow, path, err := service.currentRaidEntranceTownTransition(session, 29, character)
			if err != nil {
				t.Fatalf("raid landing failed: %v", err)
			}
			if town != row.townID || area != row.areaID || path != row.mapPath {
				t.Fatalf("landing town=%d area=%d path=%s, want %d/%d %s", town, area, path, row.townID, row.areaID, row.mapPath)
			}
			if projected.Stats["town_id"] != int64(row.townID) || projected.Stats["area_id"] != int64(row.areaID) ||
				projected.Stats["pos_x"] != row.position[0] || projected.Stats["pos_y"] != row.position[1] {
				t.Fatalf("projected stats=%v", projected.Stats)
			}
			// 原记录不得被就地改写（落点是投影，不是持久化）。
			if character.Stats["town_id"] != 38 {
				t.Fatalf("input character mutated: %v", character.Stats)
			}
			if sceneRow.Value1 != uint16(row.position[0]) || sceneRow.Value2 != uint16(row.position[1]) {
				t.Fatalf("scene row=%+v", sceneRow)
			}
			// 团本赛丽亚房间是每角色私有实例：否则同频道的其他人会站进别人的房间。
			if got := townAreaInstanceForMapPath(path, 29); got != 29 {
				t.Fatalf("raid Seria room must be a private instance: instance=%d", got)
			}
			// 真实 PVF 里这两条路径必须被"赛丽亚房间"判据接受（它们不含 seria 字面量）。
			if !townMapPathLooksLikeSeria(path) {
				t.Fatalf("real PVF raid landing map is not accepted as a Seria room: %s", path)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.Contains(text, "kind=game-raid-seria-location-used") {
				t.Fatalf("landing event missing:\n%s", text)
			}
			if want := "town_id=" + itoaByte(row.townID); !strings.Contains(text, want) {
				t.Fatalf("landing event lacks %s:\n%s", want, text)
			}
			if want := "map_path=" + row.mapPath; !strings.Contains(text, want) {
				t.Fatalf("landing event lacks %s:\n%s", want, text)
			}
		})
	}
}

func itoaByte(value byte) string {
	if value == 0 {
		return "0"
	}
	var digits [3]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = '0' + value%10
		value /= 10
	}
	return string(digits[index:])
}
