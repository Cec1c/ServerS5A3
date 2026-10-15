package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	dnfcompound "longheng.io/server/internal/modules/dnf/equipmentcompound"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func regenerationSource() dungeonDropCatalogTestSource {
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "3167 `material/crystal.stk`\n10099774 `material/unique.stk`\n10099775 `material/legend.stk`\n",
		"equipment/equipment.lst": "3000 `3000.equ`\n3001 `3001.equ`\n3100 `3100.equ`\n3101 `3101.equ`\n4000 `4000.equ`\n4001 `4001.equ`\n",
		dnfcompound.RulesPath: "[choose part]\n1 `武器` `katana`\n[/choose part]\n[choose part]\n2 `防具` `cl coat`\n[/choose part]\n[choose part]\n3 `首饰` `amulet`\n[/choose part]\n[choose part]\n4 `特殊装备` `earring`\n[/choose part]\n" +
			"[compound need material for specific]\n3 2 10099774 3 3167 50 6 2 10099775 2 3167 100\n[/compound need material for specific]\n[compound specific apply cost by level]\n3 1 1.00 3 85 3.00 3 90 4.00 6 1 1.00 6 85 3.00 6 90 4.00\n[/compound specific apply cost by level]\n",
		// 测试强制85→90，便于验证换模板、保留封装和原生结果查槽顺序。
		dnfcompound.LevelsPath: "[regen level limit]\n3 1 100000 0 3 85 0 100000 3 90 100000 0 6 1 100000 0 6 85 0 100000 6 90 100000 0\n",
	}
	for _, name := range []string{"crystal", "unique", "legend"} {
		source["stackable/material/"+name+".stk"] = "[name]\n`合成材料`\n[stackable type]\n`[material]` 0\n[stack limit]\n999999\n"
	}
	for _, row := range []struct {
		id            int
		rarity, level int
		legacy        bool
	}{{3000, 3, 85, false}, {3001, 3, 90, false}, {3100, 3, 85, true}, {3101, 3, 90, true}, {4000, 6, 85, false}, {4001, 6, 90, false}} {
		category := ""
		if row.legacy {
			category = "[item category]\n`legacy`\n"
		}
		source[fmt.Sprintf("equipment/%d.equ", row.id)] = fmt.Sprintf("[name]\n`装备%d`\n[rarity]\n%d\n[minimum level]\n%d\n[creation rate]\n100\n[grade]\n80\n[attach type]\n`[sealing]`\n[item group name]\n`katana`\n[equipment type]\n`[weapon]`\n[durability]\n35\n%s", row.id, row.rarity, row.level, category)
	}
	return source
}

func regenerationFixture(t *testing.T, id int64, seal byte) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 1234}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: id, Count: 1, Extra: map[string]string{"item_kind": "equipment", "seal_flag": fmt.Sprint(seal), "ext_data0": "12", "durability": "17", "value_a": "222"}}, "0:121": {ItemID: 3167, Count: 1000}}}); err != nil {
		t.Fatal(err)
	}
	if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:363": {ItemID: 10099774, Count: 100}, "0:364": {ItemID: 10099775, Count: 100}}}); err != nil {
		t.Fatal(err)
	}
	service := equipmentRecipeCompoundTestService(t, regenerationSource(), group)
	service.options.currentClientVersion = "official-24.9"
	if err := service.preloadEquipmentRegenerationCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "装备合成测试", selectedCharacterID: 19}
	return service, session, conn, group
}

func TestEquipmentRegenerationRequestStrict90Layout(t *testing.T) {
	for _, row := range []struct {
		body []byte
		want dnfcompound.Request
	}{{[]byte{0x1c, 0, 0, 0, 3, 0}, dnfcompound.Request{SourceSlot: 28, Part: 3}}, {[]byte{0x12, 0, 0, 0, 1, 0}, dnfcompound.Request{SourceSlot: 18, Part: 1}}} {
		got, err := parseCurrentEquipmentRegenerationRequest(row.body)
		if err != nil || got != row.want {
			t.Fatalf("解析%+v %v", got, err)
		}
	}
	for _, body := range [][]byte{nil, {9, 0, 0, 0, 1}, {9, 0, 0, 0, 1, 0, 0}, {0xff, 0xff, 0, 0, 1, 0}, {9, 0, 1, 0, 1, 0}, {9, 0, 0, 0, 14, 0}, {9, 0, 0, 0, 0, 0}} {
		if _, err := parseCurrentEquipmentRegenerationRequest(body); err == nil {
			t.Fatalf("畸形请求被放行%x", body)
		}
	}
}

