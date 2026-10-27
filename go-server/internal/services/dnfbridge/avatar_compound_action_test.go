package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func avatarCompoundActionFixture() bridgePVFSource {
	source := currentAvatarCompoundTestPVF()
	source[currentAvatarCompoundServerParameterPath] = "[upper bind cube rate bonus]\n137\n[master bind cube rate bonus]\n615\n"
	source[currentAvatarCompoundTestConfigPath] += "[material_upper]\n1 899 1\n[/material_upper]\n[material_master]\n1 73 1\n[/material_master]\n"
	source["stackable/stackable.lst"] += "72 `activity_cube.stk`\n73 `master_cube.stk`\n899 `gold_cube.stk`\n"
	source["stackable/activity_cube.stk"] = "[name]\n`活动道具`\n[stackable type]\n`[etc]` 0\n[action type]\n`[bind cube]` 5\n[/action type]\n"
	source["stackable/master_cube.stk"] = "[stackable type]\n`[etc]` 0\n"
	source["stackable/gold_cube.stk"] = "[stackable type]\n`[etc]` 0\n"
	return source
}

func TestCurrentAvatarCompoundActivityMasterActionCommitsActualCube(t *testing.T) {
	for _, hit := range []bool{false, true} {
		t.Run(fmt.Sprintf("额外稀有=%t", hit), func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int {
				if hit {
					return 0
				}
				return limit - 1
			})
			slots := currentAvatarCompoundTestSlots()
			slots["0:9"] = dnfrepo.ItemStack{ItemID: 72, Count: 2}
			repos, catalog := mustAvatarCompoundLiveAssets(t, avatarCompoundActionFixture(), "0", slots)
			svc := currentAvatarCompoundTestService(repos, catalog)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
			body[21] = 5
			request, _ := parseCurrentAvatarCompoundRequest(body)
			config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if config.acceptsMaterial(72) || config.Variants[0].MasterMaterialID != 73 {
				t.Fatal("夹具没有复现活动编号未列入master的根因")
			}
			plan, err := svc.prepareCurrentAvatarCompound(context.Background(), session, catalog, config, request)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.PVFActionCube || plan.CubeItemID != 72 || plan.BonusRate != 1565 || plan.MaterialRateBonus != 615 || plan.MaterialRole != "material_master" || plan.BonusHits != hit {
				t.Fatalf("动作资格或PVF钻石加成错误: %+v", plan)
			}
			if err := svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketCompoundAvatar), body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			rows := []currentAvatarCompoundResultRow{{Slot: 5, ItemID: 41003, AbilityNo: 4}}
			if hit {
				rows = append(rows, currentAvatarCompoundResultRow{Slot: 6, ItemID: 41101, AbilityNo: 4})
			}
			if !bytes.Equal(ack.Body, buildCurrentAvatarCompoundSuccessBody(request, rows)) {
				t.Fatalf("动作类钻石成功应答错误: %x", ack.Body)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if got := after.Slots["0:9"]; got.ItemID != 72 || got.Count != 1 {
				t.Fatalf("没有扣除实际活动道具: %+v", got)
			}
			if got := after.Slots["1:5"]; got.ItemID != 41003 || got.Count != 1 || got.Extra["ext_data0"] != "4" {
				t.Fatalf("结果未持久化: %+v", got)
			}
			if _, exists := after.Slots["1:6"]; exists != hit {
				t.Fatalf("第二槽错误: %+v", after.Slots)
			}
			if config.acceptsMaterial(72) {
				t.Fatal("资格识别污染了原始配置")
			}
		})
	}
}

func TestCurrentAvatarCompoundActivityMasterRejectsWithoutConsumption(t *testing.T) {
	for _, test := range []struct {
		name   string
		alter  func(bridgePVFSource)
		cube   uint32
		target uint32
	}{
		{"同名没有动作", func(s bridgePVFSource) {
			s["stackable/activity_cube.stk"] = "[name]\n`钻石装扮合成器`\n[stackable type]\n`[etc]` 0\n"
		}, 72, 41003},
		{"动作类型不同", func(s bridgePVFSource) {
			s["stackable/activity_cube.stk"] = "[stackable type]\n`[etc]` 0\n[action type]\n`[bind cube]` 4\n[/action type]\n"
		}, 72, 41003},
		{"动作参数不是整数", func(s bridgePVFSource) {
			s["stackable/activity_cube.stk"] = "[stackable type]\n`[etc]` 0\n[action type]\n`[bind cube]` `5`\n[/action type]\n"
		}, 72, 41003},
		{"不是etc物品", func(s bridgePVFSource) {
			s["stackable/activity_cube.stk"] = "[stackable type]\n`[material]` 0\n[action type]\n`[bind cube]` 5\n[/action type]\n"
		}, 72, 41003},
		{"职业表没有master", func(s bridgePVFSource) {
			s[currentAvatarCompoundTestConfigPath] = currentAvatarCompoundTestPVF()[currentAvatarCompoundTestConfigPath]
		}, 72, 41003},
		{"动作类不能直点额外稀有", nil, 72, 41101},
		{"不能只凭请求类型5放行普通材料", nil, 3037, 41003},
	} {
		t.Run(test.name, func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
			source := avatarCompoundActionFixture()
			if test.alter != nil {
				test.alter(source)
			}
			slots := currentAvatarCompoundTestSlots()
			slots["0:9"] = dnfrepo.ItemStack{ItemID: int64(test.cube), Count: 2, Extra: map[string]string{"核对": "应完整保留"}}
			repos, catalog := mustAvatarCompoundLiveAssets(t, source, "0", slots)
			svc := currentAvatarCompoundTestService(repos, catalog)
			conn := &bufferConn{}
			before, _, _ := repos.Inventory.Load(context.Background(), "19")
			body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, test.target, 4)
			body[21] = 5
			if err := svc.handleGameCommand(&gameSession{conn: conn, selectedCharacterID: 19}, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketCompoundAvatar), body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) || len(rest) != 0 {
				t.Fatalf("拒绝回包错误: %x", ack.Body)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝请求修改了库存")
			}
		})
	}
}

