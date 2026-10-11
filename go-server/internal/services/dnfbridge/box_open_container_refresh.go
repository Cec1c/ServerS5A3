package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 开盒子（op208 单开 / op1128 连开）**回包后只重建真正变了的容器**的口径。
//
// 现场缺陷（`changlog.md:1103-1106`，操作者 bug 清单第 5 条"开盒子等高频操作导致
// 服务端卡顿"）：op44 早已是增量，op160（礼盒）走的是 bridge 原生路径、本来就只发
// `result.ChangedLists`（见 gameplay_module_booster.go:106-114），而 op208/op1128
// 走的是 `packageitem.ownerAppliedMagicBoxResult` → `PostActionRefreshSelectedItemContainers`
// → `sendSelectedCurrentContainerListsWithRefresh`，**无条件回放五个整容器**
// （`currentSelectedItemListTypes` = list 0/1/2/7/38）。量到的代价：
// 夹具 5 包 / 11,333 B、线上同五个列表 19,175 B（`7264+3688+6075+2145+3`），
// `BenchmarkMagicBoxPostActionRefresh` = 239 µs / 370 KB / 850 allocs（`changlog.md:1113-1115`）。
//
// 但那次分诊同时写明：**没有客户端/厂商证据可以翻转默认口径**（9.16 基线日志里根本
// 没有开盒会话），所以这里**不改默认**：默认档 `current` = 逐字节保持今天的五个整容器
// 回放，只有显式写 `changed_only` 才启用"只发变了的容器"。
//
// changed_only 档的判据（不是猜的，来自本次开盒的实际写入）：
//  1. **源容器恒为 list 0**：魔盒请求的列表类型只接受 `0`/客户端 `4`
//     （packageitem/request.go:135-140 `mapMagicBoxListType`），其余取值直接判请求
//     非法、连 ACK 都没有；盒子与材料因此总是从主背包被扣掉 ⇒ list 0 一定变了。
//  2. **奖励容器 = 本次真正发放到的容器**：bridge 自己提供奖励模板解析器
//     （magic_box_pvf.go:67-74），packageitem 在事务里**只对实际抽中的奖励**逐个调用
//     它（packageitem/owner.go:422-438 的 `rewardIDs` 循环），而模板上的
//     `TargetListType` 正是"这件奖励落哪个容器"的唯一真源
//     （magic_box_pvf.go:421-426 `currentPVFItemTargetContainer`）。
//     于是把解析器包一层，就能在同一个 goroutine 里、ACK 之前拿到精确的容器集合。
//  3. 解析器没被调用过（记录不完整）⇒ **回落全量档**，绝不猜容器。
//
// 容器集合最后按 `currentSelectedItemListTypes` 的规范顺序过滤：同一个容器无论走哪个档，
// 包体（同一个 builder）与相对顺序都逐字节相同；规范集里没有的 list 今天也不会发，
// 所以过滤不会删掉今天会发的包。
//
// 一键回退：
//
//	DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH=current
//
// **残余不确定**（所以默认档没有跟随新口径）：开盒材料在 packageitem 里是"优先按
// list 0 的请求槽找，找不到再取全表最小键"（owner.go:321-353），理论上材料若被放在
// 装扮/宠物/公会栏，扣它的那个容器不会在 changed_only 档被重发（库内状态仍然正确，
// 只是客户端那一格要等下次该容器的刷新）。这一条只能在有客户端证据之后才敢翻转默认。
const (
	boxOpenContainerRefreshEnv         = "DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH"
	boxOpenContainerRefreshCurrent     = "current"
	boxOpenContainerRefreshChangedOnly = "changed_only"
)

// 开盒命令的操作名（packageitem/command.go:47/61，由 ownerAppliedMagicBoxResult 放进
// Result.Operation，bridge 在 sendAlignedPostAction 里按它分流）。
const (
	boxOpenOperationRandombox       = "use_randombox_item"
	boxOpenOperationRandomboxExpand = "use_randombox_item_expand"
)

// normalizeBoxOpenContainerRefreshMode 归一化 DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH。
// current（默认）= 逐字节保持今天的五个整容器回放；changed_only = 只重发本次真正改变的
// 容器（源 list 0 + 本次实际发放到的奖励容器）。其余取值（含未设置）一律回到 current。
func normalizeBoxOpenContainerRefreshMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case boxOpenContainerRefreshChangedOnly, "changed", "minimal", "on", "1", "true":
		return boxOpenContainerRefreshChangedOnly
	default:
		return boxOpenContainerRefreshCurrent
	}
}

// boxOpenContainerRefreshChangedOnlyRequested 回答"开盒是否只重发变了的容器"。
// 只有显式写 changed_only 才是新口径；零值 = current = 今天的线格式
// （与 equipMoveContainerRefreshFull 同样的"零值即现状"约定，测试与探针里的
// `Service{options: options{...}}` 字面量不会提到这个字段）。
func (s *Service) boxOpenContainerRefreshChangedOnlyRequested() bool {
	if s == nil {
		return false
	}
	return s.options.boxOpenContainerRefreshChangedOnly
}

