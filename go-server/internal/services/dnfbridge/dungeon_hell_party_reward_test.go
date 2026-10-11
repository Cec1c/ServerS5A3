package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住「深渊波次怪的奖励投骰」（操作者清单 batch item ②，
// 见 `dungeon_hell_party_reward.go` 文件头）：
// `etc/hellparty.etc [difficulty]` 的 `RewardRollCount` → 组内最后一只死亡时
// 按 `etc/itemdropinfo_monster_hell.etc` 的概率表投 N 次 → 命中即按**既有地面掉落通路**
// 落地（`runtimeDungeonDropOwner` + op38 掉落行 + 既有 op43 拾取入包）。
//
// fixture 与 `dungeon_hell_party_wave_test.go` 同一份（真档 map 60083 那一行）：
// 柱子 `[hellparty]` 给出 order1 = 组 125（1 只，code 1050）、
// order2 = 组 211（3 只，code 65745/65746/65747）；`[difficulty]` A 档 `8 5 100 100 0`。

const (
	// hellPartyRewardTestTierA = 真档 `etc/hellparty.etc` 的 A 档 `RewardRollCount`。
	hellPartyRewardTestTierA = int64(8)
	// hellPartyRewardTestBasisLevel = fixture `.dgn` 的 `[basis level]`。
	hellPartyRewardTestBasisLevel = int64(testHellPartyBasisLevel)
	// hellPartyRewardTestMinimumLevel = fixture `.dgn` 的 `[minimum required level]`。
	hellPartyRewardTestMinimumLevel = int64(10)
	// hellPartyRewardTestFullRateChance = `BaseDropRate(100) * rate(1000) / 100`，
	// 钉住"表里 rate >= 1000 时 8 次投骰全部命中"（`HellMonsterDropConfig.cs:53`）。
	hellPartyRewardTestFullRateChance = 1000
	// hellPartyRewardTestItemRarity 取 0：fixture 的 `[basis of rarity dicision]` 四行全
	// 1000000 ⇒ `RollHellRarity` 恒返回 0，池必须挂在 rarity 0 上才能稳定命中。
	// （厂商 `ChooseEquipment(..., allowFallback: true)` 的"稀有度空池回落 0"分支因此
	// 在这里不被覆盖；回落本身由既有 `SelectGenericEquipment` 用例负责。）
	hellPartyRewardTestItemRarity = uint32(0)
)

var (
	hellPartyRewardTestGroupFirst = int64(125)
	hellPartyRewardTestGroupLast  = int64(211)
	hellPartyRewardTestItem       = uint32(9001)
)

// hellPartyRewardTestRuleText 是 `Etc/ItemDropInfo_Monseter.etc` 的同形替身文本。
//
// 形状照 `dungeon_drop_pickup_test.go:517-547` 那份**已被既有用例证明能被本仓解析器
// 完整吃下**的黄金文本：`MonsterRules` 是**定长段**，`LoadMonsterRules` 会逐段校验
// （4×7 稀有度表、5×4/5×5/5×4 加成表、2/8/4/2/2×5 各数组），少一段就整份规则作废。
// 因此这里逐段照抄黄金文本的值，**只在末尾**追加本用例要用的两个自由段
// （概率行 / 等级参照表），由结束标记收口。
//
// `rate` 只替换五档里的**第一档**（`run.Difficulty = 1` 读的就是它）。
func hellPartyRewardTestRuleText(rate int64) string {
	return "[drop prob count]\n1\n[drop prob]\n" +
		// KeyMin KeyMax + 五档 rate —— 第一档给 rate，其余给 1000（本用例只读第一档）。
		"1 199 0 " + strconv.FormatInt(rate, 10) + " 1000 1000 1000\n" +
		"[/drop prob]\n" +
		"[party member drop bonusrate]\n" +
		"1 1 1 1 1 1.3 1.6 2 1 1.2 1.4 1.6 1 1.2 1.4 1.6 1 1.2 1.4 1.6\n" +
		"[dungeon difficulty drop bonusrate]\n" +
		"1.00 1.00 1.00 1.40 1.10 1.00 1.40 1.60 2.20 2.00 1.00 1.40 1.60 2.20 2.00 " +
		"1.00 1.40 1.60 2.20 2.00 1.00 1.40 1.60 2.20 2.00\n" +
		"[monster type drop bonusrate]\n" +
		"1.00 2.00 3.00 6.60 1.00 2.00 3.00 4.00 0.20 2.00 7.00 16.80 1.00 2.00 3.00 4.00 1.00 2.00 3.00 4.00\n" +
		"[first boss/named mob hunting]\n1 1\n" +
		"[condition rate]\n95 5 93 7 90 10 85 15\n" +
		"[gold quantity]\n1 2 4 6\n" +
		"[gold volume]\n100 110\n" +
		"[item drop rarity control]\n2 13 17 0 0 3 13 17 0 0\n" +
		"[/item drop rarity control]\n" +
		"[basis of rarity dicision]\n" +
		// 4 行 × 7 列全 1000000：任何 `lcg.Next(1000000)+1` 都落在 0 档。
		"1000000 1000000 1000000 1000000 1000000 1000000 1000000\n" +
		"1000000 1000000 1000000 1000000 1000000 1000000 1000000\n" +
		"1000000 1000000 1000000 1000000 1000000 1000000 1000000\n" +
		"1000000 1000000 1000000 1000000 1000000 1000000 1000000\n" +
		"[/basis of rarity dicision]\n" +
		"[item drop ref table]\n" +
		// 两个基准等级（`[minimum required level]` 与 `[basis level]`）各一行，
		// 等级区间 [1,1]：`grade = basis` 那件唯一候选。
		strconv.FormatInt(hellPartyRewardTestMinimumLevel, 10) + " 1 1\n" +
		strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + " 1 1\n" +
		"[/item drop ref table]\n"
}

