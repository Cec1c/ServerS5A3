package dnfbridge

import "strings"

// 史诗图鉴数量同步使用class0/op1149。2026-10-17核对现用EXE：
// 注册0x20327a0→消费者0x2008190，读取u8模式、u32行数及(id,绝对数量)对；
// 模式0先清空并按客户端目录初始化所有碎片为0，模式1覆盖指定行，随后刷新图鉴UI。
// 0x149f450直接赋值，支持减少及0，不是把传入值累加。
//
// 运行时未设置开关默认always：选角发送全量（含空账本初始化），领取、拾取及扣除
// 成功后发送绝对余额。login_only、vendor_empty、off仍可显式回退；未知值保留
// login_only。options字面量及nil Service的零值保持login_only，生产选项经解析加载。
//
// 2026-09-25曾因邮件领取后的历史崩溃暂停默认增量；同崩溃在1149上线前已发生，
// 未证实1149是原因。本轮依据当前消费者与调用链恢复同步，不改账本、邮件领取事务
// 或EXE。原生UI实际刷新仍须用户实机验收，静态核对与自动发包回归分别记录。
const (
	// epicPiecePushEnv 是账本下行包（class0/1149）的发送档位开关。
	epicPiecePushEnv = "DNFBRIDGE_EPIC_PIECE_PUSH"

	// epicPiecePushValueLoginOnly 是显式回退档：只在登录/选角引导位发全量快照。
	epicPiecePushValueLoginOnly = "login_only"
	// epicPiecePushValueAlways 是运行时默认：引导位全量及业务提交后实时覆盖。
	epicPiecePushValueAlways = "always"
	// epicPiecePushValueVendorEmpty 是厂商逐字节形状：引导位只发 `00 00000000`。
	epicPiecePushValueVendorEmpty = "vendor_empty"
	// epicPiecePushValueOff 是一个 1149 都不发。
	epicPiecePushValueOff = "off"
)

// epicPiecePushMode 是 epicPiecePushEnv 的归一化取值。
type epicPiecePushMode int

const (
	// epicPiecePushLoginOnly 字面量零值：只保留引导位那次全量快照。
	epicPiecePushLoginOnly epicPiecePushMode = iota
	// epicPiecePushAlways 运行时默认：引导位全量及业务提交后增量。
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

// snapshotEvenWhenEmpty 表示空账号是否也初始化客户端余额表。
func (mode epicPiecePushMode) snapshotEvenWhenEmpty() bool {
	// 首次增量前必须初始化客户端余额表；空账号和切换账号也清掉旧视图。
	return mode == epicPiecePushVendorEmpty || mode == epicPiecePushAlways
}

// updateEnabled 表示业务提交后是否发送模式1的绝对余额更新。
func (mode epicPiecePushMode) updateEnabled() bool {
	return mode == epicPiecePushAlways
}

// readEpicPiecePushMode 解析 DNFBRIDGE_EPIC_PIECE_PUSH。
//
// 空/未设置默认always；显式login_only保留旧行为，未知值仍回退login_only。
func readEpicPiecePushMode(value string) epicPiecePushMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", epicPiecePushValueAlways, "incremental", "full", "legacy", "on", "1", "true":
		return epicPiecePushAlways
	case epicPiecePushValueVendorEmpty, "vendor", "vendor_wire", "empty", "empty_table":
		return epicPiecePushVendorEmpty
	case epicPiecePushValueOff, "0", "false", "none", "disabled", "silent":
		return epicPiecePushOff
	default:
		return epicPiecePushLoginOnly
	}
}

// epicPiecePush 返回当前 Service 的归一化档位（options字面量零值仍为login_only）。
func (s *Service) epicPiecePush() epicPiecePushMode {
	if s == nil {
		return epicPiecePushLoginOnly
	}
	return s.options.epicPiecePush
}