func TestCurrentAvatarCompoundMasterActionCannotBorrowCNPool(t *testing.T) {
	variant := currentAvatarCompoundVariant{Path: "etc/chn_compound/compoundavatar_swordman.etc", MasterMaterialID: 73,
		Parts: map[string]currentAvatarCompoundPartPool{"hat": {Upper: []currentAvatarCompoundPoolEntry{{ItemID: 41003, Weight: 1}}}}}
	if variant.masterActionAllowsTarget("hat", 41003) {
		t.Fatal("动作类钻石不能借用CN追忆池")
	}
}

func TestRealPVFCurrentAvatarCompoundCapturedActivityDiamond(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	for _, test := range []struct {
		name, job  string
		body       []byte
		cube       uint32
		wantAction bool
		wantRate   int64
	}{
		{"普通真实PVF语义", "0", avatarCompoundPublicReplayBody(101560830, 401560005, 39544, 1, 0), 21, false, 1275},
		{"黄金真实PVF语义", "0", avatarCompoundPublicReplayBody(39544, 401560003, 39544, 0, 1), 899, false, 1475},
		{"旧活动钻石准入回归", "14", avatarCompoundPublicReplayBody(415560047, 415560067, 415560013, 0, 5), 10008512, true, 1675},
		{"表内钻石与活动钻石同档", "14", avatarCompoundPublicReplayBody(415560047, 415560067, 415560013, 0, 5), 490702265, false, 1675},
	} {
		t.Run(test.name, func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
			body := test.body
			request, err := parseCurrentAvatarCompoundRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			slots := map[string]dnfrepo.ItemStack{
				fmt.Sprintf("0:%d", request.ConsumeSlot): {ItemID: int64(test.cube), Count: 2},
				fmt.Sprintf("1:%d", request.Slot1):       {ItemID: int64(request.ItemID1), Count: 1},
				fmt.Sprintf("1:%d", request.Slot2):       {ItemID: int64(request.ItemID2), Count: 1},
			}
			repos, _ := mustAvatarCompoundLiveAssets(t, real.source, test.job, slots)
			svc := currentAvatarCompoundTestService(repos, real.catalog)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			job := byte(0)
			if test.job == "14" {
				job = 14
			}
			config, err := loadCurrentAvatarCompoundConfig(real.source, job)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantAction && config.acceptsMaterial(test.cube) {
				t.Fatal("现场根因条件与PVF不符")
			}
			plan, err := svc.prepareCurrentAvatarCompound(context.Background(), session, real.catalog, config, request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.PVFActionCube != test.wantAction || plan.CubeItemID != test.cube {
				t.Fatalf("识别路径错误: %+v", plan)
			}
			if test.wantRate != 0 && plan.BonusRate != test.wantRate {
				t.Fatalf("真实PVF合成概率错误: %d", plan.BonusRate)
			}
			t.Logf("合成器=%d 来源品质=%v 最终概率=%d PVF加成=%d 角色=%s 动作识别=%t", test.cube, plan.MaterialGrades, plan.BonusRate, plan.MaterialRateBonus, plan.MaterialRole, plan.PVFActionCube)
			if err := svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketCompoundAvatar), body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 99 || len(ack.Body) != 131 || ack.Body[0] != 1 || ack.Body[1] != 3 || binary.LittleEndian.Uint16(ack.Body[23:25]) != uint16(request.Slot1) || binary.LittleEndian.Uint32(ack.Body[25:29]) != request.RequestedItemID || binary.LittleEndian.Uint16(ack.Body[77:79]) != 0xffff {
				t.Fatalf("现场请求没有得到原生成功形状: %x", ack.Body)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "19")
			if got := after.Slots[fmt.Sprintf("0:%d", request.ConsumeSlot)]; got.ItemID != int64(test.cube) || got.Count != 1 {
				t.Fatalf("扣错合成器: %+v", got)
			}
			if got := after.Slots[fmt.Sprintf("1:%d", request.Slot1)]; got.ItemID != int64(request.RequestedItemID) || got.Count != 1 || got.Extra["ext_data0"] != fmt.Sprint(request.AbilityNo) {
				t.Fatalf("结果或属性错误: %+v", got)
			}
			if _, exists := after.Slots[fmt.Sprintf("1:%d", request.Slot2)]; exists {
				t.Fatal("第二材料未消耗")
			}
		})
	}
}

// 使用固定合成槽，避免公开私人原始报文中的库存布局。
func avatarCompoundPublicReplayBody(item1, item2, target uint32, ability uint16, kind byte) []byte {
	body := currentAvatarCompoundTestBody(9, 5, item1, 6, item2, target, ability)
	body[21] = kind
	return body
}