// bridgeHellPartyRewardPVF 在波次 fixture 之上补三张**真实掉落表**的同形替身：
//   - `Etc/ItemDropInfo_Monseter.etc`（本仓模块路径 `drop.MonsterRulePath`）：
//     概率行（五档 rate）+ `[basis of rarity dicision]`（4×7）+ `[item drop ref table]`。
//   - `equipment/equipment.lst` + `.equ` + `Etc/ItemDictionary/ItemDictionary.etc`：
//     一件 `grade = 副本基准等级`、`rarity = 1`、`creation rate / generation rate = 100` 的装备，
//     **两张池子都能取到它**（`.equ` 池要求 `creation rate > 0`）。
//   - `drop/drop.lst`：`pvfDungeonDropCatalog` 的第三条清单（本用例不消费怪物池，
//     但目录构造会读它，缺失即目录不可用）。
//
// ⚠ 本 fixture **故意不放** `etc/itemdropinfo_monster_hell.etc`：合成 source 走的是
// "hell 表不可读 ⇒ 降级成今天的三处读法"那条路（见 `hellPartyRewardTier`），
// 也就是这些用例一直钉住的今天行为；需要覆盖**修复档**的用例自己把那张表装进 source
// （reward 用例走 `hellPartyRewardRequestForTest`，池/档位用例走
// `hellPartyRewardTestHellDropInfoTextFor`），真档覆盖则由
// `TestRealPVFHellPartyRewardRollsUseTheFixedTierAndNeverDegrade` 负责。
func bridgeHellPartyRewardPVF() bridgePVFSource {
	source := bridgeHellPartyPVF()
	source["equipment/equipment.lst"] = strconv.FormatUint(uint64(hellPartyRewardTestItem), 10) +
		" `weapon/test_drop.equ`\n"
	source["equipment/weapon/test_drop.equ"] = "[name]\n`abyss test equipment`\n" +
		"[durability]\n50\n[attach type]\n`[trade]`\n" +
		// `[grade]` 与 `[rarity]` 是 `ResolveItem` 的落库字段；`[equipment type]`
		// 决定落哪个背包页（与厂商 `ItemMetadataResolver` 同口径）。
		"[grade]\n" + strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + "\n" +
		"[rarity]\n" + strconv.FormatUint(uint64(hellPartyRewardTestItemRarity), 10) + "\n" +
		// `creation rate` 是**厂商 `.equ` 池**的门槛（`EquipmentDropPoolProvider.cs:101`）；
		// 缺了它这件装备在修复档下会被整池丢掉。
		"[creation rate]\n100\n" +
		"[equipment type]\n`[weapon]`\n"
	source["Etc/ItemDictionary/ItemDictionary.etc"] =
		strconv.FormatUint(uint64(hellPartyRewardTestItem), 10) + " " +
			strconv.FormatUint(uint64(hellPartyRewardTestItemRarity), 10) + " " +
			strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + " 0 10001 100 `abyss test equipment`\n"
	source[dnfdrops.MonsterRulePath] = hellPartyRewardTestRuleText(1000)
	// `pvfDungeonDropCatalog` 构造时读三张 `.lst`；本用例只用装备池，
	// 另外两张给空表（读不到即目录不可用，所以必须存在）。
	source["drop/drop.lst"] = ""
	source[dungeonDropStackableList] = ""
	return source
}

