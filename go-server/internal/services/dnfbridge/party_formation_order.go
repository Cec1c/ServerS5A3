package dnfbridge

import "strings"

// 本文件实现 formation（建队/入队/委任队长/成员变更）里
// **op11（0x000b 端点信息）/ op153（0x0099 实时表）/ op9（0x0009 名册）三者的下发顺序**对齐。
//
// 厂商 9.16 实测（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，同一场组队同一毫秒窗口
// 10:23:05.984–.985）：
//
//	:3335 EVENT kind=game-runtime-party-peer-endpoints-send conn_id=game-000059 …
//	      body_len=23 msg_id=11 classification=0 source=managed_runtime_party_snapshot_phase1
//	      ordering=all_members_op11_barrier_before_any_op9_then_op153
//	:3338 EVENT … conn_id=game-000061 …（第二条 op11，另一个收件人）同样 phase1
//	:3341 EVENT kind=game-current-party-frame-op9-send conn_id=game-000059
//	      source=managed_runtime_party_snapshot_phase2 … body_len=46 msg_id=9
//	:3344 EVENT kind=game-upper-send-meta conn_id=game-000059 … msg_id=153 …
//	:3346/:3349 同一对（op9 phase2 → op153）给 conn_id=game-000061
//
// 即厂商把 formation 明确分成两段：
//   - **phase1 = 给每个成员各发一条 op11，作为 barrier，发生在任何 op9 之前**；
//   - **phase2 = op9 完整队伍信息 + 固定槽位表（body_len=46）**，op153 紧随其后。
//
// 我们改动前是 `op153 → op11 → op9`（op11 落在 op153 之后、op9 之前；日志串写死
// `op153_then_op11_before_op9`）。
//
// ⚠️ 顺序读数的诚实标注：厂商自己的 `ordering=` 串写的是
// `all_members_op11_barrier_before_any_op9_then_op153`，而同一窗口里的**实际字节序**是
// `op11 → op9 → op153`（见上面 :3341/:3344 与 :3346/:3349）。两种读法都支持"op11 是
// barrier、在所有 op9 之前"这一条**唯一被本任务指定要改的**差异；op153 与 op9 的相对顺序
// 因此存在两种候选，本仓库按"单变量 + 一键回退"的既有纪律**两档都给**：
//
//   - `vendor`（默认，本任务指定的对齐目标）= `op11 → op153 → op9`：
//     op11 提到最前面，**同时保留本仓库既有不变式"op153 必须早于 op9"**
//     （见 `party_online.go` 里 `sendRuntimePartyRosterLocal` 的注释：只发 op9 会让客户端的
//     实时缓存留在旧值/空值，表现为队友 0 血、minimap 无标记）。这是**风险最小**的一档：
//     相对 legacy 只移动了 op11 的位置。
//   - `vendor_wire` = `op11 → op9 → op153`：厂商日志里**逐字节观测到的**顺序（上引 :3341/:3344）。
//     它与本仓库既有不变式相反，所以不设为默认，只作为实测 A/B 的备用档。
//   - `legacy` = `op153 → op11 → op9`：改动前的行为，**逐字节回退**（包序、body、日志串全同）。
//
// 日志对齐：vendor/vendor_wire 档下 op11 的 `source` 用厂商同名字面量
// `managed_runtime_party_snapshot_phase1`、op9 用 `managed_runtime_party_snapshot_phase2`，
// 便于直接把两边的 packet_log 并排 diff。
const partyFormationOrderEnv = "DNFBRIDGE_PARTY_FORMATION_ORDER"

const (
	partyFormationOrderValueVendor     = "vendor"
	partyFormationOrderValueVendorWire = "vendor_wire"
	partyFormationOrderValueLegacy     = "legacy"
)

// partyFormationOrderMode 是 partyFormationOrderEnv 的归一化取值。
type partyFormationOrderMode int

