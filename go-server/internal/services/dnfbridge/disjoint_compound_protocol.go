package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentDisjointItemRequestSize       = 5
	currentAvatarDisjointRequestMinSize  = 2
	currentAvatarDisjointRequestFullSize = 6
	currentAvatarInventoryListType       = 1
	currentEmblemCompoundMinInputs       = 2
	currentEmblemCompoundMaxInputs       = 5
	currentEmblemCompoundInputSize       = 6
	currentDisjointMaxRewards            = 32

	// 批量分解（客户端"阿拉德分解修理机"多选分解 / 90CN.dll 的 equipment-disjoint
	// batch 路径）在 op26 上是**另一种请求体形状**：
	//
	//	i16 0xFFFF（无单一来源槽）| u8 list=0（主背包）| i16 0xFFFF（-1，无便携分解机）
	//	| u32 context（分解机 NPC 商店 id）| 4 字节批量标签 | u8 件数 | 件数 × u16 槽位
	//
	// 契约证据（HK 实服 packet_log.*.txt 全部 61 条 type=26 RECV：27 条批量 + 34 条
	// 9 字节单件；含已轮转文件，逐条严格解析）：
	//   - 27/27 条批量的 body 长度 == 14 + 2×件数（件数取自尾部件数字节，取样 1..24）；
	//   - 27/27 条的 4 字节标签恒为 42 32 36 01，全库仅出现这 27 次，且**从不出现在
	//     任何非 op26 的封包里**；同一批里 context 有 317/1000 两种取值而标签不变
	//     ⇒ 它不随上下文变化，是"批量请求"的类型标签（ASCII 解读 = 'B' '2' '6' 0x01，
	//     即 Batch / opcode 26 / v1），不是校验位、不是数量、不是掩码；
	//   - 27/27 条的前 9 字节只有两种形态：`ffff 00 ffff 3d010000`（25 条，商店 317）
	//     与 `ffff 00 ffff e8030000`（2 条，1000）；
	//   - 槽位列表按客户端勾选顺序（非升序）：3 件样本 = 49,51,50；4 件样本同时存在
	//     9,11,12,13 与 12,10,11,13 两种顺序；
	//
	//	18B: ffff 00 ffff 3d010000 42323601 02 0900 0a00             → 2 件，槽 9,10
	//	20B: ffff 00 ffff 3d010000 42323601 03 3100 3300 3200        → 3 件，槽 49,51,50
	//	22B: ffff 00 ffff 3d010000 42323601 04 0900 0b00 0c00 0d00   → 4 件，槽 9,11,12,13
	//	62B: ffff 00 ffff 3d010000 42323601 18 …                     → 24 件
	//
	// 交叉验证（2026-09-25 操作者复现）：确认框自述「共4个道具」、界面标题「阿拉德分解
	// 修理机」（= 商店 317）⇒ 件数 4 与 `04` 一致；角色 dnf:10006 / character_id=4 的
	// 主背包当时只剩槽 9/11/12/13 四个可分解装备（10010 布甲上衣、116000011 光剑、
	// 10400 皮甲背心、12400 皮甲护腿），与 `0900 0b00 0c00 0d00` 完全吻合；同会话客户端
	// 补丁 trace 亦自述 `batch queued selected=4 tool=-1 context=317`。
	//
	// 【已废弃的旧读法】上一轮把尾部当成"9 字节 / 72 位掩码、bit i = 槽 i"
	// （`mask_bit_count`/`mask_slots_candidate`）：那条读法对 18B 样本算出"选中 15 位"、
	// 对 22B 样本会算出 72 位掩码，与客户端自述件数、库里槽位都不符，**不得再使用**。
	currentDisjointBatchPrefixSize  = 9
	currentDisjointBatchTagSize     = 4
	currentDisjointBatchCountSize   = 1
	currentDisjointBatchMinItems    = 1
	currentDisjointBatchMaxItems    = 72 // 主背包槽位上限；件数超出即判畸形
	currentDisjointBatchMaxBodySize = currentDisjointBatchPrefixSize + currentDisjointBatchTagSize +
		currentDisjointBatchCountSize + 2*currentDisjointBatchMaxItems
	// currentDisjointBatchHeaderSize = 前缀 + 标签 + 件数字节（不含槽位列表）。分支守卫
	// 用它而不是"最小合法长度"：`件数=0`（14 字节）这类畸形体也走批量分支，从而拿到
	// `batch op26 count=0` 的具名拒绝日志，而不是被当成畸形单件体。
	currentDisjointBatchHeaderSize  = currentDisjointBatchPrefixSize + currentDisjointBatchTagSize + currentDisjointBatchCountSize
	currentDisjointBatchMinBodySize = currentDisjointBatchHeaderSize + 2*currentDisjointBatchMinItems
	currentDisjointBatchHeaderSlot  = int16(-1)
	// currentDisjointBatchAckShapeEnv 是"批量分解成功回包形状"的一键开关，取值写进
	// `game-disjoint-item-batch-committed` 的 `ack_shape=` 字段供复测判读。
	//
	// 2026-09-25 现场判定（操作者复测 + 客户端补丁反汇编 + 同一会话 packet_log）：
	// 上一轮的 `per_item_single_ack`（逐件回单件成功体、不补刷新）**实测无效**——
	// 服务端 4 条 ACK 全部发出（pkt_seq 131..134）且事务已提交，但客户端界面里
	// 4 件装备**一件都没消失**：14 秒后操作者再点一次批量，客户端补丁 trace 仍自述
	// `equipment-disjoint batch queued selected=4`，服务端收到的仍是同一条
	// `04 0900 0b00 0c00 0d00`。⇒ 当前客户端对"一请求换 N 条 ACK"一条都不采纳
	// （不是"只读第一条"）。因此保留 ACK（形状已被实服证明可被解析、不崩）之外，
	// **必须**再用仓库既有的"槽位内容变了"下发把界面拉回权威状态。
	//
	// 三档：
	//   - single_aggregate_ack（**默认**，2026-09-29 起）：**一条** ACK，行 = 整批
	//     产物按 (槽,物品) 合并后的"本次获得"总量；槽位刷新照旧（op14 受影响行）。
	//     现场依据：操作者报「批量分解完成后发放的产出是一件一件发过来的，需要统一
	//     在一个框，不然分解多样物品会有很多次弹窗」——`per_item_single_ack_refresh`
	//     对 10 件回 10 条 op26 ACK（日志 `ack_count=10`），客户端按每条 ACK 各弹一次
	//     获得框。合并成一条（行序 = 槽升序）即"一个框列出全部产物"。
	//     ACK 的 source slot 取本批**第一件**（它确实被这次批量消耗了，是请求里真实
	//     存在过的槽位；批量请求自己的 0xFFFF 只是"无单一来源"的标记，回显它没有
	//     任何已证实的读法）。
	//   - per_item_single_ack_refresh：N 条单件 ACK + 受影响槽位 op14 行
	//     （2026-09-25..29 的线上形状，**逐字节回退档**）。
	//   - per_item_single_ack：只回 N 条单件 ACK，逐字节等于更早一轮的线上行为。
	//   - one_ack_container_refresh：只回第一件的 ACK + 主背包 list 0 整容器下发（op13），
	//     留给"单行刷新仍然不吃"时的兜底方案（登录引导同一条容器包语法）。
	currentDisjointBatchAckShapeEnv                    = "DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE"
	currentDisjointBatchAckShapeSingleAggregate        = "single_aggregate_ack"
	currentDisjointBatchAckShapePerItemSingleAck       = "per_item_single_ack"
	currentDisjointBatchAckShapePerItemSingleAckFresh  = "per_item_single_ack_refresh"
	currentDisjointBatchAckShapeOneAckContainerRefresh = "one_ack_container_refresh"
)