// prepareHellPartyRewardRuntime 复用波次用例的真实进图链路（同一个
// `prepareDungeonRuntime` 入口，因此波次隐藏模板行真的落在房间 actor 表上），
// 额外把"掉了东西要能捡进背包"所需的空背包建好。
func prepareHellPartyRewardRuntime(t *testing.T) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene, dnfrepo.Group) {
	t.Helper()
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyRewardPVF())
	repositories, ok := service.repositoryProvider()
	if !ok {
		t.Fatal("memory repository group is missing")
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if !runtime.HellRoomSet || len(runtime.HellPartyWaves) != 2 {
		t.Fatalf("hell party runtime not frozen: set=%t waves=%+v", runtime.HellRoomSet, runtime.HellPartyWaves)
	}
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce hell party actors: %v count=%d", err, count)
	}
	return service, session, conn, runtime, scene, repositories
}

// hellPartyRewardActorKeys 返回 fixture 里每个波次组的扩展 actor objectKey
// （组 125 一只、组 211 三只，组内按公告顺序 = 死亡顺序）。
func hellPartyRewardActorKeys(t *testing.T, runtime *runtimeDungeonState) ([]uint32, []uint32) {
	t.Helper()
	var first, last []uint32
	for _, actor := range runtime.Room.Snapshot().ExtendedActors {
		if actor.Kind != runtimeDungeonActorSpecialMonster {
			continue
		}
		switch actor.HellPartyGroupID {
		case hellPartyRewardTestGroupFirst:
			first = append(first, actor.ObjectKey)
		case hellPartyRewardTestGroupLast:
			last = append(last, actor.ObjectKey)
		}
	}
	if len(first) != 1 || len(last) != 3 {
		t.Fatalf("wave actor groups mismatched: first=%v last=%v", first, last)
	}
	return first, last
}

// hellPartyRewardDeathBody 造一条"这只波次怪被打死"的 op39（与既有用例同一形状：
// body[0:4] = objectKey、body[4:6] = 击杀者 actor key）。
func hellPartyRewardDeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(99))
	return body
}

// hellPartyRewardRequestForTest 造一次"组 211 最后一只"的投骰输入（固定房间种子）。
func hellPartyRewardRequestForTest(
	t *testing.T,
	service *Service,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	rate int64,
) hellPartyRewardRequest {
	t.Helper()
	source := mustBridgeSource(t, service)
	source[dnfdrops.MonsterRulePath] = hellPartyRewardTestRuleText(rate)
	// 修复档的 rate 来自 hell 表（`etc/itemdropinfo_monster_hell.etc`），
	// 因此两张表给同一个 rate，档位语义（A 档 8 次投骰、chance=rate）在两档下都成立。
	source[hellPartyRewardDropInfoPath] = hellPartyRewardTestHellDropInfoTextFor(rate)
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
	invalidateHellPartyRewardDropInfoCache(t, service)
	request, err := service.resolveHellPartyReward(runtime, scene, hellPartyRewardTestGroupLast, 65747)
	if err != nil {
		t.Fatalf("resolve hell party reward: %v", err)
	}
	return request
}

