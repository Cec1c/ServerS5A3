package inventory

import (
	"os"
	"strings"
)

// 本文件为客户端即将启用的 op19「玩家请求标志位」扫清服务端障碍。
//
// ## 背景：那个字节是什么
//
// 28 字节 op19 body 的最后两个字节是 `TrailingState0/TrailingState1`（`request.go:250`），
// 它们**只有宠物装备搬运那条路径读过**，见下面的作用域证据。客户端将把 `body[26]` 填 1，
// 用来标明"这条 op19 是玩家亲手拖拽发起的"，好让服务端（在 client 侧自动批样本到手后）
// 用一个开关按厂商口径执行真·脱下。
//
// ## 作用域证据（不是读代码猜的，两侧都有钉子）
//
// 唯一的调用点 `inventory/handler.go:61-65`：
//
//	petEquipmentMove := isProvenPetEquipmentMove(cmd)
//	if petEquipmentMove {
//		if err := validateProvenPetEquipmentMove(cmd); err != nil { ... }
//
// 而 `isProvenPetEquipmentMove`（`handler.go:1351-1360`）**要求 `SourceListType == listTypePet(7)`**，
// 还要求目的端点是穿戴端点（3/17）且落在宠物槽区间（26 且源槽 0..139；或 27..29 且源槽 140..188）。
// ⇒ 装备"脱下/穿上"的形状（`src=(list0 背包 或 list1 装扮, …) → dst=(3, …)`）**源 list 不是 7**，
// 永远进不了那个分支 ⇒ **那个尾字节对装备包没有任何约束**（= 标志位对装备路径已经安全）。
// 钉子：`op19_trailing_flag_test.go` 的 `TestProvenPetEquipmentMoveScopeIsPetSourceOnly` 与
// `TestOp19TrailingFlagEquipmentPacketNeverReachesThePetValidator`（后者同时断言：**若**作用域被放宽到装备包，
// 这条带标志的包**会**被那个校验拒掉 —— 所以下面这个开关是必要的保险，而不是空的）。
//
// ## 为什么仍然要改
//
// 客户端将对**所有** op19 打这个标志（包括宠物搬运）。宠物路径上那条常量校验会因此把
// **合法**的宠物包判成非法包（比误判成玩家请求更糟）。因此把它拆开：
// **尾字节**按本开关放宽，**其余常量（MoveCount/DestinationStack/ActorIndex）一个字不放**。
//
// ## 开关
//
//	DNFBRIDGE_OP19_TRAILING_FLAG
//	  未设 / 空 / 任意未知值 / on / flag / tolerate ⇒ **修复档（默认）**：宠物路径不再校验尾字节
//	  strict / legacy / off / 0 / false / no / disable / disabled / none ⇒ **回退档**：逐字节等于今天
//	  （宠物包尾字节非 0 一律拒绝，错误文本也逐字节相同）
//
// 与既有的 `DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT` / `DNFBRIDGE_EQUIP_MIRROR_REQUIP_ECHO` /
// `DNFBRIDGE_EMPTY_SLOT_ROW_ENCODING` **正交**：本开关只决定"宠物搬运包的尾字节要不要校验"。
const op19TrailingFlagEnv = "DNFBRIDGE_OP19_TRAILING_FLAG"

// op19TrailingFlagTolerated 回答"是否容忍 op19 尾字节非 0"（每次调用都读 env，不缓存）。
func op19TrailingFlagTolerated() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(op19TrailingFlagEnv))) {
	case "strict", "legacy", "off", "0", "false", "no", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}