// currentDisjointBatchAckShape 读 DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE 并归一化：
// 未知、空、拼错一律回到默认新方案（fail-safe 到"会刷新界面"的那一档，而不是
// 回到已知无效的旧形状）；只有显式写出回退值才回退。
func currentDisjointBatchAckShape() string {
	shape := strings.ToLower(strings.TrimSpace(envString(currentDisjointBatchAckShapeEnv, "")))
	switch shape {
	case currentDisjointBatchAckShapePerItemSingleAck,
		currentDisjointBatchAckShapePerItemSingleAckFresh,
		currentDisjointBatchAckShapeOneAckContainerRefresh:
		return shape
	default:
		return currentDisjointBatchAckShapeSingleAggregate
	}
}

// currentDisjointBatchAckShapeAckCount 是这一档要回几条 op26 ACK。
// single_aggregate_ack 与 one_ack_container_refresh 都收成一条。
func currentDisjointBatchAckShapeAckCount(itemCount int, shape string) int {
	if itemCount > 1 && (shape == currentDisjointBatchAckShapeSingleAggregate ||
		shape == currentDisjointBatchAckShapeOneAckContainerRefresh) {
		return 1
	}
	return itemCount
}

// currentDisjointBatchAckShapeUsesSlotRefresh 判"是否补 op14 受影响槽位行"。
// 合并档**保留**刷新：ACK 只负责那一个获得框，界面状态仍由权威槽位行拉回。
func currentDisjointBatchAckShapeUsesSlotRefresh(shape string) bool {
	return shape == currentDisjointBatchAckShapePerItemSingleAckFresh ||
		shape == currentDisjointBatchAckShapeSingleAggregate
}

