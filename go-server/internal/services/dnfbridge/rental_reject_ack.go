package dnfbridge

import (
	"encoding/binary"
	"os"
	"strings"
)

// 操作者已知问题清单第 10 条：租赁武器（“无法租赁武器”）。
//
// 取证结论（2026-09-27，详见 changlog.md 同日条目）：
//
//  1. 我方租赁链路**不是缺包**，而且有线上成功实证：
//     `packet_log.20260925-055809.txt`
//     `RECV kind=game-legacy cmd=1 type=999 body_len=21` →
//     `EVENT kind=game-upper-rental-equipment-applied ... item_id=408020031 job=14
//     job_tag=[at priest] tier=7 point_cost=40 slot=14 expire_unix=1790372244`
//     （同场还有 op1000 两次充值成功、op985 面板刷新、过期清理 `removed_count=1`）。
//  2. 真档 `Script.pvf` 的 `etc/chnrentsystem/rentsysteminfo.etc` **有** `[package selection]`
//     （按 `[group]` 分职业 + 档位），`TestRealScriptPVFCurrentRentalCatalog` 对真档实测 PASS。
//  3. 线上自 2026-09-25 起再没有任何租赁请求：全库 `grep -rc type=999 / type=1000`
//     只命中那一个文件，`game-upper-rental-*-blocked` 事件 0 条。
//
// 所以本条**要修的不是“缺功能”，而是“拒绝路径的封包口径”**。我方在事务前的每一条拒绝
// 分支上**一个字节都不回**，而厂商**每一条分支都会回包**。客户端点了“租赁”却拿不到任何
// 应答，现象就是“点了没反应 / 无法租赁”。
//
// 厂商依据（本机厂商源码 `S4A21-完整包-20260923\源码\服务端\Server\DfoServer\`）：
//
//   - `Network\Builders\RentalWeaponPacketBuilder.cs:11-23`
//     ```csharp
//     public static byte[] BuildSuccessAck() => BuildResultAck(0);
//     public static byte[] BuildResultAck(uint result) {
//     var body = new byte[5]; body[0] = 1;
//     Buffer.BlockCopy(BitConverter.GetBytes(result), 0, body, 1, 4); return body; }
//     public static byte[] BuildFailureAck() => new byte[] { 0 };
//     ```
//     ⇒ 带结果码 = **5 字节** `01 <u32 LE result>`；纯失败 = **1 字节** `00`。
//   - `Network\Handlers\RentalHandler.cs` 每条分支都回包：
//     `:49` 解析失败 → `SendFailure(session)`（result=0 ⇒ `BuildFailureAck()` = `00`）；
//     `:58` 没有 owned `InventoryLease` → 同上；
//     `:108` 事务拒绝 → `SendFailure(session, failureResult)`，其中只有
//     `CanRentWeapon` 失败会置 `failureResult = InventoryFullResult`(=2)，其余（幸运星不足、
//     应用失败）`failureResult` 仍是 0 ⇒ `00`。
//   - `Network\Handlers\LuckyStarHandler.cs:125-131`（充值 op976）：失败 =
//     `Build(0x01, CHARGE_RENTPOINT, new byte[] { 0x00 })` ⇒ **1 字节** `00`。
//   - `Network\Handlers\LuckyStarClientNotifier.cs:44-68`（充值成功）：**13 字节**
//     `01 <i32 mode> <i32 quantity> <i32 totalLuckyStar>`，`mode` / `quantity`
//     **原样回显请求体**（不是 0）。
//
// 我方改动前：
//   - op999 / op1000 **事务前**拒绝（解析失败、未选角、PVF 目录读不到、职业越界、
//     无匹配档位、job|tier 不匹配、不在 PVF 分组、道具校验失败、owner 不可用）
//     **不回任何包**；
//   - op999 事务拒绝一律 `01 <u32 1>`（厂商：只有背包满回 `01 <u32 2>`，其余回 `00`）；
//   - op1000 事务拒绝回 `00 01`（厂商回 `00`）；
//   - op1000 成功第二个 u32 恒为 0（厂商回显请求里的 quantity）。
//
// ⚠️ 本文件**不动成功路径的字节**：op999 成功仍是 `01 <u32 0>`（5 字节），
// op999 背包满仍是 `01 <u32 2>`，op985 面板刷新一字不改。
//
// 一键回退：`DNFBRIDGE_RENTAL_REJECT_ACK=off`（或 0/false/no/legacy/disable/disabled）
// ⇒ 逐字节回到今天（静默丢弃 + 事务拒绝 `01 <u32 1>` / `00 01` / 充值应答 u32 0）。
const (
	// currentRentalRejectAckEnv 是本条修复的开关。
	//
	// 取值：
	//   - 空 / `missing`（**默认**）= 只补“今天完全不回包”的纯缺失分支，按厂商
	//     `BuildFailureAck()` 回 1 字节 `00`；**已经回包的分支一字不动**。
	//   - `vendor` = 在 `missing` 基础上，把“事务级拒绝”与“充值成功应答”也按厂商口径对齐
	//     （事务拒绝 → `00`；充值成功第二个 u32 → 回显请求 quantity）。
	//   - `off` / `0` / `false` / `no` / `legacy` / `disable` / `disabled` = 逐字节回到今天。
	//
	// 为什么 `missing` 档可以默认开启：这些分支在改动前**不产生任何报文**，
	// 新增的 1 字节 `00` 是**严格增量**——既不改变任何既有报文的字节、顺序、sequence 分配，
	// 也不改变仓储事务、背包行、面板刷新与日志事件（`missing` 档只在“本来要静默返回”的位置
	// 追加一条 `game-upper-rental-failure-ack-send` 事件）。成功路径与 op985 刷新完全不受影响，
	// 所以它不可能影响其它路径。仍然保留 `off` 档做一键回退。
	currentRentalRejectAckEnv = "DNFBRIDGE_RENTAL_REJECT_ACK"

	// 三个归一化档位。
	currentRentalRejectAckMissing = "missing"
	currentRentalRejectAckVendor  = "vendor"
	currentRentalRejectAckOff     = "off"

	// 事件名：只在真的补发了回包时产生；`off` 档一条都不产生。
	currentRentalFailureAckEvent = "game-upper-rental-failure-ack-send"

	// 厂商 `RentalWeaponPacketBuilder.cs` 的两个结果码（照抄，不臆造）。
	currentRentalVendorTooManyActiveItemsResult = 1
	currentRentalVendorInventoryFullResult      = 2
)

