package dnfbridge

// 本文件钉住"开盒子（op208/op1128）回包后只重发真正变了的容器"这条修法。
//
// 为什么需要它：这两条 opcode 走的是 `packageitem.ownerAppliedMagicBoxResult`
// → `PostActionRefreshSelectedItemContainers`，而 bridge 那条通用路径**无条件回放五个
// 整容器**（list 0/1/2/7/38）。现场量到的代价是每次开盒 5 个列表 / 19,175 B
// （`changlog.md:1103-1106`），其中真正会变的只有主背包（盒子被扣的那一格）和奖励落点。
//
// 用例把三件事分开钉住：
//  1. **默认档 current = 逐字节今天**：同一次会话、同一份库状态，先跑默认档拿到 5 个
//     容器帧，作为后面所有比对与字节账的基线；
//  2. **changed_only = 只发变了的容器**，且这些容器的**整帧字节与默认档完全相同**
//     （同一个 builder、同一个信封、同一个规范顺序）；
//  3. **奖励落在别的容器时那个容器照样发**（[feed] 宠物消耗品 → list 7），以及
//     **非开盒路径 / 记录不完整时仍然走全量档**（绝不猜、绝不少发）。

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/config"
)

// 主背包（列表 0）是每一次开盒的源容器：魔盒请求只接受该列表类型
// （packageitem/request.go:135-140）。
const currentMainInventoryListTypeForTest = dnfrepo.MainInventoryListType

// 两个奖励模板的容器判据（真源 = 运行时 PVF 的 `[stackable type]` 家族，
// 见 item_target_container.go 的家族 → 段表）：
//
//	2600014 `[consumption]` → list 0（主背包消耗品页）
//	24      `[feed]`        → list 7 189..238（宠物消耗品段，已有测试钉住）
const (
	boxOpenRewardTestMainItemID = int64(2600014)
	boxOpenRewardTestPetItemID  = int64(24)
)

// boxOpenRewardResolverFixture 复刻 bridge 在 magic_box_pvf.go 里交给 packageitem 的
// 奖励模板解析器（同一个 `resolveCurrentMagicBoxRewardItem`，同一份 PVF 家族判定）。
func boxOpenRewardResolverFixture(tb testing.TB) alignedcmd.MagicBoxRewardItemResolver {
	tb.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":                       "",
		"equipment/equipment.lst":                   "",
		"stackable/stackable.lst":                   "24 `cash/creature/creature_food.stk`\n2600014 `potion/ptn.stk`\n",
		"stackable/potion/ptn.stk":                  "[name] `Potion`\n[stackable type] `[consumption]`\n[stack limit] 100\n",
		"stackable/cash/creature/creature_food.stk": "[name] `Creature Food`\n[stackable type] `[feed]`\n[stack limit] 1000\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		tb.Fatal(err)
	}
	return func(itemID int64) (alignedcmd.MagicBoxRewardItem, error) {
		return resolveCurrentMagicBoxRewardItem(catalog, source, itemID)
	}
}

// boxOpenRefreshRun 是一次"开盒 ACK + 回包后容器刷新"写出的字节账。
type boxOpenRefreshRun struct {
	packets   int
	bytes     int
	ackMsgID  uint16
	ackBody   []byte
	listTypes []byte
	frames    map[byte][]byte
}