// currentDisjointBatchAckShapeUsesContainerRefresh 判"是否补 op13 主背包整容器下发"。
func currentDisjointBatchAckShapeUsesContainerRefresh(shape string) bool {
	return shape == currentDisjointBatchAckShapeOneAckContainerRefresh
}

// currentDisjointBatchRefreshShapeEnv 是"批量分解 op14 槽位刷新行覆盖哪些槽"的一键开关。
//
// 2026-09-29 现场：`single_aggregate_ack` 已上线（`ack_count=1`，一个框），但运维报
// 「弹一个窗口但是**里面的无色晶体一次又一次叠加** 要写成**一次性发放**」。
// 逐帧取证（两条批量提交，226911/236926 区段）证明**服务端没有任何重复行**：
//
//	op26 ACK body（27B）= 01 | 09 00 | 00 | ff ff | 02
//	                     | 66 01 dd0b0000 d4790000   (358, 3037, 31188)
//	                     | 6d 01 2c009b00 18000000   (365, 10158124, 24)
//	op14 body（3097B）= 00 | 1a 00 | 0x77×26 行，其中**只有一行**是 3037@358
//	                     （绝对量 122962 = 91774 + 31188，与 DB 终态逐字相符）
//
// 也就是说：**整批回包里带 3037 的帧有两帧**（ACK 里的 `granted`、op14 里的绝对量），
// 而"单件分解"这条被运维认定为正确的路径**只发一帧**：
// `commitCurrentDisjointItem`（`gameplay_module_equipment_disjoint.go:255-263`）
// 只发 op26 ACK，**一个字节的 op14 都不发**。
//
// 因此默认档把批量回包**收敛到与单件路径同构**：ACK 承载"本次获得"（一次性发放的
// 那一行），op14 只负责把**被消耗掉的来源槽**清空 —— 这正是上一轮实验证明必需的
// 那部分（`per_item_single_ack` 不补刷新时界面上一件都不消失），而产物槽的权威新量
// 已经在 ACK 的那一行里给过了（单件路径就是这么工作的）。
// ⇒ 整批回包里带 3037 的帧从 2 帧降到 1 帧。
//
// 两档：
//   - source_only（**默认**，2026-09-29 起）：op14 只含本批**来源槽**行（内容仍从
//     仓储终态重读，所以"产物落回刚腾空的那一格"依然是产物行）。
//   - legacy：op14 先产物槽、后来源槽（含产物槽行），= 2026-09-29 线上形状，
//     **逐字节回退档**。
//
// 一键回退需要**同时**写两个开关（ACK 形状 + 刷新形状）才能逐字节回到 09-29 线上；
// 只回退刷新形状时 ACK 仍是那一条聚合体，见报告的开关表。
const (
	currentDisjointBatchRefreshShapeEnv        = "DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE"
	currentDisjointBatchRefreshShapeSourceOnly = "source_only"
	currentDisjointBatchRefreshShapeLegacy     = "legacy"
)