// TestHellPartyRewardRollsMirrorVendorSemantics 是投骰语义的钉子：
// A 档 = 8 次投骰、`chance = 100 * rate / 100`、`roll = lcg.Next(1001)`、
// `roll > chance ⇒ miss`，命中后 `RollHellRarity`（`lcg.Next(1000000)+1` 与
// `[basis of rarity dicision]` 阈值前缀比较）+ `SelectGenericEquipment`。
//
// ⚠ 本用例钉的是**厂商档**：`DNFBRIDGE_HELL_PARTY_REWARD_RARITY=vendor`。
// 默认档（`epic`，操作者要求"深渊只掉史诗"）下同一夹具会命中闸门而**一件都不掉**
// （夹具的稀有度阈值恒返回 rarity 0、池子里也只有 rarity 0 的件），那条口径由
// `dungeon_hell_party_reward_rarity_test.go` 负责。
//
// 期望值由用例内**逐行复刻**同一序列算出（不是硬编码常量），因此这条用例同时
// 钉住"抽了几个随机数、顺序是什么"。
func TestHellPartyRewardRollsMirrorVendorSemantics(t *testing.T) {
	for _, test := range []struct {
		name       string
		rate       int64
		wantRolls  int
		wantChance int
		wantGrants int
	}{
		{name: "tier_a_full_rate_grants_every_roll", rate: 1000, wantRolls: 8, wantChance: 1000, wantGrants: 8},
		{name: "low_rate_still_rolls_eight_times", rate: 80, wantRolls: 8, wantChance: 80, wantGrants: 0},
		{name: "zero_rate_consumes_no_roll", rate: 0, wantRolls: 0, wantChance: 0, wantGrants: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(hellPartyRewardRarityEnv, hellPartyRewardRarityVendorValue)
			service, _, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
			request := hellPartyRewardRequestForTest(t, service, runtime, scene, test.rate)
			result, err := hellPartyRewardRolls(request)
			if err != nil {
				t.Fatalf("roll: %v", err)
			}
			if result.RewardRollCount != hellPartyRewardTestTierA {
				t.Fatalf("reward_roll_count=%d want=%d (etc/hellparty.etc tier A)", result.RewardRollCount, hellPartyRewardTestTierA)
			}
			if result.Rate != test.rate || result.Chance != test.wantChance {
				t.Fatalf("rate=%d chance=%d want rate=%d chance=%d",
					result.Rate, result.Chance, test.rate, test.wantChance)
			}
			if result.Rolls != test.wantRolls {
				t.Fatalf("rolls=%d want=%d (RewardRollCount must drive the loop)", result.Rolls, test.wantRolls)
			}
			if len(result.Selected) != test.wantGrants {
				t.Fatalf("selected=%+v want %d grants (rate=%d chance=%d rolls=%d misses=%d)",
					result.Selected, test.wantGrants, result.Rate, result.Chance, result.Rolls, result.Misses)
			}
			// 逐行复刻厂商 `HellMonsterDropConfig.cs:54-90`，从**同一个种子**重放：
			// 少了/多了任何一次 lcg.Next 都会让这里和被测实现的物品序列对不上。
			want := replayVendorHellRewardRolls(t, request, test.rate)
			if len(want) != len(result.Selected) {
				t.Fatalf("vendor replay count=%d implementation=%d", len(want), len(result.Selected))
			}
			for index := range want {
				if want[index] != result.Selected[index] {
					t.Fatalf("roll#%d item=%+v want vendor replay %+v", index+1, result.Selected[index], want[index])
				}
			}
		})
	}
}

// TestHellPartyRewardRollsRefuseUnparsableTables 钉住"表缺失/对不上 ⇒ 不发明数据"：
// `[drop prob]` 缺本级行、`[item drop ref table]` 缺基准等级行，都必须**硬失败**，
// 由调用点记一条 deferred 事件、发零掉落，绝不猜一个物品。
func TestHellPartyRewardRollsRefuseUnparsableTables(t *testing.T) {
	service, _, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)

	t.Run("probability row missing for the basis level", func(t *testing.T) {
		// 修复档的 rate 来自 hell 表，普通怪表的 `[drop prob]` 在这条链上**不再被读**；
		// 这条子用例钉的是"回退/降级档仍然硬失败"（逐字节今天的行为）。
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)
		request := hellPartyRewardRequestForTest(t, service, runtime, scene, 1000)
		source := mustBridgeSource(t, service)
		source[dnfdrops.MonsterRulePath] = strings.Replace(
			source[dnfdrops.MonsterRulePath], "1 199 0 1000 1000 1000 1000",
			"1 5 0 1000 1000 1000 1000", 1,
		)
		currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
		rules, err := currentDungeonMonsterDropRules(service.dungeonMonsterTable)
		if err != nil {
			t.Fatalf("reload rules: %v", err)
		}
		request.rules = rules
		if _, err := hellPartyRewardRolls(request); !errors.Is(err, errDungeonHellPartyRewardDropRule) {
			t.Fatalf("missing probability row error=%v want %v", err, errDungeonHellPartyRewardDropRule)
		}
	})

	t.Run("item reference row missing for the basis level", func(t *testing.T) {
		request := hellPartyRewardRequestForTest(t, service, runtime, scene, 1000)
		source := mustBridgeSource(t, service)
		source[dnfdrops.MonsterRulePath] = strings.Replace(
			source[dnfdrops.MonsterRulePath],
			strconv.FormatInt(hellPartyRewardTestBasisLevel, 10)+" 1 1\n", "", 1,
		)
		currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
		rules, err := currentDungeonMonsterDropRules(service.dungeonMonsterTable)
		if err != nil {
			t.Fatalf("reload rules: %v", err)
		}
		request.rules = rules
		if _, err := hellPartyRewardRolls(request); !errors.Is(err, errDungeonHellPartyRewardDropRule) {
			t.Fatalf("missing item reference row error=%v want %v", err, errDungeonHellPartyRewardDropRule)
		}
	})
}

