package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 通过真实op99入口检查三档品质与四种合成器的概率边界、扣料及双产出。
// 加成特意采用137/615，并把黄金编号改为74，避免固定概率和道具编号通过测试。
func TestCurrentAvatarCompoundPVFRateBoundaryCommits(t *testing.T) {
	for _, cube := range []struct {
		name  string
		id    uint32
		bonus int64
		role  string
	}{
		{"普通", 21, 0, "material"},
		{"黄金", 74, 137, "material_upper"},
		{"表内钻石", 73, 615, "material_master"},
		{"活动钻石", 72, 615, "material_master"},
	} {
		for _, grades := range []struct{ a, b, base int64 }{{1, 1, 950}, {1, 2, 1112}, {2, 2, 1275}} {
			for _, hit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/品质%d+%d/命中%t", cube.name, grades.a, grades.b, hit), func(t *testing.T) {
					want := grades.base + cube.bonus
					withCurrentAvatarCompoundRoll(t, func(limit int) int {
						if limit != int(currentAvatarCompoundRateScale) {
							return 0
						}
						if hit {
							return int(want - 1)
						}
						return int(want)
					})
					source := avatarCompoundActionFixture()
					source[currentAvatarCompoundTestConfigPath] = strings.Replace(source[currentAvatarCompoundTestConfigPath], "1 899 1", "1 74 1", 1)
					source["stackable/stackable.lst"] += "74 `alternate_gold.stk`\n"
					source["stackable/alternate_gold.stk"] = "[stackable type]\n`[etc]` 0\n"
					source["equipment/avatar_hat_a.equ"] = fmt.Sprintf("[equipment type] `[hat avatar]`\n[grade] %d\n[rarity] 2\n", grades.a)
					source["equipment/avatar_hat_b.equ"] = fmt.Sprintf("[equipment type] `[hat avatar]`\n[grade] %d\n[rarity] 2\n", grades.b)
					slots := currentAvatarCompoundTestSlots()
					slots["0:9"] = dnfrepo.ItemStack{ItemID: int64(cube.id), Count: 2}
					repos, catalog := mustAvatarCompoundLiveAssets(t, source, "0", slots)
					svc := currentAvatarCompoundTestService(repos, catalog)
					conn := &bufferConn{}
					session := &gameSession{conn: conn, selectedCharacterID: 19}
					body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
					request, _ := parseCurrentAvatarCompoundRequest(body)
					config, err := loadCurrentAvatarCompoundConfig(source, 0)
					if err != nil {
						t.Fatal(err)
					}
					plan, err := svc.prepareCurrentAvatarCompound(context.Background(), session, catalog, config, request)
					if err != nil {
						t.Fatal(err)
					}
					if plan.BonusRate != want || plan.MaterialRateBonus != cube.bonus || plan.MaterialRole != cube.role || plan.BonusHits != hit {
						t.Fatalf("PVF加成或概率边界错误: %+v", plan)
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
						t.Fatalf("概率边界回包错误: %x", ack.Body)
					}
					after, _, _ := repos.Inventory.Load(context.Background(), "19")
					if got := after.Slots["0:9"]; got.ItemID != int64(cube.id) || got.Count != 1 {
						t.Fatalf("实际合成器扣除错误: %+v", got)
					}
					if got := after.Slots["1:5"]; got.ItemID != 41003 || got.Count != 1 {
						t.Fatalf("保底结果错误: %+v", got)
					}
					if got, exists := after.Slots["1:6"]; exists != hit || (hit && got.ItemID != 41101) {
						t.Fatalf("额外稀有产出错误: %+v", got)
					}
				})
			}
		}
	}
}

func TestCurrentAvatarCompoundInvalidPVFBonusDoesNotConsume(t *testing.T) {
	for _, cube := range []struct {
		id    uint32
		field string
	}{{899, "upper bind cube rate bonus"}, {73, "master bind cube rate bonus"}, {72, "master bind cube rate bonus"}} {
		for name, text := range map[string]string{
			"文件缺失": "", "字段缺失": "[lower bind cube rate bonus]\n4000\n",
			"负数":  fmt.Sprintf("[%s]\n-1\n", cube.field),
			"非整数": fmt.Sprintf("[%s]\n`400`\n", cube.field),
			"重复":  fmt.Sprintf("[%s]\n1\n[%s]\n2\n", cube.field, cube.field),
		} {
			t.Run(fmt.Sprintf("合成器%d/%s", cube.id, name), func(t *testing.T) {
				source := avatarCompoundActionFixture()
				if text == "" {
					delete(source, currentAvatarCompoundServerParameterPath)
				} else {
					source[currentAvatarCompoundServerParameterPath] = text
				}
				slots := currentAvatarCompoundTestSlots()
				slots["0:9"] = dnfrepo.ItemStack{ItemID: int64(cube.id), Count: 2}
				repos, catalog := mustAvatarCompoundLiveAssets(t, source, "0", slots)
				svc := currentAvatarCompoundTestService(repos, catalog)
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 19}
				body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
				request, _ := parseCurrentAvatarCompoundRequest(body)
				config, err := loadCurrentAvatarCompoundConfig(source, 0)
				if err != nil {
					t.Fatal(err)
				}
				withCurrentAvatarCompoundRoll(t, func(int) int { t.Fatal("配置无效时不应进入抽奖"); return 0 })
				_, err = svc.prepareCurrentAvatarCompound(context.Background(), session, catalog, config, request)
				if currentAvatarCompoundRejectionBranch(err) != currentAvatarCompoundBranchConfigUnavailable {
					t.Fatalf("配置错误未正确分类: %v", err)
				}
				before, _, _ := repos.Inventory.Load(context.Background(), "19")
				if err := svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketCompoundAvatar), body); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if !bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) || len(rest) != 0 {
					t.Fatalf("失败回包错误: %x", ack.Body)
				}
				after, _, _ := repos.Inventory.Load(context.Background(), "19")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("配置失败消耗了资产")
				}
			})
		}
	}
}