// currentDisjointBatchRefreshShape 读 DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE 并归一化：
// 未知、空、拼错一律回到默认新方案（产物只由 ACK 那一行承载）；只有显式写出回退值
// 才带上产物槽行。口径与 currentDisjointBatchAckShape 一致。
func currentDisjointBatchRefreshShape() string {
	switch strings.ToLower(strings.TrimSpace(envString(currentDisjointBatchRefreshShapeEnv, ""))) {
	case currentDisjointBatchRefreshShapeLegacy, "off", "0", "false", "no", "disable", "disabled", "none",
		"all_rows", "reward_and_source", "reward_slots":
		return currentDisjointBatchRefreshShapeLegacy
	default:
		return currentDisjointBatchRefreshShapeSourceOnly
	}
}

// currentDisjointBatchRefreshShapeUsesRewardSlots 判"op14 刷新里要不要带产物槽行"。
func currentDisjointBatchRefreshShapeUsesRewardSlots(shape string) bool {
	return shape == currentDisjointBatchRefreshShapeLegacy
}

// currentDisjointBatchTag 是批量请求体里固定的 4 字节类型标签（现场 27/27 恒定，
// 见上面的契约证据）。任何其它取值都判为畸形请求，回有界失败 19、零消耗。
var currentDisjointBatchTag = [currentDisjointBatchTagSize]byte{0x42, 0x32, 0x36, 0x01}

// currentDisjointBatchRequest 是批量分解请求（op26 紧凑槽位列表体）的解码结果。
type currentDisjointBatchRequest struct {
	ToolSlot int16
	Context  uint32
	// Slots 是客户端按勾选顺序列出的主背包槽位，逐字节等于请求体尾部
	// `件数 × u16`。它参与消耗/发放决策（不是日志候选）。
	Slots []int16
}

// SlotListText 把槽位列表渲染成日志字段（`9,11,12,13`）。
func (r currentDisjointBatchRequest) SlotListText() string {
	if len(r.Slots) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Slots))
	for _, slot := range r.Slots {
		parts = append(parts, strconv.FormatInt(int64(slot), 10))
	}
	return strings.Join(parts, ",")
}

var (
	errCurrentDisjointRequestInvalid = errors.New("dnf current disjoint request is invalid")
	errCurrentDisjointUnavailable    = errors.New("dnf current disjoint source or PVF mapping is unavailable")
	errCurrentDisjointSourceInvalid  = errors.New("dnf current disjoint source item is invalid")
	errCurrentDisjointRewardInvalid  = errors.New("dnf current disjoint reward mapping is invalid")
)

// 分解/合成的结果码。4 是当前客户端唯一渲染成
// "物品栏空间不足，无法获得分解物品。请整理物品快捷栏、材料栏或副职材料栏后再试。"
// 的失败码（86JP DisjointItemResult.ErrorInventoryFull = 0x04）；其余拒收
// （请求体畸形、目标槽非法、来源不可分解、PVF 映射缺失、投影失败）必须用通用失败码
// （86JP ErrorInvalidRequest/ErrorInvalidTarget = 0x13，与本仓库副职业发放路径的
// 19 约定一致），否则"来源不合法"会被玩家看到成"背包空间不足"。
const (
	currentDisjointFailureInventoryFull byte = 4
	currentDisjointFailureInvalid       byte = 19
)