const (
	// partyFormationOrderVendor 是默认档（零值）= 厂商两阶段顺序（op11 barrier → op153 → op9）。
	partyFormationOrderVendor partyFormationOrderMode = iota
	// partyFormationOrderVendorWire = 厂商日志里实际观测到的字节序（op11 → op9 → op153）。
	partyFormationOrderVendorWire
	// partyFormationOrderLegacy = 改动前的顺序（op153 → op11 → op9），逐字节回退档。
	partyFormationOrderLegacy
)

func (mode partyFormationOrderMode) String() string {
	switch mode {
	case partyFormationOrderVendorWire:
		return partyFormationOrderValueVendorWire
	case partyFormationOrderLegacy:
		return partyFormationOrderValueLegacy
	default:
		return partyFormationOrderValueVendor
	}
}

// op11First 表示 op11 是否作为 phase1 barrier 先于 op153 发出（厂商两档都是）。
func (mode partyFormationOrderMode) op11First() bool {
	return mode != partyFormationOrderLegacy
}

// realtimeBeforeRoster 表示 formation 里的 op153 是否在 op9 之前发出。
func (mode partyFormationOrderMode) realtimeBeforeRoster() bool {
	return mode != partyFormationOrderVendorWire
}

// usesVendorPhaseLabels 表示日志 source 是否使用厂商的 phase1/phase2 命名。
func (mode partyFormationOrderMode) usesVendorPhaseLabels() bool {
	return mode != partyFormationOrderLegacy
}

// phase1Source 是 op11 的日志 source。
func (mode partyFormationOrderMode) phase1Source() string {
	if mode.usesVendorPhaseLabels() {
		return "managed_runtime_party_snapshot_phase1"
	}
	return "runtime_party_snapshot"
}

// phase2Source 是本收件人那条 op9 名册的日志 source。
func (mode partyFormationOrderMode) phase2Source() string {
	if mode.usesVendorPhaseLabels() {
		return "managed_runtime_party_snapshot_phase2"
	}
	return "runtime_party_snapshot"
}

// areaPeerSource 是"同区域队友那条 op9"的日志 source（厂商用
// `managed_runtime_party_snapshot_to_area_peer`，不带 phase 后缀）。
func (mode partyFormationOrderMode) areaPeerSource() string {
	if mode.usesVendorPhaseLabels() {
		return "managed_runtime_party_snapshot"
	}
	return "runtime_party_snapshot"
}

// orderingLabel 是 op11 下发日志里的 `ordering` 字段。
// 厂商档直接用厂商自己的字面量，方便并排 diff。
func (mode partyFormationOrderMode) orderingLabel() string {
	if mode.usesVendorPhaseLabels() {
		return "all_members_op11_barrier_before_any_op9_then_op153"
	}
	return "op153_then_op11_before_op9"
}

// afterRosterOrderingLabel 是 M1（`DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER=repeat`）
// 在 op9 之后再补发一次 op11 时用的 `ordering` 字段：在本次 formation 的顺序串后追加
// `_then_op11_after_op9`，所以 legacy 档仍是我们改动前那一串（逐字不变）。
func (mode partyFormationOrderMode) afterRosterOrderingLabel() string {
	return mode.orderingLabel() + "_then_op11_after_op9"
}

// readPartyFormationOrderMode 解析 DNFBRIDGE_PARTY_FORMATION_ORDER。
// **默认 `vendor`（= 本任务指定的对齐目标）**：空/未设置/非法值都是 vendor；
// `legacy` 一键回到改动前的顺序，`vendor_wire` 是厂商日志实际字节序的备用档。
func readPartyFormationOrderMode(value string) partyFormationOrderMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case partyFormationOrderValueLegacy:
		return partyFormationOrderLegacy
	case partyFormationOrderValueVendorWire:
		return partyFormationOrderVendorWire
	default:
		return partyFormationOrderVendor
	}
}

// partyFormationOrder 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = vendor = 默认档）。
func (s *Service) partyFormationOrder() partyFormationOrderMode {
	if s == nil {
		return partyFormationOrderVendor
	}
	return s.options.partyFormationOrder
}