func TestEquipmentRegenerationSealedSourceState(t *testing.T) {
	rawSealed := make([]byte, currentItemListEntryWireSize)
	rawSealed[0x0d] = 1
	for _, tc := range []struct {
		name  string
		stack dnfrepo.ItemStack
		want  bool
	}{
		{"明确封装", dnfrepo.ItemStack{Extra: map[string]string{"seal_flag": "1"}}, true},
		{"明确解封覆盖旧行", dnfrepo.ItemStack{RawEntry: rawSealed, Extra: map[string]string{"seal_flag": "0"}}, false},
		{"首次穿戴已解封", dnfrepo.ItemStack{RawEntry: rawSealed, Extra: map[string]string{"seal_flag": "1", "seal_removed_by_first_equip": "1"}}, false},
		{"当前行封装", dnfrepo.ItemStack{RawEntry: rawSealed}, true},
		{"仅Bind不证明封装", dnfrepo.ItemStack{Bind: true}, false},
		{"非法标记", dnfrepo.ItemStack{Extra: map[string]string{"seal_flag": "2"}}, false},
		{"未登记封装", dnfrepo.ItemStack{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentEquipmentRegenerationSourceSealed(tc.stack); got != tc.want {
				t.Fatalf("封装资格%t，预期%t", got, tc.want)
			}
		})
	}
}

func TestEquipmentRegenerationUnsealedOnlyFailure(t *testing.T) {
	for _, id := range []int64{3000, 3100, 4000} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, session, conn, g := regenerationFixture(t, id, 0)
			before, _, _ := g.Inventory.Load(context.Background(), "19")
			account, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
			if err := s.handleCurrentEquipmentRegeneration(session, []byte{9, 0, 0, 0, 1, 0}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := g.Inventory.Load(context.Background(), "19")
			accountAfter, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
			if ack.Header.MsgID != 407 || !bytes.Equal(ack.Body, []byte{0, 17}) || len(rest) != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, accountAfter) {
				t.Fatal("解封来源未被整笔拒绝")
			}
		})
	}
}

type regenerationReadProbe struct {
	source  dungeonDropCatalogTestSource
	reads   int
	blocked bool
}

func (p *regenerationReadProbe) ReadText(name string) (string, error) {
	p.reads++
	if p.blocked {
		return "", fmt.Errorf("测试禁止请求阶段重读PVF：%s", name)
	}
	return p.source.ReadText(name)
}

func TestEquipmentRegenerationCatalogOnlyLoadsAtStartup(t *testing.T) {
	probe := &regenerationReadProbe{source: regenerationSource()}
	items, err := newPVFDungeonDropCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pvfItemCatalog: items}
	reads := probe.reads
	if _, _, err = s.currentEquipmentRegenerationCatalog(); !errors.Is(err, dnfcompound.ErrCandidates) || probe.reads != reads {
		t.Fatal("未预载时请求访问器扫描了PVF或放行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.preloadEquipmentRegenerationCatalog(ctx); !errors.Is(err, context.Canceled) || items.equipmentCompoundCatalog != nil {
		t.Fatal("取消的启动加载发布了缓存")
	}
	if err = s.preloadEquipmentRegenerationCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, catalog, err := s.currentEquipmentRegenerationCatalog()
	if err != nil || catalog == nil || probe.reads <= reads {
		t.Fatal("启动阶段没有建立可用缓存")
	}
	reads, probe.blocked = probe.reads, true
	if err = s.preloadEquipmentRegenerationCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, again, err := s.currentEquipmentRegenerationCatalog()
	if err != nil || again != catalog || probe.reads != reads {
		t.Fatal("已加载缓存被重新扫描")
	}
}