// currentDisjointFailureCode 只把"目标段真的放不下"翻译成 4，其它错误一律 19。
// 目标段是否放得下由 grantCurrentDisjointRewards -> grantCurrentCeraShopProduct ->
// PlacePickupItem 按运行时 PVF 的段区间逐槽判定（放不下时返回
// errDungeonPickupInventoryFull），这里不再另造第二套空间判定。
func currentDisjointFailureCode(err error) byte {
	if errors.Is(err, errDungeonPickupInventoryFull) {
		return currentDisjointFailureInventoryFull
	}
	return currentDisjointFailureInvalid
}

// currentDisjointItemRequest is the exact semantic body written by the
// current client's op26 sender: target slot, item-space, portable-disjoint
// slot, and (on some UI paths) a u32 context.  The reply reader consumes the
// first three fields again before it reads result rows.
type currentDisjointItemRequest struct {
	SourceSlot int16
	ListType   byte
	ToolSlot   int16
	Context    uint32
}

type currentAvatarDisjointRequest struct {
	SourceSlot     int16
	ExpectedItemID uint32
}

type currentEmblemCompoundInput struct {
	ItemID uint32
	Slot   int16
}

type currentEmblemCompoundRequest struct {
	Inputs []currentEmblemCompoundInput
}

type currentDisjointReward struct {
	ItemID uint32
	Count  uint32
}

type currentDisjointRewardSlot struct {
	Slot    int16
	ItemID  uint32
	Count   uint32 // final stack amount, which is what the current readers apply.
	Granted uint32
}

type currentDisjointResult struct {
	Rewards     []currentDisjointRewardSlot
	Updates     []currentItemListEntry
	GoldAfter   int64
	GoldChanged bool
}

// currentDisjointBatchItem 是批量分解里单件的产物行（逐件回包与日志用）。
type currentDisjointBatchItem struct {
	Slot    int16
	Rewards []currentDisjointRewardSlot
}

// currentDisjointBatchResult 是一次批量分解的逐件产物，顺序 = 请求里的槽位顺序。
type currentDisjointBatchResult struct {
	Items       []currentDisjointBatchItem
	GoldAfter   int64
	GoldChanged bool
}

// currentDisjointSlotError 把"哪一件不合法"钉进错误里，使整批拒绝能报出 offending
// slot（`game-disjoint-item-batch-rejected reason=… slot=…`）。整批拒绝时事务不提交，
// 因此这个错误同时也是"零消耗"的保证。
type currentDisjointSlotError struct {
	Slot int16
	Err  error
}

func (e currentDisjointSlotError) Error() string {
	return fmt.Sprintf("slot=%d: %v", e.Slot, e.Err)
}

func (e currentDisjointSlotError) Unwrap() error { return e.Err }

// currentDisjointErrorSlot 取出错误里携带的槽位；没有则返回 -1。
func currentDisjointErrorSlot(err error) int16 {
	var slotErr currentDisjointSlotError
	if errors.As(err, &slotErr) {
		return slotErr.Slot
	}
	return -1
}

type currentDisjointAdditionalConst struct {
	GreatDivisor  float64
	NormalDivisor float64
	GreatChance   float64
}

type currentDisjointExpand struct {
	Enabled      bool
	ItemID       uint32
	LevelDivisor float64
	GreatChance  float64
	NormalChance float64
}

type currentDisjointPVFConfig struct {
	CubeItemID                  uint32
	CubeBase                    float64
	CubeMultipliers             []float64
	Additional                  [][]uint32
	AdditionalConsts            []currentDisjointAdditionalConst
	Expands                     []currentDisjointExpand
	AvatarByJob                 map[string][]uint32             // legacy disjoint.etc [avatar disjoint info], kept for config validation
	AvatarEmblemTables          []currentAvatarEmblemGradeTable // etc/avatardisjoint/emblemlistinfo_<grade>.etc
	EmblemBoosters              map[[2]int]uint32               // (emblem grade,input count) -> booster item
	CustomAbilityDisableLoaded  bool                            // false means individualcustominfo.etc was not read successfully
	DisabledByCustomAbilityType map[string]map[uint32]struct{}  // individualcustominfo.etc [disjoint disable item]
}

