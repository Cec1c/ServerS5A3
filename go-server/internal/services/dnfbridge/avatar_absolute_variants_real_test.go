package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 每例使用独立内存事务，只共享只读PVF目录，避免反复装载百万文件索引。
func absoluteAvatarIsolatedRepos(t *testing.T, job byte, slots map[string]dnfrepo.ItemStack) dnfrepo.Group {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Job: strconv.Itoa(int(job))}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots}); err != nil {
		t.Fatal(err)
	}
	return repos
}

func absoluteAvatarExpectedOutputs(t *testing.T, source dnfpvf.Source) map[int64]map[string]map[string]uint32 {
	t.Helper()
	text, err := source.ReadText(currentAbsoluteAvatarMapPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(currentAbsoluteAvatarMapPath, text)
	if err != nil {
		t.Fatal(err)
	}
	result := map[int64]map[string]map[string]uint32{}
	key := int64(-1)
	for _, section := range doc.Sections {
		values := doc.Tokens[section.Start:section.End]
		switch section.Name {
		case "absolute bind cube":
			if len(values) != 1 || values[0].Kind != dnfpvf.TokenInt {
				t.Fatal("真实梦想套装键形状异常")
			}
			key = values[0].Int
			if _, exists := result[key]; exists {
				t.Fatalf("真实梦想表重复套装键%d", key)
			}
			result[key] = map[string]map[string]uint32{}
		case "absolute bind plus rare":
			if key < 0 {
				t.Fatal("真实梦想表产出不属于任何套装")
			}
			job := ""
			for i := 0; i < len(values); {
				if values[i].Kind != dnfpvf.TokenString {
					t.Fatal("真实梦想表职业或部位类型异常")
				}
				name := strings.ToLower(strings.TrimSpace(values[i].Value))
				if strings.HasPrefix(name, "[") {
					job = name
					result[key][job] = map[string]uint32{}
					i++
					continue
				}
				if job == "" || i+1 >= len(values) || values[i+1].Kind != dnfpvf.TokenInt || values[i+1].Int <= 0 {
					t.Fatal("真实梦想表产出行异常")
				}
				if result[key][job][name] != 0 {
					t.Fatal("真实梦想表同职业部位重复")
				}
				result[key][job][name] = uint32(values[i+1].Int)
				i += 2
			}
		case "/absolute bind cube":
			key = -1
		}
	}
	return result
}

func absoluteAvatarRealMaterial(t *testing.T, real realAvatarCompoundPVFAssets, job byte) uint32 {
	t.Helper()
	config, err := loadCurrentAvatarCompoundConfig(real.source, job)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range config.Variants {
		if strings.HasPrefix(v.Path, currentAvatarCompoundCHNRoot+"/") {
			continue
		}
		for _, part := range currentAvatarCompoundPartOrder {
			for _, row := range v.Parts[part].Upper {
				item, err := real.catalog.ResolveItem(row.ItemID)
				if err != nil || item.Grade != v.UpperGrade {
					continue
				}
				text, err := real.source.ReadText(item.PVFPath)
				if err != nil {
					t.Fatal(err)
				}
				doc, err := dnfpvf.Parse(item.PVFPath, text)
				if err != nil {
					t.Fatal(err)
				}
				if currentAbsoluteAvatarAllowsJob(doc, job) {
					return row.ItemID
				}
			}
		}
	}
	t.Fatalf("职业%d的真实PVF没有可供梦想使用的高级装扮", job)
	return 0
}

func TestRealPVFAbsoluteAvatarAllDeclaredVariants(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	archive, ok := real.source.(*platformpvf.Archive)
	if !ok {
		t.Fatal("需要真实PVF归档")
	}
	var cubes []dungeonDropItemDefinition
	scanned, missing := 0, 0
	for _, id := range real.catalog.stackableIDs {
		ref := real.catalog.itemRefs[id]
		scanPath := ref.path
		if !strings.HasPrefix(strings.ToLower(scanPath), "stackable/") {
			scanPath = "stackable/" + scanPath
		}
		if _, found := archive.FindFile(scanPath); !found {
			missing++
			continue
		}
		_, text, err := readDungeonDropText(real.source, "stackable", ref.path)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if !strings.Contains(strings.ToLower(text), "[absolute bind cube]") {
			continue
		}
		cube, err := real.catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(cube.ActionType, "[absolute bind cube]") {
			cubes = append(cubes, cube)
		}
	}
	if len(cubes) == 0 {
		t.Fatalf("扫描没有命中任何真实梦想合成器：已读%d、索引缺文件%d", scanned, missing)
	}
	sort.Slice(cubes, func(i, j int) bool { return cubes[i].ItemID < cubes[j].ItemID })
	expected := absoluteAvatarExpectedOutputs(t, real.source)
	passed, rejected, unconfigured := 0, 0, 0
	for _, cube := range cubes {
		text, err := real.source.ReadText(cube.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(cube.PVFPath, text)
		if err != nil {
			t.Fatal(err)
		}
		action, ok := doc.Section("action type")
		if !ok || len(action) != 6 {
			t.Fatalf("梦想%d动作形状未覆盖", cube.ItemID)
		}
		key, count := action[1].Int, int(action[2].Int)
		if key < 0 || count < 1 || count > 8 || len(expected[key]) == 0 {
			t.Fatalf("梦想%d真实套装或材料配置无效", cube.ItemID)
		}
		usable, hasUsable := doc.Section("usable job")
		allowed := map[string]bool{}
		for _, token := range usable {
			if token.Kind == dnfpvf.TokenString {
				allowed[strings.ToLower(token.Value)] = true
			}
		}
		countBefore := passed
		for number := 0; number <= jobmap.MaxID; number++ {
			job := byte(number)
			tag := strings.ToLower(jobmap.QuestTags(number)[0])
			outputs := expected[key][tag]
			permitted := !hasUsable || allowed["[all]"] || allowed[tag]
			if !permitted || len(outputs) == 0 {
				// 外职业或缺少产出的职业均不能借用其它职业物品。
				var foreign uint32
				for _, row := range expected[key] {
					for _, id := range row {
						foreign = id
						break
					}
					if foreign != 0 {
						break
					}
				}
				if _, err := loadCurrentAbsoluteAvatarRule(real.source, cube, job, foreign); err == nil {
					t.Fatalf("梦想%d错误放行职业%d", cube.ItemID, job)
				}
				if !permitted {
					rejected++
				} else {
					unconfigured++
				}
				continue
			}
			material := absoluteAvatarRealMaterial(t, real, job)
			for _, part := range currentAvatarCompoundPartOrder {
				target, ok := outputs[part]
				if !ok {
					t.Fatalf("梦想%d职业%d缺少部位%s", cube.ItemID, job, part)
				}
				t.Run(fmt.Sprintf("道具%d/职业%d/%s", cube.ItemID, job, part), func(t *testing.T) {
					definition, err := real.catalog.ResolveItem(target)
					if err != nil {
						t.Fatal(err)
					}
					if definition.EquipmentType != currentAvatarCompoundPartEquipmentTypes[part] {
						t.Fatal("PVF产出部位与道具不符")
					}
					body := make([]byte, 144)
					binary.LittleEndian.PutUint16(body[13:], 9)
					binary.LittleEndian.PutUint32(body[16:], target)
					binary.LittleEndian.PutUint16(body[22:], uint16(count))
					slots := map[string]dnfrepo.ItemStack{"0:9": {ItemID: int64(cube.ItemID), Count: 2}}
					for i := 0; i < count; i++ {
						slot := 40 + i
						slots[fmt.Sprintf("1:%d", slot)] = dnfrepo.ItemStack{ItemID: int64(material), Count: 1}
						binary.LittleEndian.PutUint16(body[24+6*i:], uint16(slot))
						binary.LittleEndian.PutUint32(body[26+6*i:], material)
					}
					repos := absoluteAvatarIsolatedRepos(t, job, slots)
					conn := &bufferConn{}
					svc := currentAvatarCompoundTestService(repos, real.catalog)
					session := &gameSession{conn: conn, selectedCharacterID: 19}
					if err := svc.handleCurrentAbsoluteAvatar(session, body); err != nil {
						t.Fatal(err)
					}
					ack, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
					if ack.Body[0] != 1 || len(ack.Body) != 55 || binary.LittleEndian.Uint32(ack.Body[7:]) != target {
						t.Fatalf("真实梦想%d套装%d职业%d失败:%x", cube.ItemID, key, job, ack.Body)
					}
					refresh, rest := splitGameServerUpperPacket(t, tail)
					if refresh.Header.MsgID != 14 || len(rest) != 0 {
						t.Fatal("梦想刷新协议不符")
					}
					after, _, _ := repos.Inventory.Load(context.Background(), "19")
					if len(after.Slots) != 2 || after.Slots["0:9"].Count != 1 || after.Slots["1:40"].ItemID != int64(target) {
						t.Fatalf("真实梦想扣料或产出错误:%+v", after.Slots)
					}
					conn.write.Reset()
					if err := svc.handleCurrentAbsoluteAvatar(session, body); err != nil {
						t.Fatal(err)
					}
					again, _, _ := repos.Inventory.Load(context.Background(), "19")
					if !reflect.DeepEqual(after.Slots, again.Slots) {
						t.Fatal("重复请求二次扣料")
					}
				})
				passed++
			}
		}
		t.Logf("梦想合成器 id=%d name=%s pvf_key=%d 材料数=%d 合法职业部位组合=%d", cube.ItemID, cube.Name, key, count, passed-countBefore)
	}
	t.Logf("全量梦想核对：扫描存在的STK=%d 索引缺文件=%d 合成器=%d 合法组合=%d 职业限制拒绝=%d 缺职业产出拒绝=%d", scanned, missing, len(cubes), passed, rejected, unconfigured)
	if passed == 0 {
		t.Fatal("没有完成任何真实梦想合成")
	}
}

func assertAbsoluteAvatarReplay(t *testing.T, real realAvatarCompoundPVFAssets, cube uint32, body []byte) {
	t.Helper()
	request, err := parseCurrentAbsoluteAvatarRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]dnfrepo.ItemStack{fmt.Sprintf("0:%d", request.ConsumeSlot): {ItemID: int64(cube), Count: 1}}
	for _, row := range request.Materials {
		slots[fmt.Sprintf("1:%d", row.Slot)] = dnfrepo.ItemStack{ItemID: int64(row.ItemID), Count: 1}
	}
	repos := absoluteAvatarIsolatedRepos(t, 0, slots)
	svc := currentAvatarCompoundTestService(repos, real.catalog)
	conn := &bufferConn{}
	if err := svc.handleCurrentAbsoluteAvatar(&gameSession{conn: conn, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(ack.Body) != 55 || ack.Body[0] != 1 {
		t.Fatalf("现场请求%d失败：%x", cube, ack.Body)
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	row := after.Slots[fmt.Sprintf("1:%d", request.Materials[0].Slot)]
	if len(after.Slots) != 1 || row.ItemID != int64(request.Target) || row.Extra["ext_data0"] != strconv.Itoa(int(request.Ability)) {
		t.Fatalf("现场请求资产不符：%+v", after.Slots)
	}
}

// 四种现场业务语义使用重新构造的公开夹具回归，不提交原始报文。
func TestRealPVFAbsoluteAvatarFourLiveRequests(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	for _, tc := range []struct {
		name string
		cube uint32
		body []byte
	}{
		{"金色战灵成功", 10007850, absoluteAvatarReplayBody(39643, 4, 39525, 401550029, 401510016, 101530639, 101530637, 401500030, 401550027, 401540010)},
		{"黑色战灵成功", 10007849, absoluteAvatarReplayBody(39220, 0, 101570589, 401510014, 101520660, 401540008, 101500886, 101520660, 101500886, 101570589)},
		{"天光云影头发原失败", 10007246, absoluteAvatarReplayBody(101560739, 4, 101530614, 101511038, 101511038, 101530614, 401500032, 39867, 101550703, 101540789)},
		{"天光云影面部原失败", 10007246, absoluteAvatarReplayBody(101570491, 2, 101540789, 101550703, 101530614, 40266, 101530614, 101540789, 101550703, 401500032)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAbsoluteAvatarReplay(t, real, tc.cube, tc.body)
		})
	}
}