func TestEquipmentRegenerationMissingStartupCatalogPreservesAssets(t *testing.T) {
	s, session, conn, group := regenerationFixture(t, 4000, 1)
	before, _, _ := group.Inventory.Load(context.Background(), "19")
	accountBefore, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
	s.pvfItemCatalog.equipmentCompoundCatalog = nil
	if err := s.handleCurrentEquipmentRegeneration(session, []byte{9, 0, 0, 0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := group.Inventory.Load(context.Background(), "19")
	accountAfter, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("未完成启动加载仍修改玩家资产")
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 407 || !bytes.Equal(ack.Body, []byte{0, 17}) || len(rest) != 0 {
		t.Fatal("缺启动缓存的失败回包错误")
	}
}

func TestEquipmentRegenerationThreeFamiliesRefreshBeforeACK(t *testing.T) {
	for _, id := range []int64{3000, 3100, 4000} {
		for _, seal := range []byte{1} {
			t.Run(fmt.Sprintf("%d_%d", id, seal), func(t *testing.T) {
				s, session, conn, group := regenerationFixture(t, id, seal)
				if err := s.handleGameCommand(session, 1, currentEquipmentRegenerationOpcode, []byte{9, 0, 0, 0, 1, 0}); err != nil {
					t.Fatal(err)
				}
				empty, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if empty.Header.MsgID != 14 || empty.Header.Classification != 0 || len(empty.Body) != 122 || binary.LittleEndian.Uint32(empty.Body[5:9]) != 0xffffffff {
					t.Fatal("同格换模板缺少删除行")
				}
				update, rest := splitGameServerUpperPacket(t, rest)
				if update.Header.MsgID != 14 || update.Header.Classification != 0 || update.Body[0] != 0 {
					t.Fatal("权威库存没有先发")
				}
				rows := int(binary.LittleEndian.Uint16(update.Body[1:3]))
				if len(update.Body) != 3+119*rows {
					t.Fatal("库存行尺寸不正确")
				}
				seen := map[uint16]bool{}
				var outputID uint32
				for offset := 3; offset < len(update.Body); offset += 119 {
					slot := binary.LittleEndian.Uint16(update.Body[offset:])
					if seen[slot] {
						t.Fatal("重复库存行")
					}
					seen[slot] = true
					if slot == 9 {
						outputID = binary.LittleEndian.Uint32(update.Body[offset+2:])
						validOutput := outputID == 4001
						if id != 4000 {
							validOutput = outputID == 3001 || outputID == 3101
						}
						if !validOutput || update.Body[offset+0x0d] != seal || update.Body[offset+0x0a] != 0 || binary.LittleEndian.Uint16(update.Body[offset+0x0b:]) != 35 {
							t.Fatal("产物封装/强化/耐久或模板不正确")
						}
					}
				}
				ack, rest := splitGameServerUpperPacket(t, rest)
				if ack.Header.MsgID != 407 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 9, 0}) || len(rest) != 0 {
					t.Fatal("当前消费者所需ACK或顺序错误")
				}
				inv, _, _ := group.Inventory.Load(context.Background(), "19")
				acc, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
				char, _, _ := group.Character.Load(context.Background(), "19")
				wantSoul, wantCrystal, slot := int64(9), int64(150), "0:363"
				if id == 4000 {
					wantSoul, wantCrystal, slot = 6, 300, "0:364"
				}
				if inv.Slots["0:9"].ItemID != int64(outputID) || inv.Slots["0:121"].Count != 1000-wantCrystal || acc.Slots[slot].Count != 100-wantSoul || char.Stats["gold"] != 1234 {
					t.Fatal("事务结果不正确")
				}
				if !seen[9] || !seen[121] || !seen[uint16(363+(id/4000))] {
					t.Fatal("材料或结果没有刷新")
				}
			})
		}
	}
}

