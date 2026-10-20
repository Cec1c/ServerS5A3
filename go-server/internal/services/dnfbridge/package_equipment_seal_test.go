package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/packageitem"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 只读发现真实PVF中的封装装备奖励，供后续入口验收选取实际盒子/罐子/自选礼包。
// 解析被门禁拒绝的表单独记入报告，不把它们计为已验证的奖励来源。
func TestRealPVFDiscoverSealedEquipmentPackageSources(t *testing.T) {
	path, reportPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"), os.Getenv("DNFBRIDGE_PACKAGE_SEAL_AUDIT_REPORT")
	if path == "" || reportPath == "" {
		t.Skip("设置真实PVF及E盘报告路径后核对封装礼包来源")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		SourceID   uint32 `json:"source_id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Path       string `json:"path"`
		RewardID   uint32 `json:"reward_id"`
		RewardType string `json:"reward_type"`
		RewardPath string `json:"reward_path"`
		Category   uint16 `json:"category,omitempty"`
	}
	report := struct {
		Counts   map[string]int   `json:"counts"`
		Samples  map[string][]row `json:"samples"`
		Rejected []string         `json:"rejected"`
	}{Counts: make(map[string]int), Samples: make(map[string][]row)}
	ids := append([]uint32(nil), catalog.stackableIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		source, err := catalog.ResolveItem(id)
		if err != nil {
			report.Counts["源物品定义不可解析"]++
			continue
		}
		typ := normalizeMagicBoxPVFType(source.StackableType)
		rewards := make(map[uint32]uint16)
		switch typ {
		case "booster selection":
			definition, resolveErr := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestSelection)
			err = resolveErr
			for category, entries := range definition.SelectionCategory {
				for _, entry := range entries {
					rewards[entry.ItemID] = category
				}
			}
		case "booster", "cera booster", "booster random", "random upgradable legacy", "upgradable legacy":
			definition, resolveErr := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
			err = resolveErr
			for _, group := range definition.Random.Groups {
				for _, entry := range group.Entries {
					if entry.Weight > 0 {
						rewards[uint32(entry.ItemID)] = 0
					}
				}
			}
		case "usable cera package":
			definition, resolveErr := resolveCurrentCeraPackageDefinition(catalog, id)
			err = resolveErr
			for _, reward := range definition.Rewards {
				rewards[reward.Definition.ItemID] = 0
			}
		default:
			continue
		}
		report.Counts[typ+"扫描"]++
		if err != nil {
			report.Rejected = append(report.Rejected, err.Error())
			report.Counts[typ+"解析拒绝"]++
			continue
		}
		rewardIDs := make([]uint32, 0, len(rewards))
		for rewardID := range rewards {
			rewardIDs = append(rewardIDs, rewardID)
		}
		sort.Slice(rewardIDs, func(i, j int) bool { return rewardIDs[i] < rewardIDs[j] })
		for _, rewardID := range rewardIDs {
			reward, err := catalog.ResolveItem(rewardID)
			if err != nil {
				report.Counts["奖励物品定义不可解析"]++
				continue
			}
			if currentNewPVFEquipmentSealFlag(reward) == 0 {
				continue
			}
			key := typ + "/封装装备"
			if currentPVFItemTargetContainer(reward).Avatar {
				key = typ + "/封装装扮"
			}
			report.Counts[key]++
			if len(report.Samples[key]) < 8 {
				document, err := parseDungeonCardPVFDocument(catalog.source, source.PVFPath)
				if err != nil {
					t.Fatal(err)
				}
				report.Samples[key] = append(report.Samples[key], row{SourceID: id, Name: magicBoxDocumentText(document, "name"), Type: typ, Path: source.PVFPath, RewardID: rewardID, RewardType: reward.EquipmentType, RewardPath: reward.PVFPath, Category: rewards[rewardID]})
			}
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if report.Counts["upgradable legacy/封装装备"] == 0 || report.Counts["booster selection/封装装备"] == 0 {
		t.Fatal("当前PVF未发现可核对的封装罐子和自选装备")
	}
	t.Logf("真实PVF封装奖励来源计数：%v；解析拒绝表数=%d", report.Counts, len(report.Rejected))
}

func realPackageSealCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF后核对开盒、开罐与自选封装")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func packageSealFixture(t *testing.T, catalog *pvfDungeonDropCatalog, source dungeonDropItemDefinition, mode int) (*Service, dnfrepo.Group, *gameSession) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Job: "0", Level: 90}); err != nil {
		t.Fatal(err)
	}
	source, err := currentPVFItemDefinitionForGrantAt(source, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := buildCurrentDungeonPickupStack(75, source, 2)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]dnfrepo.ItemStack{"0:75": stack}
	if mode > 0 {
		for slot := int16(9); slot <= 64; slot++ {
			slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
	}
	if mode > 1 {
		for slot := int16(3); slot <= 8; slot++ {
			slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	service := currentBoosterTestService(catalog, repositories)
	service.options.currentClientVersion = "official-24.9"
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	service.bindGameSessionCharacter(session, 19)
	return service, repositories, session
}

// 固定抽取只验证一个真实可得奖励的实例状态，不用于声明实际概率或整盒内容。
func packageSealFixedReward(t *testing.T, resolution alignedcmd.MagicBoxResolution, id uint32) alignedcmd.MagicBoxResolution {
	t.Helper()
	for _, group := range resolution.Groups {
		for _, entry := range group.Entries {
			if entry.ItemID == int64(id) && entry.Weight > 0 {
				resolution.Groups = []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{entry}}}
				return resolution
			}
		}
	}
	t.Fatalf("指定奖励不在真实PVF的正权重池中：item=%d", id)
	return resolution
}

func assertPackageSealInventoryAndWire(t *testing.T, service *Service, repositories dnfrepo.Group, session *gameSession, id uint32, want byte, mode int) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("发放库存缺失：found=%t err=%v", found, err)
	}
	foundReward := false
	for key, stack := range inventory.Slots {
		if stack.ItemID != int64(id) {
			continue
		}
		list, slot, ok := parseSceneInventorySlotKey(key)
		if !ok {
			t.Fatal("奖励落格无效")
		}
		if mode == 0 && (slot < 9 || slot > 64) || mode == 1 && (slot < 3 || slot > 8) {
			t.Fatalf("封装修补改变了装备落格：mode=%d key=%s", mode, key)
		}
		entry := currentItemListEntryFromStack(list, slot, stack)
		if entry.data[0x0D] != want {
			t.Fatalf("装备封装丢失：item=%d want=%d stack=%+v", id, want, stack)
		}
		if len(stack.RawEntry) > 0 && stack.RawEntry[0x0D] != want {
			t.Fatal("保存的原始行封装与实例不一致")
		}
		if err := service.sendSelectedIncrementalItemSlotRefreshes(session, "封装入包专项", []alignedcmd.ItemSlotRefresh{{ListType: list, SlotIndex: slot}}); err != nil {
			t.Fatal(err)
		}
		wireFound := false
		for _, packet := range splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes()) {
			if packet.Header.MsgID == 14 && bytes.Contains(packet.Body, entry.data[:]) {
				wireFound = true
			}
		}
		if !wireFound {
			t.Fatal("实际op14没有保留装备封装状态")
		}
		foundReward = true
	}
	if !foundReward {
		t.Fatalf("没有发放待核对装备：item=%d", id)
	}
}

func TestRealPVFBoxJarAndSelectionEquipmentKeepSeal(t *testing.T) {
	catalog := realPackageSealCatalog(t)
	for _, sample := range []struct {
		name               string
		sourceID, rewardID uint32
		kind               currentBoosterRequestKind
	}{
		{"随机盒", 490702317, 400330118, currentBoosterRequestRandom},
		{"传承罐", 4832, 10165, currentBoosterRequestRandom},
		{"魔笛称号自选", 2683861, 400330000, currentBoosterRequestSelection},
	} {
		for mode := 0; mode < 2; mode++ {
			t.Run(fmt.Sprintf("%s/%d", sample.name, mode), func(t *testing.T) {
				definition, err := resolveCurrentBoosterDefinition(catalog, sample.sourceID, sample.kind)
				if err != nil {
					t.Fatal(err)
				}
				request := currentBoosterOpenRequest{SourceSlot: 75, Kind: sample.kind}
				if sample.kind == currentBoosterRequestRandom {
					definition.Random = packageSealFixedReward(t, definition.Random, sample.rewardID)
				} else {
					selected := false
					for category, entries := range definition.SelectionCategory {
						for _, entry := range entries {
							if entry.ItemID != sample.rewardID {
								continue
							}
							request.SelectionContext = category
							choices := []currentBoosterSelectionCandidate{entry}
							if definition.SelectionRequired == 0 {
								choices = entries
							} else if definition.SelectionRequired != 1 {
								t.Fatal("此真实样例需要不同选择数")
							}
							for _, choice := range choices {
								request.Selections = append(request.Selections, currentBoosterSelectionRequest{ItemID: choice.ItemID, Option: choice.Option})
							}
							selected = true
							break
						}
						if selected {
							break
						}
					}
					if !selected {
						t.Fatal("真实自选分类中没有目标装备")
					}
				}
				service, repositories, session := packageSealFixture(t, catalog, definition.Source, mode)
				if definition.Random.MaterialItemID > 0 {
					inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
					inventory.Slots["0:109"] = dnfrepo.ItemStack{ItemID: definition.Random.MaterialItemID, Count: definition.Random.MaterialCountPerUse}
					if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
						t.Fatal(err)
					}
				}
				result, err := service.commitCurrentBooster(context.Background(), session, catalog, definition, request)
				if err != nil || result.SourceRemaining != 1 {
					t.Fatalf("开盒/开罐/自选发放失败：result=%+v err=%v", result, err)
				}
				assertPackageSealInventoryAndWire(t, service, repositories, session, sample.rewardID, 1, mode)
				if sample.name == "传承罐" {
					body, err := service.buildCurrentLotteryResultBody(context.Background(), session, catalog, result)
					if err != nil || len(body) < 3+currentItemListEntryWireSize || body[3+0x0D] != 1 {
						t.Fatalf("当前客户端开罐结果封装丢失：body=%x err=%v", body, err)
					}
				}
			})
		}
	}
}

func TestRealPVFMagicBoxEquipmentSealSurvivesMailOverflow(t *testing.T) {
	catalog := realPackageSealCatalog(t)
	const sourceID, rewardID = uint32(200), uint32(26155)
	source, err := catalog.ResolveItem(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveCurrentMagicBox(catalog, catalog.source, int64(sourceID))
	if err != nil {
		t.Fatal(err)
	}
	resolution = packageSealFixedReward(t, resolution, rewardID)
	for _, opens := range []uint16{1, 2} {
		for mode := 0; mode < 3; mode++ {
			t.Run(fmt.Sprintf("%d/%d", opens, mode), func(t *testing.T) {
				service, repositories, session := packageSealFixture(t, catalog, source, mode)
				materialSlot := int16(-1)
				if resolution.MaterialItemID > 0 {
					materialSlot = 109
					inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
					inventory.Slots["0:109"] = dnfrepo.ItemStack{ItemID: resolution.MaterialItemID, Count: resolution.MaterialCountPerUse * int64(opens)}
					if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
						t.Fatal(err)
					}
				}
				owner, err := packageitem.NewOwner(repositories)
				if err != nil {
					t.Fatal(err)
				}
				boxResolver := func(int64) (alignedcmd.MagicBoxResolution, error) { return resolution, nil }
				rewardResolver := func(id int64) (alignedcmd.MagicBoxRewardItem, error) {
					return resolveCurrentMagicBoxRewardItem(catalog, catalog.source, id)
				}
				var result packageitem.MagicBoxResult
				if opens == 1 {
					result, err = owner.ApplyMagicBox(context.Background(), packageitem.MagicBoxCommand{SelectedCharacterID: 19, RawListType: 4, ListType: 0, SlotIndex: 75, MaterialSlotIndex: materialSlot}, boxResolver, rewardResolver)
				} else {
					result, err = owner.ApplyMagicBoxExpand(context.Background(), packageitem.MagicBoxExpandCommand{SelectedCharacterID: 19, RawListType: 4, ListType: 0, SlotIndex: 75, BoxItemID: sourceID, MaterialSlotIndex: materialSlot, MaterialItemID: uint32(resolution.MaterialItemID), OpenCount: opens}, boxResolver, rewardResolver)
				}
				if err != nil || !result.Success {
					t.Fatalf("实际魔盒发放失败：result=%+v err=%v", result, err)
				}
				if mode < 2 && (len(result.Rewards) == 0 || result.Rewards[0].SealFlag != 1) {
					t.Fatal("实际魔盒结果列表丢失封装状态")
				}
				if mode == 2 {
					claimPackageSealOverflow(t, service, repositories, session, result.OverflowMailID)
				}
				assertPackageSealInventoryAndWire(t, service, repositories, session, rewardID, 1, mode%2)
			})
		}
	}
}

func TestRealPVFCeraPackageSealedEquipmentSurvivesMailOverflow(t *testing.T) {
	catalog := realPackageSealCatalog(t)
	definition, err := resolveCurrentCeraPackageDefinition(catalog, 2683004)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.AvatarItemIDs) != 0 {
		t.Fatal("真实称号礼包不应有装扮选择项")
	}
	for mode := 0; mode < 3; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			service, repositories, session := packageSealFixture(t, catalog, definition.Source, mode)
			result, err := service.commitCurrentCeraPackage(context.Background(), session, catalog, definition, currentCeraPackageOpenRequest{SourceSlot: 75})
			if err != nil {
				t.Fatal(err)
			}
			if mode == 2 {
				claimPackageSealOverflow(t, service, repositories, session, result.OverflowMailID)
			}
			assertPackageSealInventoryAndWire(t, service, repositories, session, 2747697, 1, mode%2)
		})
	}
}

func claimPackageSealOverflow(t *testing.T, service *Service, repositories dnfrepo.Group, session *gameSession, mailID string) {
	t.Helper()
	if mailID == "" {
		t.Fatal("两处满时没有生成溢出邮件")
	}
	mailbox, found, err := repositories.Mailbox.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("溢出邮件缺失：found=%t err=%v", found, err)
	}
	for _, attachment := range mailbox.Mails[mailID].Attachments {
		definition, err := service.pvfItemCatalog.ResolveItem(uint32(attachment.ItemID))
		if err != nil {
			t.Fatal(err)
		}
		if normalizeMagicBoxPVFType(definition.AttachType) == "sealing" && definition.Kind == dungeonDropItemEquipment && attachment.Extra["seal_flag"] != "1" {
			t.Fatalf("溢出附件封装丢失：attachment=%+v", attachment)
		}
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	for slot := int16(3); slot <= 64; slot++ {
		if inventory.Slots[currentDungeonPickupMainSlotKey(slot)].ItemID == 999 {
			delete(inventory.Slots, currentDungeonPickupMainSlotKey(slot))
		}
	}
	if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	var id uint32
	if _, err := fmt.Sscan(mailID, &id); err != nil {
		t.Fatal(err)
	}
	handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxExtractItem), mailIDListBody(id))
	if err != nil || !handled {
		t.Fatalf("溢出邮件领取失败：handled=%t err=%v", handled, err)
	}
}