// sendBoxOpenPostAction 走生产链路：domain 给的 Result（class1 ACK +
// PostActionRefreshSelectedItemContainers，与 packageitem/handler.go:191-208 同形）交给
// bridge 的 sendAlignedUpperResponses，然后把写出的字节拆成 ACK + 若干 class0/op13 容器帧。
func sendBoxOpenPostAction(t *testing.T, fixture *refreshCostFixture, operation string, ackMsgID uint16) boxOpenRefreshRun {
	t.Helper()
	fixture.buffer.write.Reset()
	result := alignedcmd.Result{
		Operation: operation,
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          ackMsgID,
			Body:           []byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("sendAlignedUpperResponses(%s) error = %v", operation, err)
	}

	raw := append([]byte(nil), fixture.buffer.write.Bytes()...)
	run := boxOpenRefreshRun{bytes: len(raw), frames: map[byte][]byte{}}
	if len(raw) == 0 {
		t.Fatalf("operation %s emitted no bytes", operation)
	}
	ack, rest := splitGameServerUpperPacket(t, raw)
	run.packets = 1
	run.ackMsgID = ack.Header.MsgID
	run.ackBody = append([]byte(nil), ack.Body...)
	for len(rest) > 0 {
		// splitCurrentSceneItemListPacket 同时校验 class0/op13 与固定头尾部，
		// 所以这里只会拆出真正的整容器帧。
		packet, remaining := splitCurrentSceneItemListPacket(t, rest)
		frame := append([]byte(nil), rest[:len(rest)-len(remaining)]...)
		if len(packet.Body) == 0 {
			t.Fatalf("container frame has an empty body: %x", frame)
		}
		listType := packet.Body[0]
		if _, duplicated := run.frames[listType]; duplicated {
			t.Fatalf("list %d was sent twice in one refresh", listType)
		}
		run.frames[listType] = frame
		run.listTypes = append(run.listTypes, listType)
		run.packets++
		rest = remaining
	}
	return run
}

// armBoxOpenRecordWithRewards 复刻 game_aligned.go 在把 op208/op1128 交给 packageitem
// 之前做的那一步（armBoxOpenContainerRecord = 复位记录 + 包装奖励模板解析器），并按
// "模块只对实际发放的奖励调用解析器"这一契约喂进本次开出的奖励模板。
func armBoxOpenRecordWithRewards(t *testing.T, fixture *refreshCostFixture, rewardItemIDs ...int64) {
	t.Helper()
	recorded := fixture.service.armBoxOpenContainerRecord(fixture.session, boxOpenRewardResolverFixture(t))
	for _, itemID := range rewardItemIDs {
		item, err := recorded(itemID)
		if err != nil {
			t.Fatalf("resolve box-open reward %d: %v", itemID, err)
		}
		if item.ItemID != itemID {
			t.Fatalf("reward %d resolved as %d", itemID, item.ItemID)
		}
	}
}

func requireBoxOpenListTypes(t *testing.T, run boxOpenRefreshRun, want []byte) {
	t.Helper()
	if !bytes.Equal(run.listTypes, want) {
		t.Fatalf("container list types = %v, want %v (packets=%d bytes=%d)",
			run.listTypes, want, run.packets, run.bytes)
	}
}

// boxOpenFrameSizes 只用于日志：把这次刷新各容器帧的字节数按发送顺序列出来。
func boxOpenFrameSizes(run boxOpenRefreshRun) string {
	parts := make([]string, 0, len(run.listTypes))
	for _, listType := range run.listTypes {
		parts = append(parts, fmt.Sprintf("list%d=%dB", listType, len(run.frames[listType])))
	}
	return strings.Join(parts, " ")
}

func requireSameBoxOpenFrame(t *testing.T, listType byte, baseline, got boxOpenRefreshRun) {
	t.Helper()
	wantFrame, ok := baseline.frames[listType]
	if !ok {
		t.Fatalf("baseline never sent list %d (lists=%v)", listType, baseline.listTypes)
	}
	gotFrame, ok := got.frames[listType]
	if !ok {
		t.Fatalf("list %d is missing after the switch (lists=%v)", listType, got.listTypes)
	}
	if !bytes.Equal(wantFrame, gotFrame) {
		t.Fatalf("list %d frame is not byte-identical:\n baseline=%x\n got     =%x",
			listType, wantFrame, gotFrame)
	}
}

