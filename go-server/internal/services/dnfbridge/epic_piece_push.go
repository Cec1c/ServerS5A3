package dnfbridge

import "strings"

// 本文件把"史诗图鉴账本下行包（class0 / msg_id=1149 = 0x047D）到底什么时候发"
// 抽成一个**独立**开关，用来把「领取邮件附件（史诗碎片）时客户端闪退」这条现场
// 收敛成一次可回退、可判读的 A/B。它只管**下行线**，完全不碰账本与背包口径：
// 碎片照样进账号图鉴、照样不进背包、登录清扫照旧。
//
// ── 现场（2026-09-25 23:37:31，香港 conn=game-000039 角色 3）──────────────
//
//	RECV op95 ENUM_CMDPACKET_MAILBOX_EXTRACT_ITEM
//	SEND op95 ack（17 字节 body，与当天 40 条普通附件回包同形）
//	SEND class0/1149 body=`01 01000000 6f2d9d00 e9030000`
//	     （reset=1，行数 1，碎片 10300783，绝对值 1001）
//	约 5 秒后客户端 0xC0000005 @DNF.exe+0x23082D3（access_address=0x40）
//
// 这条路径上唯一"厂商 9.16 参考日志里从未出现过"的包就是它：厂商那 7/7 条 1149
// **全部**是 `00 00000000`（entries=0 空表，且都落在选角/进图引导位上），
// 非空行布局（`u8 reset + u32 count + N×(u32,u32)`）是从 A21 C# 与同血统
// 0.1.8 Go 分支推断来的，**没有在当前 EXE 上验证过**。所以默认不再发这条增量。
//
// ── 但 1149 并不是本次闪退的已证凶手（静态取证反证，务必连起来读）────────
//
//  1. 同一个崩溃点（同 EIP、同 esp/ebp=0x001AF828/0x001AF88C、同返回地址
//     0x02711E84、同 ebx=0x11E / edx=0x11）在**史诗图鉴功能上线之前**已经出现过
//     两次：
//     2026-09-24 20:10:36.545 pid=31828 eip=0x027082D3 access_address=0x42
//     2026-09-25 03:40:27.399 pid=35424 eip=0x027082D3 access_address=0x42
//
//  2. 覆盖这两次会话的完整封包日志（本机取证副本
//     `Temp\crash-mail-20260925\packet_log.20260924-204011.txt`、
//     `packet_log.20260925-032926.txt`、`packet_log.20260925-034443.txt`）
//     里 `msg_id=1149` 出现 **0 次** —— 那个阶段服务端一个 1149 都没发过，
//     客户端一样崩在同一个地址、同一个调用点上。
//
//  3. 崩溃指令本身不是任何包读取器，而是一个 MSVC `std::wstring` 的 `c_str()`
//     取指针访问器（`_Bx` 在 +0x2C、`_Mysize` 在 +0x3C、`_Myres` 在 +0x40、
//     SSO 阈值 8 个 wchar_t）：
//
//     0x23082D0  8B 41 04      mov eax, dword ptr [ecx+4]
//     0x23082D3  83 78 40 08   cmp dword ptr [eax+0x40], 8    ← 崩溃点（eax=0 或 2）
//     0x23082D7  72 04         jb  0x23082DD
//     0x23082D9  8B 40 2C      mov eax, dword ptr [eax+0x2C]
//     0x23082DC  C3            ret
//     0x23082DD  83 C0 2C      add eax, 0x2C
//     0x23082E0  C3            ret
//
//     唯一调用者（栈回溯第一项 0x02711E84 已确认）在 RVA 0x2311AB4 起的那个大函数
//     里：`mov ecx,[edi+0x48]; call 0x23082D0; test eax,eax; jz ...` —— 属于
//     UI/数据取值侧的空指针解引用（`[edi+0x48]` 的 +4 字段为 0 或为整数 2），
//     与 1149 的解析没有关系。
//
// ⇒ 结论：**无法证明 1149 是凶手，且有硬反证**；把默认档调保守是"先不崩/收敛风险面"
//
//	的工程决策，不是定罪。要用一个 env 恢复今天的逐字节行为就是 `always`。
//
// ── 四档 ────────────────────────────────────────────────────────────────
//
//	login_only（默认，零值）  只在选角引导位发全量快照（账本非空时才发，reset=0）；
//	                          拾取/领取只更新账本，一个 1149 都不补发。
//	always                    改动前的逐字节行为：引导位全量 + 拾取/领取后补增量。
//	vendor_empty              引导位只发厂商实测过的那 5 字节空表 `00 00000000`，
//	                          拾取/领取同样不发增量（完全不出现非空 1149）。
//	off                       一个 1149 都不发（账本照旧正确，重登也不推）。
//
// 与既有开关的关系（互不覆盖，叠加生效）：
//
//	DNFBRIDGE_EPIC_PIECE_BOOK=off   → 碎片照旧进背包，且一个图鉴包都不发（优先级最高）
//	DNFBRIDGE_EPIC_PIECE_GRANT=off  → 领取/清扫回退到背包路径（仍可发图鉴包）
//	DNFBRIDGE_EPIC_PIECE_PUSH=…     → 只决定"发不发/发哪一档"，**不改任何账本或背包语义**
const (
	// epicPiecePushEnv 是账本下行包（class0/1149）的发送档位开关。
	epicPiecePushEnv = "DNFBRIDGE_EPIC_PIECE_PUSH"

	// epicPiecePushValueLoginOnly 是默认档：只在登录/选角引导位发全量快照。
	epicPiecePushValueLoginOnly = "login_only"
	// epicPiecePushValueAlways 是改动前行为：引导位全量 + 拾取/领取后补增量。
	epicPiecePushValueAlways = "always"
	// epicPiecePushValueVendorEmpty 是厂商逐字节形状：引导位只发 `00 00000000`。
	epicPiecePushValueVendorEmpty = "vendor_empty"
	// epicPiecePushValueOff 是一个 1149 都不发。
	epicPiecePushValueOff = "off"
)