// boxOpenContainerRefreshMode 只用于启动日志/诊断。
func (s *Service) boxOpenContainerRefreshMode() string {
	if s.boxOpenContainerRefreshChangedOnlyRequested() {
		return boxOpenContainerRefreshChangedOnly
	}
	return boxOpenContainerRefreshCurrent
}

// isBoxOpenOperation 判定这次 aligned 命令是不是"开盒子"（op208/op1128）。
// op160（礼盒）刻意不在里面：它走 bridge 原生路径，本来就只发 changed lists。
func isBoxOpenOperation(operation string) bool {
	switch strings.TrimSpace(operation) {
	case boxOpenOperationRandombox, boxOpenOperationRandomboxExpand:
		return true
	default:
		return false
	}
}

// armBoxOpenContainerRecord 在把 op208/op1128 交给 packageitem 之前装好"本次开盒写到
// 哪些容器"的记录：复位 + 包装 bridge 自己的奖励模板解析器。current（默认）档**一个
// 字段都不写**（连包装都不做），所以默认行为与加开关之前逐字节一致。
//
// 记录只在这次命令的 ACK/后置动作里被读取（同一个连接处理 goroutine 内串行发生），
// 因此不需要额外的锁。
func (s *Service) armBoxOpenContainerRecord(
	session *gameSession,
	resolver alignedcmd.MagicBoxRewardItemResolver,
) alignedcmd.MagicBoxRewardItemResolver {
	if s == nil || session == nil || resolver == nil || !s.boxOpenContainerRefreshChangedOnlyRequested() {
		return resolver
	}
	session.beginBoxOpenContainerRecord()
	return session.recordBoxOpenRewardContainer(resolver)
}

// beginBoxOpenContainerRecord 复位记录。**必须在模块被调用之前**做，否则上一次开盒
// 的容器集合会在本次失败时留下残影。
func (s *gameSession) beginBoxOpenContainerRecord() {
	if s == nil {
		return
	}
	s.boxOpenRewardListTypes = s.boxOpenRewardListTypes[:0]
	s.boxOpenRewardRecorded = false
}

// recordBoxOpenRewardContainer 包装奖励模板解析器：模块每解析一个**实际发放**的奖励
// 模板就记一次它的目标容器（`TargetListType` 为 0 = 主背包）。
func (s *gameSession) recordBoxOpenRewardContainer(
	inner alignedcmd.MagicBoxRewardItemResolver,
) alignedcmd.MagicBoxRewardItemResolver {
	if inner == nil {
		return nil
	}
	return func(itemID int64) (alignedcmd.MagicBoxRewardItem, error) {
		item, err := inner(itemID)
		if err != nil {
			return item, err
		}
		if item.ItemID > 0 {
			s.recordBoxOpenRewardListType(item.TargetListType)
		}
		return item, nil
	}
}

func (s *gameSession) recordBoxOpenRewardListType(listType byte) {
	if s == nil {
		return
	}
	for _, recorded := range s.boxOpenRewardListTypes {
		if recorded == listType {
			s.boxOpenRewardRecorded = true
			return
		}
	}
	s.boxOpenRewardListTypes = append(s.boxOpenRewardListTypes, listType)
	s.boxOpenRewardRecorded = true
}

// boxOpenChangedContainerListTypes 给出 changed_only 档下这次开盒要重发的容器。
// 第二个返回值为 false 表示"不要走这个档"（开关是 current，或记录不完整），调用方
// 必须回落到全量档 —— 少发一个容器的代价是客户端投影过期，所以这里只允许精确或全量。
func (s *Service) boxOpenChangedContainerListTypes(session *gameSession) ([]byte, bool) {
	if !s.boxOpenContainerRefreshChangedOnlyRequested() {
		return nil, false
	}
	if session == nil || !session.boxOpenRewardRecorded {
		s.logPacketEvent("game-box-open-changed-containers-unavailable",
			"conn_id", sessionConnID(session),
			"reason", "reward_container_record_missing",
			"fallback", "replay_all_selected_containers",
			"switch", boxOpenContainerRefreshEnv,
			"mode", boxOpenContainerRefreshChangedOnly)
		return nil, false
	}
	changed := make(map[byte]struct{}, len(session.boxOpenRewardListTypes)+1)
	// 源容器：魔盒请求只接受 list 0（packageitem/request.go:135-140），盒子与材料
	// 一定是从主背包被扣掉的。
	changed[dnfrepo.MainInventoryListType] = struct{}{}
	for _, listType := range session.boxOpenRewardListTypes {
		changed[listType] = struct{}{}
	}
	return orderCurrentContainerListTypes(changed), true
}

// orderCurrentContainerListTypes 把点名的容器按 `currentSelectedItemListTypes` 的规范
// 顺序输出（顺带去重与去重后仍不在规范集里的 list）：这样"同一个容器在 changed_only 档
// 与全量档发出的字节与相对顺序完全相同"。
func orderCurrentContainerListTypes(changed map[byte]struct{}) []byte {
	ordered := make([]byte, 0, len(changed))
	for _, listType := range currentSelectedItemListTypes {
		if _, ok := changed[listType]; ok {
			ordered = append(ordered, listType)
		}
	}
	return ordered
}