// TestBoxOpenContainerRefreshChangedOnlyKeepsChangedFrameByteIdenticalAndCutsBytes
// 是主用例：默认档（= 今天的线格式）5 个整容器 → changed_only 只发主背包那一个，
// 且那一个的整帧字节与默认档逐字节相同。同一次会话、同一份库状态，唯一变量是开关。
func TestBoxOpenContainerRefreshChangedOnlyKeepsChangedFrameByteIdenticalAndCutsBytes(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	baseline := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))
	requireBoxOpenListTypes(t, baseline, currentSelectedItemListTypes)
	if baseline.ackMsgID != uint16(dnfenum.CmdPacketUseRandomboxItem) {
		t.Fatalf("baseline first packet msg_id = %d, want the box ACK", baseline.ackMsgID)
	}
	// 默认档的字节账 = 分诊时那份夹具的数字：ACK + 五个整容器 = 6 包 / 11,333 B
	// （`changlog.md:1103-1105` 的"5 包 / 11,333 B 夹具"）。这条断言是"默认档没被
	// 这次改动动过"的钉子：任何让 current 档变形（少发/多发/包体变化）的改动都会在这里红。
	const baselineWireBytes = 11333
	if baseline.bytes != baselineWireBytes || baseline.packets != len(currentSelectedItemListTypes)+1 {
		t.Fatalf("default five-container replay = %d packets / %d bytes, want %d / %d",
			baseline.packets, baseline.bytes, len(currentSelectedItemListTypes)+1, baselineWireBytes)
	}
	t.Logf("baseline (current) container frames: %s (total %d bytes / %d packets)",
		boxOpenFrameSizes(baseline), baseline.bytes, baseline.packets)

	// 开关打开 + 装好"本次开盒写到了哪些容器"（奖励 2600014 落在主背包）。
	fixture.service.options.boxOpenContainerRefreshChangedOnly = true
	armBoxOpenRecordWithRewards(t, fixture, boxOpenRewardTestMainItemID)
	changed := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))

	requireBoxOpenListTypes(t, changed, []byte{currentMainInventoryListTypeForTest})
	// ACK 的**包体**必须逐字节不变（帧头里有每会话/每包的传输状态，不参与比对）。
	if changed.ackMsgID != baseline.ackMsgID || !bytes.Equal(changed.ackBody, baseline.ackBody) {
		t.Fatalf("box ACK body changed with the container switch:\n baseline=%x\n got     =%x",
			baseline.ackBody, changed.ackBody)
	}
	requireSameBoxOpenFrame(t, currentMainInventoryListTypeForTest, baseline, changed)
	if changed.bytes >= baseline.bytes {
		t.Fatalf("changed_only bytes=%d must be below the default five-container replay=%d",
			changed.bytes, baseline.bytes)
	}
	t.Logf("box open (reward lands in list 0): current=%d packets/%d bytes %v -> changed_only=%d packets/%d bytes %v (saved %d bytes)",
		baseline.packets, baseline.bytes, baseline.listTypes,
		changed.packets, changed.bytes, changed.listTypes,
		baseline.bytes-changed.bytes)
}

// TestBoxOpenContainerRefreshChangedOnlyKeepsSecondContainerForRewardInAnotherList
// 是"奖励落在别的容器"那一格：奖励 24 是 `[feed]` 宠物消耗品，永远落 list 7
// 189..238，所以这次必须**两个**容器都发（主背包 + 宠物栏），且两帧都与默认档逐字节相同。
func TestBoxOpenContainerRefreshChangedOnlyKeepsSecondContainerForRewardInAnotherList(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	baseline := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))
	requireBoxOpenListTypes(t, baseline, currentSelectedItemListTypes)

	fixture.service.options.boxOpenContainerRefreshChangedOnly = true
	armBoxOpenRecordWithRewards(t, fixture, boxOpenRewardTestPetItemID)
	changed := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))

	want := []byte{currentMainInventoryListTypeForTest, currentPetInventoryListType}
	requireBoxOpenListTypes(t, changed, want)
	requireSameBoxOpenFrame(t, currentMainInventoryListTypeForTest, baseline, changed)
	requireSameBoxOpenFrame(t, currentPetInventoryListType, baseline, changed)
	if changed.bytes >= baseline.bytes {
		t.Fatalf("changed_only bytes=%d must be below the default five-container replay=%d",
			changed.bytes, baseline.bytes)
	}
	t.Logf("box open (reward lands in list 7): current=%d packets/%d bytes %v -> changed_only=%d packets/%d bytes %v",
		baseline.packets, baseline.bytes, baseline.listTypes,
		changed.packets, changed.bytes, changed.listTypes)
}

