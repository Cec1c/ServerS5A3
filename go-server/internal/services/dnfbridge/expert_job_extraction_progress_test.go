package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 三类原生提取逐请求提交：普通经验增长不重建配方窗，升级仍更新配方，失败不发经验。
func TestRealPVFExtractionProgressDoesNotRepublishRecipesBetweenLevels(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要匹配的真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := dnfexpertjob.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const itemID = int64(100050285)
	metadata, err := jobs.Equipment(itemID)
	if err != nil {
		t.Fatal(err)
	}
	metadata.State = currentExpertJobEquipmentState(dnfrepo.ItemStack{ItemID: itemID, Count: 1})
	for _, kind := range []byte{1, 2, 4} {
		config, ok := jobs.Config(kind)
		if !ok {
			t.Fatal("缺少副职业配置")
		}
		ids := make([]int64, 0, len(config.Extractors))
		for id := range config.Extractors {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		var extractorID int64
		for _, id := range ids {
			if plan, e := jobs.PlanExtraction(kind, 100, id, metadata, func(int) int { return 0 }); e == nil && len(plan.Materials) > 0 {
				extractorID = id
				break
			}
		}
		if extractorID == 0 {
			t.Fatal("没有真实可用提取器")
		}
		original := config.Extractors[extractorID]
		for _, scenario := range []string{"连续普通经验", "跨等级", "显式legacy"} {
			t.Run(fmt.Sprintf("副职业%d/%s", kind, scenario), func(t *testing.T) {
				t.Setenv(currentExpertJobExpNotifyEnv, "on")
				if scenario == "显式legacy" {
					t.Setenv(currentExpertJobExpNotifyEnv, "legacy")
				}
				// 固定真实提取器已存在的经验随机区间端点，避免断言取决于随机抽样。
				if original.MinimumExperienceGain > 1 || original.MaximumExperienceGain < 1 {
					t.Fatal("真实提取器不包含1点经验")
				}
				extractor := original
				extractor.MinimumExperienceGain = 1
				extractor.MaximumExperienceGain = 1
				config.Extractors[extractorID] = extractor
				t.Cleanup(func() { config.Extractors[extractorID] = original })
				initial := int64(100)
				if scenario == "跨等级" {
					for _, threshold := range config.ExperienceThresholds {
						if threshold > initial+4 {
							initial = threshold - 1
							break
						}
					}
					if config.Level(initial) == config.Level(initial+1) {
						t.Fatal("缺少真实等级边界")
					}
				} else if config.Level(initial) != config.Level(initial+4) {
					t.Fatal("普通经验样本跨级")
				}
				repos := mustCurrentNPCShopRepositories(t, 1000, nil)
				character, _, _ := repos.Character.Load(ctx, "19")
				character.Name = "extract_progress"
				character.Stats["expert_job_type"] = int64(kind)
				character.Stats["expert_job_exp"] = initial
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				inventory, _, _ := repos.Inventory.Load(ctx, "19")
				inventory.Slots = map[string]dnfrepo.ItemStack{"0:76": {ItemID: extractorID, Count: 1}}
				for slot := 10; slot < 14; slot++ {
					inventory.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: itemID, Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
				service := currentDisjointTestService(repos, items)
				service.expertJobCatalog = jobs
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1", itemListMigrationsDone: true}
				for index := 0; index < 4; index++ {
					conn.write.Reset()
					if err := service.handleCurrentExpertJobExtraction(session, []byte{kind, 76, 0, 0, byte(10 + index), 0}); err != nil {
						t.Fatal(err)
					}
					packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
					if len(packets) < 2 || packets[0].Header.MsgID != 416 || packets[0].Body[0] != 1 {
						t.Fatal("缺少成功应答和槽刷新")
					}
					after, _, _ := repos.Character.Load(ctx, "19")
					want := initial + int64(index) + 1
					if after.Stats["expert_job_exp"] != want {
						t.Fatal("经验未逐次提交")
					}
					levelChanged := config.Level(want-1) != config.Level(want)
					infos, actors := 0, 0
					for _, packet := range packets[1:] {
						if packet.Header.MsgID == 13 {
							t.Fatal("意外整容器刷新")
						}
						if packet.Header.Classification != 0 {
							continue
						}
						if packet.Header.MsgID == 205 {
							infos++
						}
						if packet.Header.MsgID == 2 {
							actors++
							_, offset := expertJobExpNotifySubtype0BodyOffsets(after.Name)
							if len(packet.Body) < offset+4 || packet.Body[0] != 0 || binary.LittleEndian.Uint32(packet.Body[offset:]) != uint32(want) {
								t.Fatal("经验载体不是已提交的新值")
							}
						}
					}
					wantInfo, wantActor := 0, 1
					if levelChanged {
						wantInfo = 1
					}
					if scenario == "显式legacy" {
						wantInfo, wantActor = 1, 0
					}
					if infos != wantInfo || actors != wantActor {
						t.Fatalf("第%d次：205=%d USERINFO=%d，期望%d/%d", index+1, infos, actors, wantInfo, wantActor)
					}
				}
				conn.write.Reset()
				if err := service.handleCurrentExpertJobExtraction(session, []byte{kind, 76, 0, 0, 10, 0}); err != nil {
					t.Fatal(err)
				}
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				after, _, _ := repos.Character.Load(ctx, "19")
				if len(packets) != 1 || packets[0].Body[0] != 0 || after.Stats["expert_job_exp"] != initial+4 {
					t.Fatal("重放消耗来源改变经验或发出刷新")
				}
			})
		}
	}
}