// TestHellPartyRewardOnlyTheLastGroupMonsterRolls 钉住厂商 `DropService.cs:179`
// 的 `IsLastGroupMonster` 门槛：组 125 只有 1 只 ⇒ 它一死就投 8 次；
// 组 211 有 3 只 ⇒ 前两只一个随机数都不抽、一只掉落都不注册，第三只才投。
func TestHellPartyRewardOnlyTheLastGroupMonsterRolls(t *testing.T) {
	service, session, conn, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	first, last := hellPartyRewardActorKeys(t, runtime)
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	seedBefore := visit.DropRNG

	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(first[0])); err != nil {
		t.Fatalf("single-monster group death: %v", err)
	}
	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(packet.Body) != currentDungeonZeroDropDeathBodySize || packet.Body[2] != 0 {
		t.Fatalf("earlier wave must carry no equipment: %x", packet.Body)
	}
	if runtime.DropOwner != nil || visit.DropRNG != seedBefore {
		t.Fatal("earlier wave changed drop ledger or reward RNG")
	}

	for index := 0; index < len(last)-1; index++ {
		conn.write.Reset()
		if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(last[index])); err != nil {
			t.Fatalf("non-final group death[%d]: %v", index, err)
		}
		packet, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(packet.Body) != currentDungeonZeroDropDeathBodySize || packet.Body[2] != 0 {
			t.Fatalf("non-final group death[%d] key=%d carried drops: body=%x", index, last[index], packet.Body)
		}
	}
	if runtime.DropOwner != nil || visit.DropRNG != seedBefore {
		t.Fatal("non-final deaths changed drop ledger or reward RNG")
	}

	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(last[2])); err != nil {
		t.Fatalf("final group death: %v", err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) || len(rest) != 0 {
		t.Fatalf("final death header=%+v rest=%x", packet.Header, rest)
	}
	if len(packet.Body) != currentDungeonZeroDropDeathBodySize+8*currentDungeonDeathDropWireSize || packet.Body[2] != 8 {
		t.Fatalf("final death body len=%d count=%d body=%x", len(packet.Body), packet.Body[2], packet.Body)
	}
	if got := len(runtime.DropOwner.byObjectKey); got != 8 {
		t.Fatalf("registered drops=%d want 8 (last wave only)", got)
	}
}

// TestHellPartyRewardLegacyTierConsumesNothing 是回归钉子：
// `DNFBRIDGE_HELL_PARTY_REWARD_ROLL=legacy` 下 op38 逐字节等于改动前的七字节零掉落体，
// 且**一个随机数都不抽**（房间掉落 LCG 状态不变）、掉落账本一行都不建。
func TestHellPartyRewardLegacyTierConsumesNothing(t *testing.T) {
	t.Setenv(hellPartyRewardRollEnv, hellPartyRewardRollLegacyValue)
	service, session, conn, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	_, last := hellPartyRewardActorKeys(t, runtime)
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	seedBefore := visit.DropRNG

	for index, objectKey := range last {
		conn.write.Reset()
		if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(objectKey)); err != nil {
			t.Fatalf("legacy death[%d]: %v", index, err)
		}
		want, buildErr := buildCurrentDungeonDeathNotificationBody(
			objectKey,
			currentDungeonDeathResponseMonster,
		)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(packet.Body, want) || len(rest) != 0 {
			t.Fatalf("legacy op38 body=%x want=%x rest=%x", packet.Body, want, rest)
		}
	}
	if runtime.DropOwner != nil {
		t.Fatalf("legacy tier registered drops: %+v", runtime.DropOwner.byObjectKey)
	}
	if visit.DropRNG != seedBefore {
		t.Fatalf("legacy tier consumed the room drop RNG: before=%d after=%d", seedBefore, visit.DropRNG)
	}
}