// epicPiecePushMode 是 epicPiecePushEnv 的归一化取值。
type epicPiecePushMode int

const (
	// epicPiecePushLoginOnly 默认档（零值）：只保留引导位那次全量快照。
	epicPiecePushLoginOnly epicPiecePushMode = iota
	// epicPiecePushAlways 改动前行为：引导位全量 + 拾取/领取后的增量。
	epicPiecePushAlways
	// epicPiecePushVendorEmpty 引导位只发厂商实测的 5 字节空表。
	epicPiecePushVendorEmpty
	// epicPiecePushOff 一个 1149 都不发。
	epicPiecePushOff
)

func (mode epicPiecePushMode) String() string {
	switch mode {
	case epicPiecePushAlways:
		return epicPiecePushValueAlways
	case epicPiecePushVendorEmpty:
		return epicPiecePushValueVendorEmpty
	case epicPiecePushOff:
		return epicPiecePushValueOff
	default:
		return epicPiecePushValueLoginOnly
	}
}

// snapshotEnabled 表示引导位那次全量快照要不要发。
func (mode epicPiecePushMode) snapshotEnabled() bool {
	return mode != epicPiecePushOff
}

// forceEmptySnapshot 表示引导位快照是否强制压成厂商实测的 `00 00000000`
// （5 字节、entries=0），即账本里有进度也一个非空行都不下发。
func (mode epicPiecePushMode) forceEmptySnapshot() bool {
	return mode == epicPiecePushVendorEmpty
}

// snapshotEvenWhenEmpty 表示"账号账本为空也要发引导位快照"（厂商 7/7 条都是
// entries=0 也照发；`always` 档沿用 DNFBRIDGE_EPIC_PIECE_BOOK=always 的既有语义）。
func (mode epicPiecePushMode) snapshotEvenWhenEmpty() bool {
	return mode == epicPiecePushVendorEmpty
}

// updateEnabled 表示拾取/领取之后要不要补那条 **增量**（reset=1）账本包。
//
// 这是本次现场里唯一"零厂商先例"的包，也是默认档唯一压掉的东西。
func (mode epicPiecePushMode) updateEnabled() bool {
	return mode == epicPiecePushAlways
}

// readEpicPiecePushMode 解析 DNFBRIDGE_EPIC_PIECE_PUSH。
//
// 默认 login_only：空/未设置/非法值都按 login_only（**未知值一律往保守方向落**，
// 打错的档位不会静默退回"发增量"）；要拿回改动前的逐字节行为必须显式写 `always`。
func readEpicPiecePushMode(value string) epicPiecePushMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case epicPiecePushValueAlways, "incremental", "full", "legacy", "on", "1", "true":
		return epicPiecePushAlways
	case epicPiecePushValueVendorEmpty, "vendor", "vendor_wire", "empty", "empty_table":
		return epicPiecePushVendorEmpty
	case epicPiecePushValueOff, "0", "false", "none", "disabled", "silent":
		return epicPiecePushOff
	default:
		return epicPiecePushLoginOnly
	}
}

// epicPiecePush 返回当前 Service 的归一化档位（零值 options 的测试/探针路径 = 默认档）。
func (s *Service) epicPiecePush() epicPiecePushMode {
	if s == nil {
		return epicPiecePushLoginOnly
	}
	return s.options.epicPiecePush
}
