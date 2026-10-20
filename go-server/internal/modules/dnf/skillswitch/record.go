package skillswitch

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 2026-09-30: 以下是兼容回退用的历史读法，不代表当前 EXE 的真实协议。
// RVA 3C9FA0 已证实前两字节为所选技能；新开关启用时由 buffswap.Parse
// 读取完整六字节，绝不进入本包。参见 _dist/buff-swap-native-reapply.md。
// 本文件是旧 op1428 上行**包体**的逐字段读法，并且是两种布局
// （v4 现行 = 修复档 / v6 旧读法 = 一键回退档）的唯一实现处。
//
// # 现行（正确）读法：包体 4 字节（LayoutV4，**默认档**）
//
//	off 0    u8      Slot   实测 {3,9,12..26,33}
//	off 1    u8      Mode   实测 {0x00(61 条),0x01(1),0x03(9),0x2C=44(8)}
//	off 2..3 u16 LE  Value  实测 {0x0003..0x0040} ∪ {0xFFFF 哨兵}
//
// 证据（2026-09-28 实机只读测量，`_dist/buff-pairs-measurement.md` §4；原始转储
// `_dsh_tmp/buff-pairs-evidence/scan3.out.txt` 的 `HIT 0x3319F92A` / `0x3319F950` /
// `0x3319F970` / `0x3319F983` / `0x3319F996`）：运行中的 DNF.exe（PID 42996，
// sha256 `8A7AF614…AA4D`）内存 VA `0x3319F900` 起是**客户端自己的报文缓冲**，
// 里面是连续的 op1428 上行帧；帧长算术把包体长度钉死：
//
//	01 94 05 13 00 00 00 | 16 f5 5a e3 | 20 00 00 00 | 10 03 10 00
//	└ 头 7B（len=0x13=19）┘  crc32 4B      seq u32 4B     └ 包体 4B ┘
//
// 7 + 4 + 4 + 4 = 19 = 0x13 ⇒ **包体 4 字节**，不是 6。
// `10 03 10 00` = Slot 16 / Mode 3 / Value 16；`0c 03 0c 00` = Slot 12 / Mode 3 / Value 12。
//
// # 旧（错误）读法：把 6 字节当成包体（LayoutV6，一键回退档）
//
//	off 0..1 u16 LE  WirePrefix  ← **不是包体**：传输帧 u32 序号域的**高 16 位**
//	off 2    u8      Slot
//	off 3    u8      Mode
//	off 4..5 u16 LE  Value
//
// 那 2 个字节是怎么混进来的（本仓 `internal/modules/dnf/protocol/legacy_game.go`）：
// `ParseLegacyGamePacket` 用 **13 字节头**、`Sequence` 读作 **u16**（`packet[11:13]`），
// 而客户端这条上行在 `[11:15]` 放的是 **u32 序号**（19 字节帧 = 11 头 + 4 序号 + 4 包体）。
// 于是 `packet[13:]` 交给 handler 的 6 字节切片 =
// **序号高 16 位（2B） + 真包体（4B）**。
// 例：帧 `… 20 00 00 00 | 10 03 10 00` ⇒ handler 收到 `00 00 10 03 10 00`，
// 与库内 `slot_16=000010031000` 逐字节相等（`_dist/buff-skill-switch-diagnosis.md` §4.6
// 的 18 条落库记录全同此形）。
//
// # 旧 6 字节行 → 新 4 字节包体：映射是**确定的**，不需要数据迁移
//
//	旧行 [p0 p1 b0 b1 b2 b3]  --去掉前 2 个非包体字节-->  包体 [b0 b1 b2 b3]
//	`00000c030c00` → `0c030c00`  (Slot 12 / Mode 3  / Value 12)
//	`000012031200` → `12031200`  (Slot 18 / Mode 3  / Value 18)
//	`00000e030e00` → `0e030e00`  (Slot 14 / Mode 3  / Value 14)
//	`00000f030f00` → `0f030f00`  (Slot 15 / Mode 3  / Value 15)
//	`000010031000` → `10031000`  (Slot 16 / Mode 3  / Value 16)
//	`2600212cffff` → `212cffff`  (Slot 33 / Mode 44 / Value 0xFFFF 哨兵)
//
// 判据：库内 18 条按此重读**全部自洽**——三种形态的硬不变量一条都没破
// （`ModeConfirm` 的 Slot == Value、`ModeRegister` 的 Value ∈ 54..64、
// `ModeClear` 的 Value == 0xFFFF），且与实机缓冲里客户端**自己发的** 4 字节包体同形。
//
// 因此 **dual-read** 就是"去掉/补回那 2 个非包体字节"，没有任何字段被重新解释；
// 但**绝不静默改写已落库的行**：v4 档只有在**再次收到同一条记录**时才会把该行
// 改写成 4 字节规范形，并在这条落库日志上打 `layout_upgraded=true`（见 store.go 的 Apply）。
//
// # 严格性
//
// 判长只看**已知长度**，其余一律 `ErrRecordShape`：
//
//   - v4 档：4（规范包体）或 6（现行分帧的「2 非包体字节 + 4 真包体」）——这就是 dual-read；
//   - v6 档（回退 = 逐字节今天）：只接受 6。
//
// 两种长度都不会被当成"可能是别的版本"静默吞掉。
//
// # 仍未确证（不要在别处引用成结论）
//
// Slot / Mode / Value 的**语义**里，只有形状与哨兵位是实测钉死的；
// `Value` 的取值域 `54..64` 与"快捷栏槽位空间"的对应、以及下行 `count` 是否等于
// 实际项数，仍是推断（见 `_dist/buff-pairs-measurement.md` §6、§8）。
const (
	// RecordBodySize 是 op1428 上行**包体**的真实长度（实机帧长算术钉死）。
	RecordBodySize = 4
	// RecordWirePrefixSize 是**本仓现行分帧**在真包体前多留的字节数
	// （客户端 u32 序号域的高 16 位）。它不属于包体，不带任何语义。
	RecordWirePrefixSize = 2
	// RecordWireSize 是 handler 实际收到的切片长度（= 2 前缀 + 4 包体）。
	// 现场 82/82 条 RECV 的 `body_len` 都是它。
	RecordWireSize = RecordWirePrefixSize + RecordBodySize
)