var currentRentalRejectAckOffValues = map[string]bool{
	"off": true, "0": true, "false": true, "no": true, "legacy": true, "disable": true, "disabled": true,
}

var currentRentalRejectAckVendorValues = map[string]bool{
	"vendor": true, "vendor_shapes": true, "aligned": true,
}

// currentRentalRejectAckMode 归一化开关取值。非法值一律回落到默认档 `missing`
// （与既有开关约定一致：非法值不放大行为，只回到默认）。
func currentRentalRejectAckMode() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentRentalRejectAckEnv)))
	switch {
	case value == "":
		return currentRentalRejectAckMissing
	case currentRentalRejectAckOffValues[value]:
		return currentRentalRejectAckOff
	case currentRentalRejectAckVendorValues[value]:
		return currentRentalRejectAckVendor
	default:
		return currentRentalRejectAckMissing
	}
}

// currentRentalRejectAckFillsMissing 是否补“今天完全不回包”的纯缺失分支。
// `missing` 与 `vendor` 都补；只有 `off` 不补。
func currentRentalRejectAckFillsMissing(mode string) bool {
	return mode != currentRentalRejectAckOff
}

// currentRentalRejectAckVendorShapes 是否连“已经回包”的分支也按厂商口径改形状。
func currentRentalRejectAckVendorShapes(mode string) bool {
	return mode == currentRentalRejectAckVendor
}

// buildCurrentRentalFailureAckBody 厂商 `RentalWeaponPacketBuilder.BuildFailureAck()`：
// 单字节 `00`（classification 仍为 1，由 `sendGameUpperRaw` 负责）。
func buildCurrentRentalFailureAckBody() []byte {
	return []byte{0}
}

