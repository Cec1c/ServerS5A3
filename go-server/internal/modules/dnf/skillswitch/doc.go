// Package skillswitch 拥有「Buff 强化 / 技能换装栏」每角色登记的领域规则与事务边界。
//
// # 它是什么（现场口径）
//
// 当前 90CN EXE 会以 game-legacy `cmd=1 type=1428`
// （`ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY`，本仓枚举见
// `internal/modules/dnf/dnfenum/cmdpacket.go`）把**每一条**登记记录作为独立的一包
// 上行推给服务端。HK 现场 `/opt/dnf90/logs/packet_log*.txt` 全库 82 条 `type=1428`
// RECV 的 handler 字节数恒 6；四次会话（2026-09-25 01:18、09-26 01:21–01:32、
// 09-26 15:51–15:52、09-27 14:28）都出现在**登录/进城/选图后的场景与容器初始化窗口**。
//
// ⚠️ **那 6 字节不是包体（2026-09-28 实机更正）**：客户端真实的 op1428 上行包体是
// **4 字节**（`u8 Slot | u8 Mode | u16 Value LE`）。6 字节里的前 2 字节是本仓
// 13 字节 legacy 分帧把客户端 `u32` 序号读成 `u16` 之后残留下来的**序号高 16 位**。
// 证据、逐字节映射与 dual-read 规则全在 `record.go`（含 18 条落库记录的重新解读）。
// 档位开关 `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY`：默认 `v4` = 修复档，`v6` = 逐字节回到改动前。
//
// 操作者（2026-09-27）确认的因果链：客户端「Buff 强化」页登记后，
// **穿脱装备 / 选择角色 / 重新登录 / 进出副本** 四条边界上换装栏会被重置。
// 会话内登记本身是生效的（否则玩家不会只在四条边界上看到丢失），
// 因此本包的职责是：
//
//  1. 把 op1428 的每条记录**按角色**落库（settings 家族，见 Scope）；
//  2. 让四条边界上的重投影能把这份状态原样送回客户端
//     （见 `internal/services/dnfbridge/skill_switch_inventory.go` 的投影点清单）。
//     注意：**下行**要求的是另一种形状（`u8 count + count×(u32,u32)`），
//     与上行包体毫无交集；默认档 `shape_guard` 因此一个字节都不发，
//     `(key,value)` 的语义至今未证实（见 `_dist/buff-pairs-measurement.md` §6/§8）。
//
// # 与厂商参考实现的关系（**没有可抄的实现**）
//
// 已穷举搜索并留痕，结论是**厂商没有 op1428 的任何实现**：
//
//   - `H:\86JP启动器\ServerS4A21-master\Server\DfoServer\**`（1049 个 .cs）：
//     `SKILL_SWITCH` / `SkillSwitch` / `SkillSwitchInventory` 命中 **0**；
//     `Network/Core/PacketTypesA21.cs` 的 `CmdPacketTypeA21` 枚举
//     `END = 0x04F7`（=1271），**根本不含 0x0594=1428**；
//     `NotiPacketTypeA21` 枚举 `END = 0x04C2`（=1218），同样不含。
//   - `H:\86JP启动器\s4a21_master_latest_unzip\**`：同上，命中 **0**。
//   - `H:\86JP启动器\90CN\90原神开源0.1.8\dnf90-source-oneclick-main\go-server\**`：
//     只有 `dnfenum/cmdpacket.go` 里那条名字表登记，没有 handler/parser/回包。
//
// 因此本包**不发明**任何字段语义：字段名、取值域与不变量全部来自 §现场口径，
// 无法确证的部分在 `record.go` 逐条标注「未确证」。
//
// # 数据规则
//
// 不发明技能号、不发明槽位数：
//
//   - 「被引用的技能号」只取自客户端上行里那个落在**真实 job-scoped PVF 技能号空间**
//     的字段（`Record.SkillID`，判据见 `record.go`）；
//   - 技能号必须在该角色 job 的 PVF `skill/skilllist.lst` 目录里存在，
//     否则 `Apply` 返回具名错误 `ErrSkillUnknown`（**拒绝**，不落库）；
//   - 单角色留存的条数上限 `MaxRecords` 只是 settings 行的**安全上界**，
//     不是对客户端槽位数的断言，见 `store.go`。
package skillswitch