// TestBoxOpenContainerRefreshIsGatedToBoxOpenOperationsAndFallsBackWithoutRecord
// 钉住两条安全边界：
//  1. 开关只对 op208/op1128（`use_randombox_item` / `use_randombox_item_expand`）生效 ——
//     同一个 PostAction 走仓库/移动/技能等其它 operation 时仍然回放五个整容器
//     （op160 礼盒不在名单里：它走 bridge 原生路径，本来就只发 changed lists）；
//  2. changed_only 但**没有本次开盒的记录**（解析器没被调用过）时必须回落全量档，
//     绝不按猜测少发容器。
func TestBoxOpenContainerRefreshIsGatedToBoxOpenOperationsAndFallsBackWithoutRecord(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	fixture.service.options.boxOpenContainerRefreshChangedOnly = true

	// ① 记录不完整 ⇒ 全量档。
	if fixture.session.boxOpenRewardRecorded {
		t.Fatal("the reward-container record must start empty")
	}
	noRecord := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))
	requireBoxOpenListTypes(t, noRecord, currentSelectedItemListTypes)
	t.Logf("fallback without a record: packets=%d bytes=%d %v", noRecord.packets, noRecord.bytes, noRecord.listTypes)

	// ② 非开盒 operation ⇒ 即使有记录也仍然全量档。
	armBoxOpenRecordWithRewards(t, fixture, boxOpenRewardTestMainItemID)
	nonBox := sendBoxOpenPostAction(t, fixture, "move_itemspace", uint16(dnfenum.CmdPacketMoveItemspace))
	requireBoxOpenListTypes(t, nonBox, currentSelectedItemListTypes)

	// ③ 连开（op1128）同样在名单里 ⇒ 只发变了的容器。
	armBoxOpenRecordWithRewards(t, fixture, boxOpenRewardTestMainItemID)
	expand := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandomboxExpand, uint16(dnfenum.CmdPacketUseRandomboxItemExpand))
	requireBoxOpenListTypes(t, expand, []byte{currentMainInventoryListTypeForTest})

	t.Logf("gates: no_record=%d packets %v, move_itemspace=%d packets %v, op1128=%d packets %v",
		noRecord.packets, noRecord.listTypes, nonBox.packets, nonBox.listTypes, expand.packets, expand.listTypes)
}