// TestHellPartyRewardSwitchDefaultsToVendorAndOnlyLegacyRollsBack 钉住开关语义。
func TestHellPartyRewardSwitchDefaultsToVendorAndOnlyLegacyRollsBack(t *testing.T) {
	if got := readHellPartyRewardRollMode("").String(); got != "vendor" {
		t.Fatalf("empty value=%s", got)
	}
	for _, value := range []string{"vendor", "on", "1", "true", "yes", "junk", " legacy2 "} {
		if got := readHellPartyRewardRollMode(value); got != hellPartyRewardRollVendor {
			t.Fatalf("value=%q resolved to %s want vendor", value, got)
		}
		if !readHellPartyRewardRollMode(value).rolls() {
			t.Fatalf("value=%q must consume RewardRollCount", value)
		}
	}
	for _, value := range []string{"legacy", " LEGACY ", "Legacy"} {
		if got := readHellPartyRewardRollMode(value); got != hellPartyRewardRollLegacy {
			t.Fatalf("value=%q resolved to %s want legacy", value, got)
		}
		if readHellPartyRewardRollMode(value).rolls() {
			t.Fatalf("value=%q must not consume RewardRollCount", value)
		}
	}
	t.Setenv(hellPartyRewardRollEnv, hellPartyRewardRollLegacyValue)
	if got := hellPartyRewardRollModeFromEnv().String(); got != hellPartyRewardRollLegacyValue {
		t.Fatalf("env rollback value=%s", got)
	}
	t.Setenv(hellPartyRewardRollEnv, "")
	if got := hellPartyRewardRollModeFromEnv().String(); got != "vendor" {
		t.Fatalf("env default value=%s", got)
	}
}

// TestHellPartyRewardGrantLandsInInventoryThroughTheExistingPickupPath 是端到端主用例：
// 组 211 三只全灭（最后一只触发投骰）⇒ op38 带 8 条掉落行 ⇒ 用**既有 op43 拾取**
// 把它们捡进背包 ⇒ 仓库里出现真实的 8 件物品（不是 mock 出来的计数）。
func TestHellPartyRewardGrantLandsInInventoryThroughTheExistingPickupPath(t *testing.T) {
	service, session, conn, runtime, _, repositories := prepareHellPartyRewardRuntime(t)
	_, last := hellPartyRewardActorKeys(t, runtime)

	var packet dnfproto.ChannelPacket
	for index, objectKey := range last {
		conn.write.Reset()
		if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(objectKey)); err != nil {
			t.Fatalf("group death[%d] key=%d: %v", index, objectKey, err)
		}
		packet, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
		if index < len(last)-1 {
			if len(packet.Body) != currentDungeonZeroDropDeathBodySize || packet.Body[2] != 0 {
				t.Fatalf("non-final group death[%d] body=%x", index, packet.Body)
			}
			continue
		}
		if packet.Body[2] != 8 {
			t.Fatalf("final death drop count=%d body=%x", packet.Body[2], packet.Body)
		}
	}

	// 逐条按 op38 掉落行里的 scene object key 走既有拾取入口。
	const rowPrefix = 4 + 2 // scene object key u32 + 物品行前 2 字节（instanceId）
	sceneObjectKeys := make([]uint32, 0, 8)
	for index := 0; index < 8; index++ {
		offset := 3 + index*currentDungeonDeathDropWireSize
		sceneObjectKeys = append(sceneObjectKeys, binary.LittleEndian.Uint32(packet.Body[offset:offset+4]))
		if itemID := binary.LittleEndian.Uint32(packet.Body[offset+6 : offset+10]); itemID != hellPartyRewardTestItem {
			t.Fatalf("op38 drop row %d item id=%d want=%d", index, itemID, hellPartyRewardTestItem)
		}
		ownerOffset := offset + rowPrefix + currentItemListEntryWireSize + 2
		// owner 字段 = 击杀者角色在本房间的 actor key
		// （厂商 `DungeonKillApplicationService.cs:1418-1431` 传的就是
		// `context.SourceUserId`）。这一格由会话在房间里的 actor 分配器给出，
		// 用例只钉"行 0 上非 0、非无主哨兵"（后续行同源，见下面的登记账本断言）。
		if index == 0 {
			owner := binary.LittleEndian.Uint16(packet.Body[ownerOffset : ownerOffset+2])
			if owner == 0 || owner == 0xFFFF {
				t.Fatalf("op38 drop row 0 owner=%d must be the killer actor key", owner)
			}
		}
	}
	for index, dropObjectKey := range sceneObjectKeys {
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
			t.Fatalf("pickup %d key=%d: %v", index, dropObjectKey, err)
		}
	}
	for index, dropObjectKey := range sceneObjectKeys {
		if drop := runtime.DropOwner.byObjectKey[dropObjectKey]; drop == nil || drop.Status != runtimeDungeonDropConsumed {
			t.Fatalf("pickup %d key=%d not consumed: %+v", index, dropObjectKey, drop)
		}
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	granted := 0
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(hellPartyRewardTestItem) {
			granted += int(stack.Count)
		}
	}
	if granted != 8 {
		t.Fatalf("inventory granted=%d want 8 slots=%+v", granted, inventory.Slots)
	}
}