// Layout 是 op1428 上行包体的字段布局读法。
type Layout string

const (
	// LayoutV4 是 2026-09-28 实机证实的真实包体形状：
	// `u8 Slot | u8 Mode | u16 Value LE`（4 字节）。**默认档 = 修复档。**
	LayoutV4 Layout = "v4"
	// LayoutV6 是 2026-09-28 之前本仓的读法：把分帧残留的 2 字节也算进包体
	// （6 字节 = 2 前缀 + 4 真包体）。**一键回退档**，逐字节复现改动前的行为。
	LayoutV6 Layout = "v6"
)

// NormalizeLayout 归一化 `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY`。
//
// 默认（空 / `v4` / `fix` / `on` / 未知取值）= `LayoutV4` = 修复档；
// 写 `v6`（别名 `legacy` / `today` / `old` / `6`）= `LayoutV6` = 逐字节回到改动前。
// 与 "默认档不能被别名破坏" 的约定一致：未知取值一律落到**修复档**。
func NormalizeLayout(value string) Layout {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(LayoutV6), "legacy", "today", "old", "6":
		return LayoutV6
	default:
		return LayoutV4
	}
}

// String 让 Layout 能直接进日志/启动回显。
func (l Layout) String() string {
	if l == LayoutV6 {
		return string(LayoutV6)
	}
	return string(LayoutV4)
}