// currentAvatarEmblemGradeTable is one grade's avatar disjoint reward table
// (86JP AvatarDisjointConfigProvider grammar): each [result info] section is
// one pool whose first value is the pick count, followed by
// item/weight/count/special 4-tuples.
type currentAvatarEmblemGradeTable struct {
	Pools []currentAvatarEmblemPool
}

type currentAvatarEmblemPool struct {
	PickCount int
	Rewards   []currentAvatarEmblemReward
}

type currentAvatarEmblemReward struct {
	ItemID  uint32
	Weight  uint32
	Count   uint32
	Special bool
}

func parseCurrentDisjointItemRequest(body []byte) (currentDisjointItemRequest, error) {
	if len(body) != currentDisjointItemRequestSize && len(body) != currentDisjointItemRequestSize+4 {
		return currentDisjointItemRequest{}, fmt.Errorf("%w: op26 body=%d", errCurrentDisjointRequestInvalid, len(body))
	}
	request := currentDisjointItemRequest{
		SourceSlot: int16(binary.LittleEndian.Uint16(body[0:2])),
		ListType:   body[2],
		ToolSlot:   int16(binary.LittleEndian.Uint16(body[3:5])),
	}
	if len(body) == currentDisjointItemRequestSize+4 {
		request.Context = binary.LittleEndian.Uint32(body[5:9])
	}
	if request.ListType != dnfrepo.MainInventoryListType || request.SourceSlot <= 0 ||
		dnfrepo.IsAccountSharedInventorySlot(request.ListType, request.SourceSlot) {
		return currentDisjointItemRequest{}, fmt.Errorf("%w: op26 list=%d slot=%d", errCurrentDisjointRequestInvalid, request.ListType, request.SourceSlot)
	}
	return request, nil
}

// currentDisjointBodyIsBatchShape 只做"这是不是批量分支"的形状判定：首字段是
// 0xFFFF 批量标记 + 主背包 list。它是分支守卫，不是合法性承诺——严格解码（长度
// 等式、固定标签、件数区间、槽位本身）由 decodeCurrentDisjointBatchRequest 负责，
// 任何不合规都回有界失败 19 且零消耗。
func currentDisjointBodyIsBatchShape(body []byte) bool {
	if len(body) < currentDisjointBatchHeaderSize || len(body) > currentDisjointBatchMaxBodySize {
		return false
	}
	return int16(binary.LittleEndian.Uint16(body[0:2])) == currentDisjointBatchHeaderSlot &&
		body[2] == dnfrepo.MainInventoryListType
}