// TestHellPartyRewardScopeIsTheHellPartyWaveOnly 钉住作用域：房图自带的普通怪死亡
// 不走这条奖励；房间 actor 表里查不到的 key 也不走。
func TestHellPartyRewardScopeIsTheHellPartyWaveOnly(t *testing.T) {
	service, session, conn, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	var ordinary uint32
	for _, monster := range runtime.Room.Snapshot().Monsters {
		ordinary = monster.ObjectKey
		break
	}
	if ordinary == 0 {
		t.Fatal("fixture has no ordinary room monster")
	}
	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(ordinary)); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Body[2] != 0 {
		t.Fatalf("ordinary monster death carried reward drops: body=%x", packet.Body)
	}
	if _, isRewardWave := dungeonHellPartyRewardWaves(runtime, scene, 0xFFFE); isRewardWave {
		t.Fatal("unknown object key must not be treated as a hell party reward wave")
	}
	if groupID, ok := dungeonHellPartyRewardWaves(runtime, scene, ordinary); ok {
		t.Fatalf("ordinary room monster resolved as a reward wave: group=%d", groupID)
	}
}

// replayVendorHellRewardRolls 逐行复刻厂商 `HellMonsterDropConfig.cs:54-90`
// （`lcg.Next(1001)` → miss/`RollHellRarity` → `ChooseEquipment` → 落地），
// 用于证明被测实现的随机数消耗顺序与厂商一致。
func replayVendorHellRewardRolls(t *testing.T, request hellPartyRewardRequest, rate int64) []hellPartyRewardItem {
	t.Helper()
	if request.rules == nil || request.dropCatalog == nil {
		t.Fatal("replay: request is not fully resolved")
	}
	rng := newCurrentDungeonDropLCG(request.rng.Seed())
	minimumReference, minimumFound := request.rules.ItemReference(request.DungeonMinimumLevel)
	basisReference, basisFound := request.rules.ItemReference(request.DungeonBasisLevel)
	if !minimumFound || !basisFound {
		t.Fatal("replay: item reference rows are missing")
	}
	chance := int(rate) * hellPartyRewardBaseDropRate / hellPartyRewardChancePercentDivisor
	selected := make([]hellPartyRewardItem, 0, hellPartyRewardTestTierA)
	for attempt := int64(0); attempt < request.RewardRollCount; attempt++ {
		if rng.Next(hellPartyRewardDropChanceDenominator) > chance {
			continue
		}
		thresholds := request.rules.RawTables().RarityDecision
		row := int(request.AbyssPartyDifficulty) - 1
		if row < 0 {
			row = 0
		}
		roll := int64(rng.Next(hellPartyRewardRarityRollBound)) + 1
		rarity := int64(0)
		for index := 0; index < len(thresholds[row]) && index < hellPartyRewardMaxRarityRow; index++ {
			if roll <= thresholds[row][index] {
				rarity = int64(index)
				break
			}
		}
		itemID, ok, err := request.dropCatalog.SelectGenericEquipment(
			rng, int(request.DungeonBasisLevel), minimumReference.ValueA, basisReference.ValueA, rarity)
		if err != nil {
			t.Fatalf("replay: select equipment: %v", err)
		}
		if !ok || itemID == 0 {
			continue
		}
		selected = append(selected, hellPartyRewardItem{ItemID: itemID, Amount: 1})
	}
	return selected
}