const (
	// ModeRegister 是「双槽配对」形态（61 条）：`Slot ∈ {12..23}\{13}`、
	// `Value ∈ {54..64}`，2026-09-26 01:21 的 `header_seq=567..577` 11 连发是一段
	// 11 条的**双射配对**（Slot 各出现一次、Value 各出现一次，且该形态在
	// 4 次会话里以完全相同的形态重复出现）⇒ `Value` 是绑定槽、`Slot` 是被引用的号。
	// v4 包体样本：`0c 00 36 00`（旧 6 字节行 `00000c003600`）。
	ModeRegister byte = 0x00

	// ModeConfirm 是「单槽自配对」形态（9 条，含操作者给的 4 条现行样本）：
	// 不变量 `Slot == Value`（9/9）。两个字段携带同一个值。
	// v4 包体样本：`0c 03 0c 00`（旧 6 字节行 `00000c030c00`）。
	ModeConfirm byte = 0x03

	// ModeClear 是「枚举哨兵」形态（8 条）：不变量 `Slot ≡ 0x21`、`Value ≡ 0xFFFF`、
	// `Mode ≡ 0x2C`（8/8）。它出现在一段连发的收尾（例：2026-09-26 15:51 那段从
	// `header_seq=35` 起、最后一条就是它）。
	//
	// v4 包体 = `21 2c ff ff`（旧 6 字节行 `2600212cffff`）。**按 4 字节读法，
	// `0xFFFF` 落在 u16 Value 上，才是自洽的哨兵位**；按旧 6 字节读法它是
	// `slot_b=0xFFFF`，同样成立，但前 2 字节 `26 00` 会被误当成 `scope=38`——
	// 实机证明那只是序号高 16 位。
	//
	// **不把它当成"清空登记"**：它 8/8 出现在真实记录的**后面**，若按清空处理，
	// 每一次登记都会被紧随其后的哨兵立刻抹掉。本包只把它记成
	// `sentinel_<hex>` 取证键，不动任何 `slot_*` 键（见 store.go）。
	ModeClear byte = 0x2C

	// ModeUnread 只有 **1 条**样本（旧 6 字节行 `260003013700`，= v4 包体
	// `03 01 37 00`：Slot=3、Value=55）。样本不足，本包不对它做任何语义声明，
	// 只当普通记录留存。
	ModeUnread byte = 0x01
)

// ValueNone 是 `Value` 的「空/无记录」哨兵（0xFFFF）；8/8 条与 `ModeClear` 同现，
// 其余 74 条从未出现。
//
// 旧名 `SlotNone`：那是 6 字节读法下的字段名（`SlotB`）。按实机证实的 4 字节读法，
// `0xFFFF` 落在 u16 **Value** 上，故改名；取值与判定完全不变。
const ValueNone uint16 = 0xFFFF

var (
	// ErrStoreUnavailable 表示 settings 仓储缺失（内存仓储被裁剪的测试、
	// 或装配期故障）。调用方按 fire-and-forget 处理：只记事件，不升级成会话错误。
	ErrStoreUnavailable = errors.New("skill switch settings storage is unavailable")
	// ErrCharacterRequired 表示会话还没有解析出角色。
	ErrCharacterRequired = errors.New("selected character id required")
	// ErrCharacterNotFound 表示角色记录不存在。
	ErrCharacterNotFound = errors.New("character record not found")
	// ErrCharacterOwner 表示会话账号与该角色不属于同一账号。
	ErrCharacterOwner = errors.New("selected character does not belong to account")
	// ErrRecordShape 表示包体长度不是该布局允许的长度
	// （v4：4 或 6；v6：6），或重建失败。长度不符必须具名拒绝。
	ErrRecordShape = errors.New("skill switch record shape is invalid")
	// ErrSkillUnknown 是**具名拒绝**：这条记录引用的技能号在该角色 job 的
	// PVF 技能目录里不存在。整条记录不落库（其余已存记录不受影响）。
	ErrSkillUnknown = errors.New("skill switch references a skill absent from the job-scoped PVF skill catalog")
	// ErrRecordLimit 表示该角色的去重记录数已达 MaxRecords 安全上界。
	ErrRecordLimit = errors.New("skill switch record limit reached")
)

// Record 是一条 op1428 记录的逐字段读法。
type Record struct {
	// WirePrefix 只在读自**旧 6 字节形**（`LayoutV6`，或 v4 档下现行分帧交来的
	// 6 字节切片）时有值：那是传输帧 u32 序号域的高 16 位，**不是包体字段、
	// 不带任何语义、不参与任何判定**（只进日志与取证）。
	// 旧字段名 `Scope` 的"作用域"含义已被 2026-09-28 的实机证据证伪。
	WirePrefix uint16
	// Slot 是包体 off0（旧名 `SlotA`）。`SkillID` 从这里取（判据见 SkillID）。
	Slot byte
	// Mode 是包体 off1，取值见 Mode* 常量。
	Mode byte
	// Value 是包体 off2..3（旧名 `SlotB`）。`ModeRegister` 下是被绑定的槽号，
	// `ModeConfirm` 下与 `Slot` 同值，`ModeClear` 下是 `ValueNone`。
	Value uint16
	// Layout 是这条记录**读出来**的布局。它决定 Bytes()/Hex() 的字节形
	// （v6 回退档必须逐字节等于改动前），也决定投影回放的字节形。
	Layout Layout
}

