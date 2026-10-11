package equip

// LoginWearReassertNoopGate 是 MoveResult.NoopGate 上"登录引导窗口兜底"那条闸门的
// 具名值：owner.go 的 equipmentLoginWearReassertNoop 命中时写出的就是它。
//
// 为什么单独放一个常量文件：这条名字的**下游读者**在别的包里 ——
// internal/modules/dnf/inventory 的 ownerAppliedEquipmentMoveResult 按它决定
// "noop 也要补一次权威刷新"（见 dnfbridge/equip_login_wear_family_window.go 的长注）。
// 两处若各写一份字面量，任何一处被改都会静默失配（备份路径退化成今天的"只回一条
// ACK"），所以这里给出唯一的具名值，并由
// inventory/handler_login_wear_reassert_test.go 断言 owner 实际写出的
// `noopGate=` 就等于它。
//
// owner.go 里的字面量**未改动**（闸门判据是保护范围）；本常量与它取值相同，
// 由上面那条测试钉住两者不许漂移。
const LoginWearReassertNoopGate = "login_wear_reassert_noop"

// IsWornClaimNoopGate 回答某个 MoveResult.NoopGate 是否属于"客户端在复述穿戴态"那一族
// noop —— 也就是"一个字节都没写库、但客户端手里那份视图需要被权威状态纠正"的三条闸门：
//
//	LoginWearReassertNoopGate  登录引导窗口兜底（时间窗内，见 equipmentLoginWearReassertNoop）
//	WornSlotReassertGate       方向判定：客户端点名的那件正穿在那一格（声明已经成立）
//	WornSlotStaleClaimGate     方向判定：那一格**空着**，客户端在声明一件不在身上的东西
//
// 三条的共同点是：客户端报出的穿戴/背包状态与服务端权威状态**不一致**（或已经一致），
// 而这次 op19 一个字节都没写。下游（internal/modules/dnf/inventory 的
// ownerAppliedEquipmentMoveResult）按这条判据决定"noop 也要照真落库路径补权威刷新"，
// 这样客户端那份幻影副本才会被纠正，而不是下一条登录再复述同一句。
//
// 判据放这里（而不是在下游各写一份字面量）：任何一处漂移都会静默退化成"只回一条 ACK"。
// 比较语义与旧代码的 `result.NoopGate == LoginWearReassertNoopGate` 完全一致
// （精确匹配，不做 TrimSpace），旧闸门的行为因此逐字节不变。
func IsWornClaimNoopGate(gate string) bool {
	switch gate {
	case LoginWearReassertNoopGate, WornSlotReassertGate, WornSlotStaleClaimGate:
		return true
	default:
		return false
	}
}