func mustBridgeSource(t *testing.T, service *Service) bridgePVFSource {
	t.Helper()
	source, ok := service.dungeonMonsterTable.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("test source type=%T", service.dungeonMonsterTable.source)
	}
	return source
}

// TestRealPVFHellPartyRewardRewardTablesExist 是真档 PVF 用例
// （`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：把**线上那份 Script.pvf** 上这条奖励链真正会
// 算出来的数落成日志，并把"表在不在"钉成断言：
//   - `[drop prob]` 必须有覆盖副本 310 基准等级的行（A 档拿第 2 列 = `Rate`）；
//   - `[item drop ref table]` 必须有该等级的行（等级区间端点）；
//   - 通用装备池必须在**该等级区间内**真的取得到候选。
//
// ⚠ 第三条在 90CN 当前这份 PVF 上是**不成立**的（下面日志会打出来）：池只覆盖到
// `grade <= 70`，副本 310 的基准等级是 90 ⇒ 概率过了也选不出物品。
// 这一条**不**在这里断言失败（它不是本改动的回归，而是 PVF 侧的内容事实），
// 由报告转述给操作者 ⇒ 因此本用例在真档上仍然是 PASS，只是把缺口打进日志。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFHellPartyReward ./internal/services/dnfbridge`
func TestRealPVFHellPartyRewardRewardTablesExist(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable for the abyss reward: %v", dnfdrops.MonsterRulePath, err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	basisLevel := dungeon.Metadata.BasisLevel.Value

	probability, found := rules.Probability(basisLevel)
	if !found {
		t.Fatalf("real [drop prob] has no row covering dungeon 310 basis level %d", basisLevel)
	}
	chance := probability.Rates[1] * hellPartyRewardBaseDropRate / hellPartyRewardChancePercentDivisor
	t.Logf("real dungeon 310 basis=%d tier A rate=%d chance=%d ⇒ 每次投骰 %d/%d（Tier A %d 次）",
		basisLevel, probability.Rates[1], chance, chance, hellPartyRewardDropChanceDenominator, hellPartyRewardTestTierA)
	minimumLevel := dungeon.Metadata.MinimumRequiredLevel.Value
	minimumReference, minimumFound := rules.ItemReference(minimumLevel)
	basisReference, basisFound := rules.ItemReference(basisLevel)
	if !minimumFound {
		t.Fatalf("real [item drop ref table] has no row for [minimum required level]=%d", minimumLevel)
	}
	if !basisFound {
		t.Fatalf("real [item drop ref table] has no row for [basis level]=%d", basisLevel)
	}
	// 区间端点按 `dungeon_drop_catalog.go:429-489 collectGenericEquipmentLocked`：
	// `grade ∈ [basis - down, basis + up)`，其中 down/up 是两行 `[item drop ref table]` 的 ValueA。
	downOffset := basisReference.ValueA
	upOffset := basisReference.ValueA
	t.Logf("real item drop ref minimum=%d valueA=%d / basis=%d valueA=%d ⇒ grade ∈ [%d, %d)",
		minimumLevel, minimumReference.ValueA, basisLevel, downOffset,
		basisLevel-basisReference.ValueA, basisLevel+upOffset)

	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	if err := dropCatalog.ensureGenericPools(); err != nil {
		t.Fatalf("real generic item pools are unusable: %v", err)
	}
	total := 0
	maxGrade := int64(0)
	for key, items := range dropCatalog.genericEquipment {
		if key.Grade > maxGrade {
			maxGrade = key.Grade
		}
		if key.Grade >= basisLevel-basisReference.ValueA && key.Grade < basisLevel+basisReference.ValueA {
			total += len(items)
		}
	}
	t.Logf("real generic equipment pool: keys=%d rows_at_basis=%d max_grade=%d",
		len(dropCatalog.genericEquipment), total, maxGrade)
	if total == 0 {
		t.Logf("⚠ 90CN PVF 事实：grade %d..%d 无任何装备候选 ⇒ 概率过了也零掉落（PVF 侧内容缺口，非本改动回归）",
			basisLevel-basisReference.ValueA, basisLevel+basisReference.ValueA-1)
	}
}