// 同ID产物也必须重新建装、扣材并刷新库存，不能把旧实例原样留下。
func TestEquipmentRegenerationSameTemplateBuildsFreshEquipment(t *testing.T) {
	for _, id := range []int64{3000, 3100, 4000} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, session, conn, group := regenerationFixture(t, id, 1)
			source := regenerationSource()
			source["equipment/equipment.lst"] = fmt.Sprintf("%d `%d.equ`\n", id, id)
			source[dnfcompound.LevelsPath] = "[regen level limit]\n3 1 100000 0 3 85 100000 0 3 90 100000 0 6 1 100000 0 6 85 100000 0 6 90 100000 0\n"
			items, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			s.pvfItemCatalog = items
			if err = s.preloadEquipmentRegenerationCatalog(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = s.handleCurrentEquipmentRegeneration(session, []byte{9, 0, 0, 0, 1, 0}); err != nil {
				t.Fatal(err)
			}
			empty, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if empty.Header.MsgID != 14 || len(empty.Body) != 122 || binary.LittleEndian.Uint32(empty.Body[5:9]) != 0xffffffff {
				t.Fatal("同ID重建没有先删除旧库存行")
			}
			update, rest := splitGameServerUpperPacket(t, rest)
			if update.Header.MsgID != 14 || update.Header.Classification != 0 || len(update.Body) < 3 || len(update.Body) != 3+119*int(binary.LittleEndian.Uint16(update.Body[1:3])) {
				t.Fatal("同ID新装备库存包错误")
			}
			found := false
			for offset := 3; offset < len(update.Body); offset += 119 {
				if binary.LittleEndian.Uint16(update.Body[offset:]) == 9 {
					found = true
					if binary.LittleEndian.Uint32(update.Body[offset+2:]) != uint32(id) || update.Body[offset+0x0a] != 0 || binary.LittleEndian.Uint16(update.Body[offset+0x0b:]) != 35 || update.Body[offset+0x0d] != 1 {
						t.Fatal("同ID产物没有重置强化、耐久与封装")
					}
				}
			}
			ack, rest := splitGameServerUpperPacket(t, rest)
			if !found || ack.Header.MsgID != 407 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 9, 0}) || len(rest) != 0 {
				t.Fatal("同ID产物未在库存刷新后返回成功槽位")
			}
			inv, _, _ := group.Inventory.Load(context.Background(), "19")
			acc, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
			char, _, _ := group.Character.Load(context.Background(), "19")
			stack := inv.Slots["0:9"]
			if stack.ItemID != id || stack.Extra["source"] != "equipment_compound" || stack.Extra["seal_flag"] != "1" || stack.Extra["ext_data0"] == "12" || stack.Extra["durability"] == "17" || stack.Extra["value_a"] == "222" {
				t.Fatal("持久化保留了旧装备实例属性")
			}
			wantSoul, wantCrystal, slot := int64(9), int64(150), "0:363"
			if id == 4000 {
				wantSoul, wantCrystal, slot = 6, 300, "0:364"
			}
			if inv.Slots["0:121"].Count != 1000-wantCrystal || acc.Slots[slot].Count != 100-wantSoul || char.Stats["gold"] != 1234 {
				t.Fatal("同ID新装备没有按PVF正常扣材")
			}
		})
	}
}

func TestEquipmentRegenerationUpperRouteAndWrongClass(t *testing.T) {
	for _, class := range []byte{0, 1} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, session, conn, g := regenerationFixture(t, 4000, 1)
			before, _, _ := g.Inventory.Load(context.Background(), "19")
			handled, err := currentGameplayModules.DispatchUpper(s, session, 407, class, []byte{9, 0, 0, 0, 1, 0})
			if !handled || err != nil {
				t.Fatalf("分发%t %v", handled, err)
			}
			after, _, _ := g.Inventory.Load(context.Background(), "19")
			if class == 0 {
				if !reflect.DeepEqual(before, after) || conn.write.Len() != 0 {
					t.Fatal("错误classification触发写入")
				}
			} else if reflect.DeepEqual(before, after) {
				t.Fatal("upper入口未接入")
			}
		})
	}
}