// decodeCurrentDisjointBatchRequest 严格解码批量分解请求体：
//
//	[0:9]   共享前缀（0xFFFF / list=0 / tool / context）
//	[9:13]  固定标签 42 32 36 01
//	[13]    件数 n（1..72）
//	[14:]   n × u16 槽位（小端，与单件体的 slot 字段同字节序）
//
// 长度必须精确等于 14+2n（既不多也不少）；槽位必须为正、不在账号共享槽
// （354..365）、且互不重复。任何一条不满足都返回 errCurrentDisjointRequestInvalid
// 包裹的具名原因（含 body_len/count/tag/slot），调用方据此回 19、不消耗不发放。
func decodeCurrentDisjointBatchRequest(body []byte) (currentDisjointBatchRequest, error) {
	if len(body) < currentDisjointBatchHeaderSize || len(body) > currentDisjointBatchMaxBodySize {
		return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 body=%d", errCurrentDisjointRequestInvalid, len(body))
	}
	if int16(binary.LittleEndian.Uint16(body[0:2])) != currentDisjointBatchHeaderSlot ||
		body[2] != dnfrepo.MainInventoryListType {
		return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 slot=%d list=%d",
			errCurrentDisjointRequestInvalid, int16(binary.LittleEndian.Uint16(body[0:2])), body[2])
	}
	request := currentDisjointBatchRequest{
		ToolSlot: int16(binary.LittleEndian.Uint16(body[3:5])),
		Context:  binary.LittleEndian.Uint32(body[5:9]),
	}
	tag := body[currentDisjointBatchPrefixSize : currentDisjointBatchPrefixSize+currentDisjointBatchTagSize]
	if !bytes.Equal(tag, currentDisjointBatchTag[:]) {
		return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 tag=%x want=%x",
			errCurrentDisjointRequestInvalid, tag, currentDisjointBatchTag)
	}
	count := int(body[currentDisjointBatchPrefixSize+currentDisjointBatchTagSize])
	if count < currentDisjointBatchMinItems || count > currentDisjointBatchMaxItems {
		return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 count=%d body=%d",
			errCurrentDisjointRequestInvalid, count, len(body))
	}
	header := currentDisjointBatchHeaderSize
	if len(body) != header+2*count {
		return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 count=%d body=%d want=%d",
			errCurrentDisjointRequestInvalid, count, len(body), header+2*count)
	}
	request.Slots = make([]int16, 0, count)
	seen := make(map[int16]bool, count)
	for index := 0; index < count; index++ {
		offset := header + index*2
		slot := int16(binary.LittleEndian.Uint16(body[offset : offset+2]))
		if slot <= 0 || dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, slot) {
			return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 slot=%d index=%d",
				errCurrentDisjointRequestInvalid, slot, index)
		}
		if seen[slot] {
			return currentDisjointBatchRequest{}, fmt.Errorf("%w: batch op26 repeated slot=%d index=%d",
				errCurrentDisjointRequestInvalid, slot, index)
		}
		seen[slot] = true
		request.Slots = append(request.Slots, slot)
	}
	return request, nil
}

func parseCurrentAvatarDisjointRequest(body []byte) (currentAvatarDisjointRequest, error) {
	if len(body) != currentAvatarDisjointRequestMinSize && len(body) != currentAvatarDisjointRequestFullSize {
		return currentAvatarDisjointRequest{}, fmt.Errorf("%w: op202 body=%d", errCurrentDisjointRequestInvalid, len(body))
	}
	request := currentAvatarDisjointRequest{SourceSlot: int16(binary.LittleEndian.Uint16(body[0:2]))}
	if len(body) == currentAvatarDisjointRequestFullSize {
		request.ExpectedItemID = binary.LittleEndian.Uint32(body[2:6])
	}
	if request.SourceSlot < 0 {
		return currentAvatarDisjointRequest{}, fmt.Errorf("%w: op202 slot=%d", errCurrentDisjointRequestInvalid, request.SourceSlot)
	}
	return request, nil
}

func parseCurrentEmblemCompoundRequest(body []byte) (currentEmblemCompoundRequest, error) {
	if len(body) < 1 {
		return currentEmblemCompoundRequest{}, errCurrentDisjointRequestInvalid
	}
	count := int(body[0])
	if count < currentEmblemCompoundMinInputs || count > currentEmblemCompoundMaxInputs || len(body) != 1+count*currentEmblemCompoundInputSize {
		return currentEmblemCompoundRequest{}, fmt.Errorf("%w: op256 count=%d body=%d", errCurrentDisjointRequestInvalid, count, len(body))
	}
	request := currentEmblemCompoundRequest{Inputs: make([]currentEmblemCompoundInput, 0, count)}
	for index := 0; index < count; index++ {
		offset := 1 + index*currentEmblemCompoundInputSize
		input := currentEmblemCompoundInput{ItemID: binary.LittleEndian.Uint32(body[offset : offset+4]), Slot: int16(binary.LittleEndian.Uint16(body[offset+4 : offset+6]))}
		if input.ItemID == 0 || input.Slot <= 0 || dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, input.Slot) {
			return currentEmblemCompoundRequest{}, fmt.Errorf("%w: op256 input=%d item=%d slot=%d", errCurrentDisjointRequestInvalid, index, input.ItemID, input.Slot)
		}
		request.Inputs = append(request.Inputs, input)
	}
	return request, nil
}

