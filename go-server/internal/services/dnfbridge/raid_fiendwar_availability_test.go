package dnfbridge

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestFiendWarChannelEventPreservesOtherActivityRows(t *testing.T) {
	for _, spendTime := range []bool{false, true} {
		for _, day := range []uint16{2418, 2419, 2420, 2421, 2422, 2423, 2424} {
			old, err := buildCurrentActivityEventInfoBodyWithEpicRoad(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, false, true, true, spendTime, day, true, true, true, true, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{false, true} {
				body, err := buildCurrentActivityEventInfoBodyWithFiendWar(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, false, true, true, spendTime, day, true, true, true, true, true, enabled)
				if err != nil {
					t.Fatal(err)
				}
				if !enabled && !bytes.Equal(body, old) {
					t.Fatal("关闭超时空改变了既有目录")
				}
				shape := parseCurrentSpendTimeEventInfoBody(t, body)
				if shape.hasEvent(2449) != enabled {
					t.Fatal("超时空开放标记不符")
				}
				oldShape := parseCurrentSpendTimeEventInfoBody(t, old)
				var base, active []byte
				for _, row := range shape.baseRows {
					if row.eventID == 2449 {
						if !bytes.Equal(row.raw, mustHexBytes(t, "910901000000000000000000000000")) {
							t.Fatalf("2449基础行与原生通用活动格式不符: %x", row.raw)
						}
						continue
					}
					base = append(base, row.raw...)
				}
				for _, row := range shape.activeRows {
					if row.eventID == 2449 {
						want := append([]byte{0x91, 0x09}, make([]byte, 48)...)
						if !bytes.Equal(row.raw, want) {
							t.Fatalf("2449活动行不符: %x", row.raw)
						}
						continue
					}
					active = append(active, row.raw...)
				}
				var oldBase, oldActive []byte
				for _, row := range oldShape.baseRows {
					oldBase = append(oldBase, row.raw...)
				}
				for _, row := range oldShape.activeRows {
					oldActive = append(oldActive, row.raw...)
				}
				if !bytes.Equal(base, oldBase) || !bytes.Equal(active, oldActive) {
					t.Fatal("追加2449改变了已有活动内容或顺序")
				}
				writeAntonNativeCardFixture(t, fmt.Sprintf("fiendwar-channel-%t-%t-%d.bin", enabled, spendTime, day), body)
			}
		}
	}
}

func TestRealPVFFiendWarChannelAvailabilityRequiresCurrentAssets(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	if !f.service.currentFiendWarChannelAvailable(peers[0]) {
		t.Fatal("真实PVF超时空入口没有开放")
	}
	f.service.options.currentClientVersion = "legacy"
	if f.service.currentFiendWarChannelAvailable(peers[0]) {
		t.Fatal("旧客户端误开放超时空活动")
	}
	f.service.options.currentClientVersion = "official-24.9"
	f.service.fiendWarConfig = nil
	f.service.initialEquipmentArchive = nil
	f.service.initialEquipmentLoadErr = errors.New("测试：缺少PVF资源")
	if f.service.currentFiendWarChannelAvailable(peers[0]) {
		t.Fatal("缺失PVF仍开放超时空活动")
	}
	var absent *Service
	if absent.currentFiendWarChannelAvailable(nil) {
		t.Fatal("缺失服务仍开放入口")
	}
}