// ParseRecord 按**默认档**（`LayoutV4`）读一条 op1428 包体切片。
// 接受 4 字节规范包体，也接受现行分帧交来的 6 字节切片（dual-read，去掉前 2 字节）。
func ParseRecord(body []byte) (Record, error) {
	return ParseRecordIn(LayoutV4, body)
}

// ParseRecordIn 按指定布局读一条 op1428 包体切片。**长度不符一律 ErrRecordShape**：
//
//	LayoutV4: len == 4（规范包体）或 len == 6（2 非包体字节 + 4 包体）
//	LayoutV6: len == 6
func ParseRecordIn(layout Layout, body []byte) (Record, error) {
	if layout == LayoutV6 {
		if len(body) != RecordWireSize {
			return Record{}, fmt.Errorf("%w: layout=v6 len=%d want=%d",
				ErrRecordShape, len(body), RecordWireSize)
		}
		return recordFromWireBytes(body, LayoutV6), nil
	}
	switch len(body) {
	case RecordBodySize:
		return Record{
			Slot:   body[0],
			Mode:   body[1],
			Value:  binary.LittleEndian.Uint16(body[2:4]),
			Layout: LayoutV4,
		}, nil
	case RecordWireSize:
		// dual-read：现行 13 字节分帧交来的 6 字节 = 序号高 16 位(2B) + 真包体(4B)。
		// 去掉前 2 字节即可；被去掉的字节留在 WirePrefix 里，不丢证据。
		return recordFromWireBytes(body, LayoutV4), nil
	default:
		return Record{}, fmt.Errorf("%w: layout=v4 len=%d want=%d_or_%d",
			ErrRecordShape, len(body), RecordBodySize, RecordWireSize)
	}
}

func recordFromWireBytes(body []byte, layout Layout) Record {
	return Record{
		WirePrefix: binary.LittleEndian.Uint16(body[0:2]),
		Slot:       body[2],
		Mode:       body[3],
		Value:      binary.LittleEndian.Uint16(body[4:6]),
		Layout:     layout,
	}
}

// InLayout 返回同一组字段在另一种布局下的视图（**只换字节形，不换字段**）。
// `WirePrefix` 原样保留：它只影响 `LayoutV6` 的 `Bytes()`，v4 的字节形里没有它。
func (r Record) InLayout(layout Layout) Record {
	r.Layout = layout
	return r
}

// SameFields 判断两条记录是否是同一组字段（**忽略布局与导线前缀**）。
// 落库时用它判定「旧 6 字节行被改写成新 4 字节规范形」，而不是"内容变了"。
func (r Record) SameFields(other Record) bool {
	return r.Slot == other.Slot && r.Mode == other.Mode && r.Value == other.Value
}

// Bytes 重建这条记录的字节形：`LayoutV4` → 4 字节包体；`LayoutV6` → 6 字节
// （2 字节导线前缀 + 4 字节包体，逐字节等于改动前）。投影时**原样回放**这些字节
// （与 op447 `frame_source=op447_verbatim_frame` 同口径），不重新编码字段。
func (r Record) Bytes() []byte {
	if r.Layout == LayoutV6 {
		raw := make([]byte, RecordWireSize)
		binary.LittleEndian.PutUint16(raw[0:2], r.WirePrefix)
		raw[2] = r.Slot
		raw[3] = r.Mode
		binary.LittleEndian.PutUint16(raw[4:6], r.Value)
		return raw
	}
	raw := make([]byte, RecordBodySize)
	raw[0] = r.Slot
	raw[1] = r.Mode
	binary.LittleEndian.PutUint16(raw[2:4], r.Value)
	return raw
}

// Hex 是这条记录的规范化键名（含布局；v4 = 8 个 hex 字符，v6 = 12 个），
// 与大小端无关地稳定，用于幂等落库。
func (r Record) Hex() string {
	return hex.EncodeToString(r.Bytes())
}