// TestBoxOpenContainerRefreshModeDefaultsToCurrentAndFollowsEnv 钉住开关本身：
// 未设置/空 = current（逐字节今天），changed_only 才启用；非法值回退 current。
func TestBoxOpenContainerRefreshModeDefaultsToCurrentAndFollowsEnv(t *testing.T) {
	for _, value := range []string{"changed_only", " changed_only ", "CHANGED_ONLY", "changed", "minimal", "on", "1", "true"} {
		if got := normalizeBoxOpenContainerRefreshMode(value); got != boxOpenContainerRefreshChangedOnly {
			t.Fatalf("normalize(%q) = %q, want %q", value, got, boxOpenContainerRefreshChangedOnly)
		}
	}
	for _, value := range []string{"", "current", "off", "0", "false", "full", "garbage"} {
		if got := normalizeBoxOpenContainerRefreshMode(value); got != boxOpenContainerRefreshCurrent {
			t.Fatalf("normalize(%q) = %q, want %q (the default must stay today's wire behaviour)", value, got, boxOpenContainerRefreshCurrent)
		}
	}

	t.Setenv(boxOpenContainerRefreshEnv, "")
	if optionsFromConfig(config.ServiceConfig{}).boxOpenContainerRefreshChangedOnly {
		t.Fatalf("%s unset must keep the default five-container replay", boxOpenContainerRefreshEnv)
	}
	service := &Service{options: optionsFromConfig(config.ServiceConfig{})}
	if got := service.boxOpenContainerRefreshMode(); got != boxOpenContainerRefreshCurrent {
		t.Fatalf("startup mode = %q, want %q", got, boxOpenContainerRefreshCurrent)
	}
	t.Setenv(boxOpenContainerRefreshEnv, "changed_only")
	if !optionsFromConfig(config.ServiceConfig{}).boxOpenContainerRefreshChangedOnly {
		t.Fatalf("%s=changed_only did not enable the changed-container refresh", boxOpenContainerRefreshEnv)
	}
	service = &Service{options: optionsFromConfig(config.ServiceConfig{})}
	if got := service.boxOpenContainerRefreshMode(); got != boxOpenContainerRefreshChangedOnly {
		t.Fatalf("startup mode = %q, want %q", got, boxOpenContainerRefreshChangedOnly)
	}
	// 一键回退。
	t.Setenv(boxOpenContainerRefreshEnv, "current")
	if optionsFromConfig(config.ServiceConfig{}).boxOpenContainerRefreshChangedOnly {
		t.Fatalf("%s=current (rollback) did not restore the five-container replay", boxOpenContainerRefreshEnv)
	}

	for _, operation := range []string{boxOpenOperationRandombox, boxOpenOperationRandomboxExpand} {
		if !isBoxOpenOperation(operation) {
			t.Fatalf("isBoxOpenOperation(%q) = false, want true", operation)
		}
	}
	for _, operation := range []string{"", "use_booster_item", "use_magic_box", "move_itemspace"} {
		if isBoxOpenOperation(operation) {
			t.Fatalf("isBoxOpenOperation(%q) = true, want false", operation)
		}
	}
}

// BenchmarkBoxOpenChangedOnlyPostActionRefresh 是 changed_only 档下"一次开盒 = ACK +
// 真正变了的容器"的服务端发送成本，与 item_refresh_cost_test.go 的
// BenchmarkMagicBoxPostActionRefresh（默认档 = ACK + 5 个整容器，239 µs / 370 KB）同一条
// 路径、同一份夹具，可直接对比（内存仓储、不含 MySQL 往返）。
func BenchmarkBoxOpenChangedOnlyPostActionRefresh(b *testing.B) {
	fixture := newRefreshCostFixture(b)
	fixture.service.options.boxOpenContainerRefreshChangedOnly = true
	// 与 refreshCostFixture.warmUp 同一件事（把"每会话一次"的迁移与首次缓存排除在
	// 测量之外），只是这里要能接受 *testing.B。
	fixture.buffer.write.Reset()
	if _, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType); !ok {
		b.Fatal("warm-up main-inventory build failed")
	}
	if err := fixture.service.sendSelectedCurrentContainerListsWithRefresh(fixture.session, "warm_up", true); err != nil {
		b.Fatal(err)
	}
	fixture.buffer.write.Reset()

	session := fixture.session
	resolver := boxOpenRewardResolverFixture(b)
	result := alignedcmd.Result{
		Operation: boxOpenOperationRandombox,
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketUseRandomboxItem),
			Body:           []byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session.beginBoxOpenContainerRecord()
		recorded := session.recordBoxOpenRewardContainer(resolver)
		if _, err := recorded(boxOpenRewardTestMainItemID); err != nil {
			b.Fatal(err)
		}
		fixture.buffer.write.Reset()
		if err := fixture.service.sendAlignedUpperResponses(session, result); err != nil {
			b.Fatal(err)
		}
	}
}