// buildCurrentRentalResultAckBody 厂商 `RentalWeaponPacketBuilder.BuildResultAck(result)`：
// 5 字节 `01 <u32 LE result>`。正好等于既有的 `sendGameUpperSuccess(cmd, u32 result)`。
func buildCurrentRentalResultAckBody(result uint32) []byte {
	body := make([]byte, 5)
	body[0] = 1
	binary.LittleEndian.PutUint32(body[1:5], result)
	return body
}

// buildCurrentRentalChargeSuccessBody 厂商
// `LuckyStarClientNotifier.BuildChargeRentPointSuccessBody`：13 字节
// `01 <i32 mode> <i32 quantity> <i32 totalLuckyStar>`。
//
// echoQuantity=false 时第二个 u32 写 0 —— 即改动前 `handleCurrentChargeRentalPoint` 的字节。
func buildCurrentRentalChargeSuccessBody(mode, quantity, totalPoints uint32, echoQuantity bool) []byte {
	var writer packetWriter
	writer.writeUint32(mode)
	if echoQuantity {
		writer.writeUint32(quantity)
	} else {
		writer.writeUint32(0)
	}
	writer.writeUint32(totalPoints)
	return writer.bytes()
}

// currentRentalTransactionRejectUsesVendorFailureAck 判定一次 op999 事务拒绝该回哪种体。
//
// 厂商 `RentalHandler.cs:74-108`：只有 `CanRentWeapon` 失败（背包满）置
// `failureResult = InventoryFullResult(2)` 并回 `BuildResultAck(2)` = `01 02000000`；
// 其余（幸运星不足 `:84`、应用失败 `:98`）`failureResult` 仍是 0 ⇒ 回 1 字节 `00`。
//
// 默认档（`missing` / `off`）返回 false = 保留我方今天的 `01 <u32 resultCode>`，一字不动。
func currentRentalTransactionRejectUsesVendorFailureAck(mode string, resultCode uint32) bool {
	return currentRentalRejectAckVendorShapes(mode) && resultCode != currentRentalVendorInventoryFullResult
}

// sendCurrentRentalFailureAck 在“改动前完全不回包”的分支上按厂商口径补发
// `BuildFailureAck()`（class 1 + 1 字节 `00`）。
//
// 返回 nil 不代表“没发”，只表示这次处理不应中断连接：与既有拒绝分支一样吞掉错误来源。
func (s *Service) sendCurrentRentalFailureAck(session *gameSession, command uint16, reason string, fields ...any) error {
	mode := currentRentalRejectAckMode()
	if !currentRentalRejectAckFillsMissing(mode) {
		return nil
	}
	body := buildCurrentRentalFailureAckBody()
	eventFields := []any{
		"msg_id", command,
		"classification", 1,
		"body_len", len(body),
		"layout", "u8_zero_vendor_build_failure_ack",
		"reason", reason,
		"mode", mode,
		"env", currentRentalRejectAckEnv,
		"body_source", "vendor_network_builders_rental_weapon_packet_builder_build_failure_ack",
	}
	eventFields = append(eventFields, fields...)
	s.logGameEvent(session, currentRentalFailureAckEvent, eventFields...)
	return s.sendGameUpperRaw(session, command, body)
}

// sendCurrentRentalResultAck 按厂商 `BuildResultAck(result)` 回 5 字节
// `01 <u32 LE result>`（= 既有的 `sendCurrentRentEquipmentResult`）。
func (s *Service) sendCurrentRentalResultAck(session *gameSession, command uint16, result uint32, reason string, fields ...any) error {
	body := buildCurrentRentalResultAckBody(result)
	eventFields := []any{
		"msg_id", command,
		"classification", 1,
		"body_len", len(body),
		"layout", "u8_one_u32_le_result_vendor_build_result_ack",
		"result", result,
		"reason", reason,
		"mode", currentRentalRejectAckMode(),
		"env", currentRentalRejectAckEnv,
		"body_source", "vendor_network_builders_rental_weapon_packet_builder_build_result_ack",
	}
	eventFields = append(eventFields, fields...)
	s.logGameEvent(session, currentRentalFailureAckEvent, eventFields...)
	return s.sendGameUpperRaw(session, command, body)
}