// Describe 是人类可读的落库值（取值确定、便于线上比对）。
//
// 字段名沿 4 字节读法改名（`slot_a`→`slot`、`slot_b`→`value`、`scope`→`wire_prefix`）：
// 旧名是 6 字节读法的名字，其中 `scope` 一项已被实机证据证伪。
func (r Record) Describe() string {
	return "wire_prefix=" + strconv.Itoa(int(r.WirePrefix)) +
		" slot=" + strconv.Itoa(int(r.Slot)) +
		" sub_mode=0x" + strconv.FormatUint(uint64(r.Mode), 16) +
		" value=" + strconv.Itoa(int(r.Value))
}

// Sentinel 表示这条记录是 `ModeClear` 形态的枚举哨兵（见 ModeClear 的说明）。
func (r Record) Sentinel() bool {
	return r.Mode == ModeClear
}

// SkillID 返回这条记录引用的 PVF 技能号，以及该记录**是否声明了技能号**。
//
// 判据（**这是本包唯一一处语义推断，逐条给反例边界**）：
//
//   - `Slot` 是三种形态里唯一取值集合**完整落在真实 job-scoped PVF 技能号空间**的字段：
//     现场 Slot 集合 = {3,9,12,13,14,15,16,17,18,19,20,21,22,23,26,33}，
//     以真档 `H:\86JP启动器\90CN\地下城与勇士\Script.pvf`（= 运行期 PVF）实测，
//     这 16 个值在 job 11 / 14 等主流 job 的技能目录里**都存在**
//     （`skill/skilllist.lst` 展开后 job 11 的 id<40 段 = 1..39 连续，
//     job 14 = 1,2,3,5,6,7,8,9,11..24,26..39）。
//   - `Value` 不能当技能号：`ModeRegister` 下它取 `54..64`，正好落在
//     `dnf_skill_layouts.slot_index` 的 `54..65` 连续段（HK 角色 3 实测），
//     即**快捷栏槽位空间**；`ModeClear` 下它是 `0xFFFF` 哨兵。
//   - `Slot` 也**不是**列表下标：同一段 11 连发里 Slot 与 Value 各自互不相同，
//     是双射而非递增下标。
//
// **未确证**：`Slot` 是技能号这一点没有客户端侧证据（`90CN_protocol.log` 只覆盖
// 2026-09-27 06:44→12:29，四次 op1428 会话全在区间外）。因此 `SkillID` 只用于
// **PVF 存在性门禁 + 日志命名**；未知技能号按 ErrSkillUnknown 拒绝，
// 但**不做任何需要理解语义的换算**（不发明等级、不发明槽位）。
//
// `ModeClear` 形态不声明技能号（`ok=false`）。
func (r Record) SkillID() (uint16, bool) {
	if r.Sentinel() {
		return 0, false
	}
	return uint16(r.Slot), true
}

// BoundSlot 返回这条记录绑定的槽号，以及该记录是否携带绑定槽。
// `ModeRegister` 是 `Value`；`ModeConfirm` 下 `Value` 与 `Slot` 同值（现场 9/9）。
func (r Record) BoundSlot() (uint16, bool) {
	if r.Sentinel() {
		return 0, false
	}
	return r.Value, true
}

// BindingBearing 报告这条记录是否**携带绑定信息**（它指名的目标与自己的键不同）。
//
// 现场两种形态在这一点上泾渭分明（`_dist/buff-swap-round3.md` §1，2026-09-28 实测）：
//
//	ModeRegister（`Slot ∈ {12..23}`、`Value ∈ {54..64}`）→ **是**：`Value != Slot`，
//	  这条记录比"这个槽登记了"多说一半信息（绑定的目标槽）；
//	ModeConfirm（`Slot == Value`，现场 18/18）→ **否**：`Value` 只是把键抄了一遍，
//	  它**只**声明"这个槽在集合里"，不含任何绑定目标。
//
// 该判据是 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND` 的唯一根据：一条不含绑定信息的
// 记录既不许**覆盖**已落库的绑定行，也不许在窗口提交时**删掉**别人的绑定行
// （2026-09-28T17:12:48Z char 6 那一次两件事都发生了，见 §1/§2）。
func (r Record) BindingBearing() bool {
	return !r.Sentinel() && r.Value != uint16(r.Slot)
}