func buildCurrentDisjointItemSuccessBody(request currentDisjointItemRequest, rewards []currentDisjointRewardSlot) []byte {
	var body packetWriter
	body.writeByte(1)
	body.writeUint16(uint16(request.SourceSlot))
	body.writeByte(request.ListType)
	body.writeUint16(uint16(request.ToolSlot))
	body.writeByte(byte(len(rewards)))
	for _, reward := range rewards {
		body.writeUint16(uint16(reward.Slot))
		body.writeUint32(reward.ItemID)
		body.writeUint32(reward.Granted)
	}
	return body.bytes()
}

// currentDisjointBatchAggregatedRewards 把整批分解的产物按 (槽,物品) 合并成**一份**
// 行清单（数量 = 各件 Granted 之和），行序 = 槽升序（同槽再按物品升序）。
//
// 它就是"分解多样东西只弹一个框"的数据来源：单件 ACK 的行本来就承载"本次获得"的
// 数量（`Granted`，见本文件顶部与 changelog 里"普通装备分解本次获得数量修复"），
// 把多件的行并成一份即可，不需要新字段、也不改槽位刷新。
func currentDisjointBatchAggregatedRewards(items []currentDisjointBatchItem) []currentDisjointRewardSlot {
	if len(items) == 0 {
		return nil
	}
	index := make(map[[2]uint32]int, len(items))
	merged := make([]currentDisjointRewardSlot, 0, len(items))
	for _, item := range items {
		for _, reward := range item.Rewards {
			key := [2]uint32{uint32(uint16(reward.Slot)), reward.ItemID}
			position, found := index[key]
			if !found {
				position = len(merged)
				index[key] = position
				merged = append(merged, currentDisjointRewardSlot{
					Slot:    reward.Slot,
					ItemID:  reward.ItemID,
					Count:   reward.Count,
					Granted: reward.Granted,
				})
				continue
			}
			merged[position].Granted += reward.Granted
			merged[position].Count = reward.Count
		}
	}
	sort.SliceStable(merged, func(left, right int) bool {
		if merged[left].Slot != merged[right].Slot {
			return merged[left].Slot < merged[right].Slot
		}
		return merged[left].ItemID < merged[right].ItemID
	})
	return merged
}

// buildCurrentDisjointBatchAggregateSuccessBody 是 single_aggregate_ack 档的回包：
// **一条**已证实的单件成功体（`01 | i16 来源槽 | u8 list | i16 tool | u8 行数 | 行…`），
// 来源槽 = 本批第一件（请求里真实存在过的槽位），行 = 整批合并后的产物。
func buildCurrentDisjointBatchAggregateSuccessBody(batch currentDisjointBatchRequest, items []currentDisjointBatchItem) []byte {
	request := currentDisjointItemRequest{
		SourceSlot: batch.Slots[0],
		ListType:   dnfrepo.MainInventoryListType,
		ToolSlot:   batch.ToolSlot,
	}
	return buildCurrentDisjointItemSuccessBody(request, currentDisjointBatchAggregatedRewards(items))
}

func buildCurrentAvatarDisjointSuccessBody(request currentAvatarDisjointRequest, rewards []currentDisjointRewardSlot) []byte {
	var body packetWriter
	body.writeByte(1)
	body.writeUint16(uint16(request.SourceSlot))
	body.writeUint16(uint16(len(rewards)))
	for _, reward := range rewards {
		body.writeUint16(uint16(reward.Slot))
		body.writeUint32(reward.ItemID)
		body.writeUint32(reward.Count)
	}
	return body.bytes()
}

func buildCurrentEmblemCompoundSuccessBody(reward currentDisjointRewardSlot) []byte {
	var body packetWriter
	body.writeByte(1)
	body.writeByte(1)
	body.writeUint32(reward.ItemID)
	body.writeUint32(reward.Granted)
	return body.bytes()
}