func TestEquipmentRegenerationInsufficientMaterialsOnlyFailure(t *testing.T) {
	s, session, conn, g := regenerationFixture(t, 4000, 1)
	ctx := context.Background()
	account, _, _ := g.AccountInventory.Load(ctx, "account-1")
	delete(account.Slots, "0:364")
	g.AccountInventory.Save(ctx, account)
	before, _, _ := g.Inventory.Load(ctx, "19")
	if err := s.handleCurrentEquipmentRegeneration(session, []byte{9, 0, 0, 0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 407 || !bytes.Equal(packet.Body, []byte{0, 17}) || len(rest) != 0 {
		t.Fatal("失败发出了库存或成功包")
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("失败消耗来源")
	}
}

type regenerationFailureConn struct{ bufferConn }

func (c *regenerationFailureConn) Write([]byte) (int, error) {
	return 0, fmt.Errorf("模拟网络失败")
}
func TestEquipmentRegenerationWriteFailureDoesNotRecommit(t *testing.T) {
	s, session, _, g := regenerationFixture(t, 4000, 1)
	session.conn = &regenerationFailureConn{}
	if err := s.handleCurrentEquipmentRegeneration(session, []byte{9, 0, 0, 0, 1, 0}); err == nil {
		t.Fatal("网络故障被忽略")
	}
	account, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
	if account.Slots["0:364"].Count != 94 {
		t.Fatal("回包失败重新执行了扣材")
	}
}

func TestEquipmentRegenerationRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("未指定真实PVF，只读专项未运行")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pvfItemCatalog: items}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := time.Now()
	err = s.preloadEquipmentRegenerationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, catalog, err := s.currentEquipmentRegenerationCatalog()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := catalog.Preload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("真实PVF引用%d，已解析%d，非本玩法元数据%d，不可读%d，合法候选%d，首次建池%s", stats.References, stats.Parsed, stats.UnsupportedMetadata, stats.Unreadable, stats.Eligible, time.Since(started))
	for _, tc := range []struct {
		id     int64
		part   uint16
		legacy bool
	}{{102000399, 1, false}, {100050624, 2, true}, {10061, 2, false}} {
		source, err := catalog.Resolve(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		pools, err := catalog.Pools(source, tc.part)
		if err != nil {
			t.Fatal(err)
		}
		for _, pool := range pools {
			t.Logf("来源%d，品质%d，圣物%t，产出等级%d，等级权重%d，候选%d", tc.id, source.Rarity, source.Legacy(), pool.MinimumLevel, pool.Weight, len(pool.Candidates))
			for _, candidate := range pool.Candidates {
				if !catalog.Rules.Parts[tc.part][candidate.Definition.Group] || candidate.Definition.Rarity != source.Rarity || candidate.Definition.MinimumLevel != pool.MinimumLevel {
					t.Fatal("真实池类别混入")
				}
			}
		}
	}
	if catalog.Rules.Weights[106020449] != 250000 || catalog.Rules.Weights[101020458] != 500000 {
		t.Fatal("真实特殊权重未保留")
	}
	for _, id := range []int64{101000278, 100050255, 100050256, 10053} {
		if _, err := catalog.Resolve(id); !errors.Is(err, dnfcompound.ErrSource) {
			t.Fatalf("魔法封印/无正掉落模板%d仍进池：%v", id, err)
		}
	}
	for _, part := range []uint16{1, 2, 3} {
		checked := false
		for _, source := range catalog.Definitions() {
			if source.Rarity != 3 || !catalog.Rules.Parts[part][source.Group] {
				continue
			}
			pools, err := catalog.Pools(source, part)
			if err != nil {
				t.Fatal(err)
			}
			groups := map[string]bool{}
			var plain, legacy *dnfcompound.Definition
			for _, candidate := range pools[0].Candidates {
				d := candidate.Definition
				groups[d.Group] = true
				if d.Legacy() {
					legacy = &d
				} else {
					plain = &d
				}
			}
			// 实际低等级可能只有圣物；从目录选择确有两类产物的同级池。
			if plain == nil || legacy == nil || len(groups) < 2 {
				continue
			}
			plainPools, plainErr := catalog.Pools(*plain, part)
			legacyPools, legacyErr := catalog.Pools(*legacy, part)
			if plainErr != nil || legacyErr != nil || !reflect.DeepEqual(plainPools, legacyPools) {
				t.Fatalf("实际品质3大类%d普通神器与圣物双向取池不一致：%v %v", part, plainErr, legacyErr)
			}
			t.Logf("真实大类%d品质3共池：等级%d，普通来源%d，圣物来源%d，细类%d，同级候选%d，双向池与权重相同", part, source.MinimumLevel, plain.ItemID, legacy.ItemID, len(groups), len(pools[0].Candidates))
			checked = true
			break
		}
		if !checked {
			t.Fatalf("真实大类%d没有找到普通神器与圣物可双向核对的跨细类池", part)
		}
	}
	legacyDoc, err := parseDungeonCardPVFDocument(archive, "equipment/character/demonicLancer/weapon/pike/chn_414000001.equ")
	if err != nil {
		t.Fatal(err)
	}
	rate, hasRate := legacyDoc.Int("creation rate")
	_, legacyErr := catalog.Resolve(414000001)
	if hasRate && rate == 0 && !errors.Is(legacyErr, dnfcompound.ErrSource) {
		t.Fatal("明确零掉落圣物被无字段特例放行")
	}
	t.Logf("圣物光域魔神长枪掉落字段：存在%t 值%d 资格错误%v", hasRate, rate, legacyErr)
	low, err := catalog.Resolve(100310912)
	if err != nil || low.Name != "龙战玄黄 - 怒战之心" || low.MinimumLevel != 30 || low.CreationRate != 0 {
		t.Fatalf("实际30级传说来源未适配：%+v %v", low, err)
	}
	lowPools, err := catalog.Pools(low, 3)
	if err != nil || len(lowPools) != 1 || lowPools[0].MinimumLevel != 30 || len(lowPools[0].Candidates) < 2 {
		t.Fatalf("实际30级项链稀疏池错误：%+v %v", lowPools, err)
	}
	for _, candidate := range lowPools[0].Candidates {
		if !catalog.Rules.Parts[3][candidate.Definition.Group] {
			t.Fatal("30级首饰池混入非首饰")
		}
	}
	t.Logf("实际30级传说%s：首饰大类同级池%d", low.Name, len(lowPools[0].Candidates))
	for _, sample := range []struct {
		id   int64
		part uint16
	}{

		{100310914, 3}, {100310654, 3}, {100310656, 3},
	} {
		d, err := catalog.Resolve(sample.id)
		if err != nil || d.CreationRate != 0 {
			t.Fatalf("实际生成率0的合法装备%d仍被拒绝：%v", sample.id, err)
		}
		pools, err := catalog.Pools(d, sample.part)
		if err != nil || len(pools) == 0 || pools[0].MinimumLevel != d.MinimumLevel {
			t.Fatalf("实际装备%d缺少同级合成池：%v", sample.id, err)
		}
		t.Logf("实际可交易模板：%d %s 等级%d，合法等级池%d", d.ItemID, d.Name, d.MinimumLevel, len(pools))
	}
	definitions := catalog.Definitions()
	for _, source := range definitions {
		var part uint16
		for id, groups := range catalog.Rules.Parts {
			if groups[source.Group] {
				part = id
				break
			}
		}
		pools, err := catalog.Pools(source, part)
		if err != nil || len(pools) == 0 || pools[0].MinimumLevel != source.MinimumLevel || len(pools[0].Candidates) == 0 {
			t.Fatalf("全等级装备%d无合法同级产出：%v", source.ItemID, err)
		}
		for _, pool := range pools {
			for _, candidate := range pool.Candidates {
				d := candidate.Definition
				if d.Rarity != source.Rarity || !catalog.Rules.Parts[part][d.Group] || d.MinimumLevel != pool.MinimumLevel || d.AttachType != "sealing" || d.GeneratedOption || d.Temporary {
					t.Fatalf("装备%d产出池混入禁止模板%d", source.ItemID, d.ItemID)
				}
			}
		}
	}
	t.Logf("全等级只读核对完成：%d个合格模板均有同级产出池", len(definitions))
	seen := map[string]bool{}
	for _, source := range catalog.Definitions() {
		if source.MinimumLevel != 85 {
			continue
		}
		family := "粉装"
		if source.Legacy() {
			family = "圣物"
		} else if source.Rarity == 6 {
			family = "传说"
		}
		if seen[family] {
			continue
		}
		var part uint16
		for candidate, groups := range catalog.Rules.Parts {
			if groups[source.Group] {
				part = candidate
				break
			}
		}
		pools, err := catalog.Pools(source, part)
		if err != nil {
			continue
		}
		if len(pools) != 2 || pools[0].MinimumLevel != 85 || pools[1].MinimumLevel != 90 {
			t.Fatal("85级真实来源缺两级产出")
		}
		seen[family] = true
		t.Logf("85→90真实%s来源：%d %s；85级池%d，90级池%d", family, source.ItemID, source.Name, len(pools[0].Candidates), len(pools[1].Candidates))
		for _, pool := range pools {
			for _, candidate := range pool.Candidates {
				if !catalog.Rules.Parts[part][candidate.Definition.Group] || candidate.Definition.Rarity != source.Rarity || candidate.Definition.MinimumLevel != pool.MinimumLevel {
					t.Fatal("真实85→90池混入其他家族或等级")
				}
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("真实85→90三类池未覆盖：%v", seen)
	}
	levels, err := catalog.Rules.TargetLevels(3, 85)
	if err != nil || len(levels) != 2 || levels[1].Weight != 10000 {
		t.Fatal("真实粉装85→90权重错误")
	}
	legendLevels, err := catalog.Rules.TargetLevels(6, 85)
	if err != nil || len(legendLevels) != 2 || legendLevels[0].Weight != 95000 || legendLevels[1].Weight != 5000 {
		t.Fatal("真实传说85→90权重错误")
	}
	materials, err := catalog.Rules.Requirements(6, 85)
	if err != nil || !reflect.DeepEqual(materials, []dnfcompound.Material{{ItemID: 10099775, Count: 6}, {ItemID: 3167, Count: 300}}) {
		t.Fatal("真实85传说材料错误")
	}
}
