package skillswitch

import (
	"context"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是 op1428「**登记窗口**」的唯一实现处
// （`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`）。
//
// # 它修的是什么（操作者第四次报障：选择角色后刚选择的装备没有登记）
//
// store.go 的 `Apply` 只做一件事：把每条记录写进 `slot_<Slot>`。于是**状态键集合
// 只增不减**——它是"这台服务器见过的所有槽位的并集"，不是"客户端最后一次登记"。
// 现场实证（HK `192.0.2.11`，`packet_log.txt`，2026-09-28）：
//
//	char 6 = `test78`（job 0）最后一次完整登记在 13:52:42.240..44.977Z，
//	客户端推上来的是 **11 个槽位** {12,14,15,16,17,18,19,20,21,22,23}；
//	而 `character:6:skill_switch_inventory` 里躺着 **14 条** `slot_*` 行——
//	`slot_3` / `slot_13` / `slot_25` 是更早两次登记留下的、操作者这次**没有选**的槽位。
//	13:52:48.9 与 13:52:49.0 两条边界上的重下发帧因此带着
//	`pairs=(3,3)(12,12)(13,13)...(25,25)`——把**没选的**也一起登记回去了。
//
// # 客户端给的分隔符：`ModeClear` 哨兵
//
// 客户端每次登记都是"**一次完整集合**"（现场 3 次登记里的 27 条包全部自洽：
// char 3 @14:26 推 9 条 = 当时全部 9 个槽位，其中 4 条是原样重发的 `replayed=true`；
// char 6 @13:52 推 11 条 = 当时全部 11 个槽位），并且**每次登记都带 `21 2c ff ff`
// 哨兵**（现场 5/5 段登记都有它；09:03 / 09:08 / 13:52 char6、13:52 / 14:26 char3）。
//
// 因此本档把哨兵当作**登记窗口的分隔符**，而不是"清空登记"：
//
//		记账开关 = 这个角色**见过至少一次哨兵**（`window_armed=1`，第一次哨兵时写下）
//		窗口     = 上一个分隔符之后收到的记录（`window_<slot>` 键，随每一条记录落库）
//		提交     = 把状态键集合**换成**窗口集合（窗口外的 `slot_*` 行删掉 ⇒ 移除真的生效）
//
//	  - **第一次**哨兵只打开记账（那次提交面对的是空窗口 ⇒ 一个 `slot_*` 都不动）；
//	  - 之后的哨兵关闭窗口：窗口里有记录 ⇒ 提交；**空窗口永不提交**（09:08:27/28 那两条
//	    只带哨兵、后面没有记录的包**不会**把换装表清空——这是上一轮拒绝"当清空处理"
//	    时担心的那件事，这里用"空窗口不提交"显式挡住）。
//	  - 重投影边界也关闭窗口：现场 13:52 char6 那段登记**只有开头一个哨兵**，
//	    没有收尾哨兵，只靠哨兵提交会让移除晚一次登记才生效。边界提交让它当场生效。
//	  - **没有哨兵 ⇒ 一个键都不新增**：没见过哨兵的角色既没有 `window_*` 键、
//	    `CommitRegistration` 也直接短路 ⇒ 该客户端的落库与下行逐字节等于改动前
//	    （某些客户端版本可能根本不发哨兵；这条路径是**真的回退档**，不是死代码）。
//
// # 截断（burst 中途掉线）时会发生什么（明写，不藏）
//
// 客户端**没有**给"这段集合推完了"的信号（收尾哨兵 5 段里只有 1 段有），所以
// "操作者移除了 X"与"这段推送被截断、X 还没发上来"在报文层面**不可区分**。
// 本档的选择与后果：
//
//   - 窗口**打开期间**状态键只增不减（新记录照旧立刻落 `slot_*`）⇒ 进程中途死掉、
//     窗口从未提交时，状态是**并集**，永远不会只剩半截——这是刻意保留的加法安全；
//   - 窗口一旦提交就是**替换**：截断的那一段若被提交，集合会等于"已收到的前缀"，
//     被截掉的那些槽位这一轮会从状态里消失（客户端的下一次完整登记会把它补回来，
//     因为客户端每次都推全集）；
//   - 不接受这种残余风险时用 `sentinel_only` 档：**只认哨兵分隔符**，边界不提交
//     （代价是"移除"要等下一次登记才生效，而且从没收到收尾哨兵时可能一直不生效）；
//   - `union` 是一键回退字面量：与改动前逐字节相同（不写 `window_*`、不提交、不删）。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. 哨兵 `21 2c ff ff` 的**字段语义**仍无客户端证据（只知它 5/5 段出现、
//     且 4/5 段在**第一条记录之前**出现）。本档只依赖"它是这段登记的分隔符"这一条
//     可观测性质，不依赖它到底是"开始"还是"结束"：规则写作"上一个分隔符之后的记录
//     构成一段"，两种读法得到**同一批提交**（见 `TestRegistrationWindowSentinelFirstOrLast`）。
//  2. 若客户端在某些会话里**只推增量**而不是全集，替换语义会删掉没重发的槽位。
//     现场 3 次登记都是全集（含原样重发的 `replayed=true` 行），但样本只有 3 次。
const RegistrationModeEnv = "DNFBRIDGE_SKILL_SWITCH_REGISTRATION"

const (
	// RegistrationWindow 是**默认档（修复档）**：哨兵 + 重投影边界都关闭窗口，
	// 提交语义 = 替换。
	RegistrationWindow = "window"
	// RegistrationSentinelOnly 是**保守档**：只认哨兵分隔符，边界不提交。
	// 用在"担心 burst 被截断、宁可让移除晚一轮生效"的场合。
	RegistrationSentinelOnly = "sentinel_only"
	// RegistrationUnion 是**一键回退字面量**：与改动前逐字节相同
	// （本文件不写任何键、不提交、不删，`Apply` 也跳过整个窗口记账）。
	RegistrationUnion = "union"
)

// NormalizeRegistrationMode 归一化 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION`。
//
// 口径与 `NormalizeLayout` / `normalizeSkillSwitchLoadoutResendMode` 一致：
// **只有回退/保守字面量才离开修复档**，未设置 / 空 / 未知取值一律 `window`。
func NormalizeRegistrationMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationUnion, "off", "today", "legacy", "rollback", "additive", "0", "disable":
		return RegistrationUnion
	case RegistrationSentinelOnly, "sentinel", "sentinels", "safe", "conservative", "no_boundary_commit":
		return RegistrationSentinelOnly
	default:
		return RegistrationWindow
	}
}

// CurrentRegistrationMode 直接自读 env（本档刻意不进 options.go，与
// `skillSwitchLoadoutResendMode` 同一口径）。
func CurrentRegistrationMode() string {
	return NormalizeRegistrationMode(os.Getenv(RegistrationModeEnv))
}

// RegistrationArmEnv 是"**登记窗口从哪一刻开始记账**"这一个开关
// （`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）。
//
// # 它修的是上一版留下的那个"提交路径从没跑过一次"的缺陷（操作者第五次报障）
//
// 上一版的记账开关是"这个角色**见过至少一次哨兵**才记窗口"（`window_armed`，
// 在**收到哨兵那一刻**才写下）。而现场 2026-09-28T15:35 那次登记的形状是
// "**8 条记录 + 收尾一个哨兵**"（`packet_log.txt` 第 140899..140997 行）：
//
//	15:35:02.929..15:35:05.201Z  8 条 op1428 记录（全部 Mode 3，槽位 13/15/16/17/18/19/20/21）
//	15:35:06.848Z                1 条 `21 2c ff ff` 哨兵（**最后一个包**）
//	15:35:09.908Z                重投影边界（选角后重新进入 -> 重下发）
//
// 那 8 条到达时 `window_armed` **还不存在**——该角色上一次哨兵（14:26:13.673Z）
// 是**旧二进制**处理的，只留下了 `sentinel_212cffff` 取证键（SQL 实证见
// `_dist/buff-swap-registration-commit.md` §2），于是这 8 条一条都没进窗口
// （8 条 `recorded` 行的 `registration_window_count=0` 就是它）；等收尾哨兵到场时
// 才 arm，`commitRegistrationWindow` 面对的是**空窗口** ⇒ 不提交；到 15:35:09.908Z
// 的重投影边界，窗口**仍然是空的** ⇒ 还是不提交。净效果：全日志
// `game-skill-switch-inventory-registration-commit` **0 条**，库里仍是并集
// （`slot_14` = 操作者刚刚在面板上移除的那个槽位还躺在 `character:3` 里）。
//
// # 本档的规则（默认 = 修复档）
//
//	arm 之前那一段 burst -> `pending_<slot>` 键（未 arm 时**都**记；
//	                          见下面"记账键"那一段）
//	重投影边界            -> 窗口为空而 `pending_*` 非空时，先把 pending **提升**成窗口
//	                          再提交（trigger=`boundary_pre_arm_burst`）
//	连续两个分隔符        -> 中间没有记录时把 pending 丢掉（这条哨兵后面没有登记内容，
//	                          见 `Apply` 里的 `armedBefore`）
//
// # 为什么要记 `pending_*`，以及它与"没有哨兵 ⇒ 逐字节等于改动前"的关系
//
// `pending_*` 是**纯记账键**：`Load` / `countEntries` / `countForensicEntries` /
// `isRecordFrameKey` 都不认它，它既不进 `slot_*` 集合、也不进下行帧、也不参与
// `slot_count` / `record_count`。而**提交**这一步（`CommitRegistration` 与哨兵提交）
// 都以 `window_armed` 为前提，`window_armed` 只有**真的收到哨兵**才会写下：
//
//   - 从不发哨兵的客户端：`pending_*` 会随记录增长，但 `window_armed` 永远是空、
//     `CommitRegistration` 在 `windowArmed` 那里直接短路 ⇒ **一个 `slot_*` 都不会被删、
//     下行帧与改动前逐字节相同**（钉在 `TestSkillSwitchRegistrationNoSentinelIsByteForByteToday`）；
//   - 需要连 `pending_*` 这几个记账键都不出现的场合：`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM=sentinel`
//     （回退字面量 = 线上当前这一版的字节）。
//
// 回退字面量 `sentinel` 复现的是**线上当前这一版的字节**（arm 之前什么都不记），
// 不是改动前那一版（那是 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION=union`）。
const RegistrationArmEnv = "DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM"

const (
	// RegistrationArmRecord 是**默认档（修复档）**：arm 之前那一段 burst 记进
	// `pending_*`，重投影边界上窗口为空时提升并提交。
	RegistrationArmRecord = "record"
	// RegistrationArmSentinel 是**一键回退字面量**：arm 之前那一段什么都不记，
	// 只有见过哨兵之后才有窗口 —— 与线上当前这一版**逐字节相同**。
	RegistrationArmSentinel = "sentinel"
)

// 提交触发点：`RegistrationCommit.Trigger` 与日志字段 `commit_trigger` 的取值。
// 三者互斥，写出来是为了让"这一次到底是哪条边界提交的"在日志上不需要推算。
const (
	// RegistrationTriggerSentinel：`Apply` 收到哨兵时关闭窗口，且窗口里有记录。
	RegistrationTriggerSentinel = "sentinel_delimiter"
	// RegistrationTriggerBoundaryWindow：重投影边界关闭仍然打开的窗口（窗口里有记录）。
	RegistrationTriggerBoundaryWindow = "boundary_open_window"
	// RegistrationTriggerBoundaryPreArmBurst：重投影边界上窗口为空、但 arm 之前
	// 收到过一段 burst ⇒ 提升 `pending_*` 成窗口再提交。**本档修的就是这一条**。
	RegistrationTriggerBoundaryPreArmBurst = "boundary_pre_arm_burst"
)

// NormalizeRegistrationArm 归一化 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`。
//
// 口径与 `NormalizeRegistrationMode` 一致：**只有回退字面量才离开修复档**，
// 未设置 / 空 / 未知取值一律 `record`。
func NormalizeRegistrationArm(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationArmSentinel, "armed", "window_only", "no_pending", "today", "live",
		"legacy", "rollback", "0", "off", "disable":
		return RegistrationArmSentinel
	default:
		return RegistrationArmRecord
	}
}

// CurrentRegistrationArm 直接自读 env（与 `CurrentRegistrationMode` 同一口径）。
func CurrentRegistrationArm() string {
	return NormalizeRegistrationArm(os.Getenv(RegistrationArmEnv))
}

// RegistrationBindEnv 是"**不含绑定信息的记录能不能改掉/删掉绑定**"这一个开关
// （`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`，默认 = 修复档）。
//
// # 它修的是什么（操作者第六次报障：「1 buff强化为空。登记成功后切换角色清空。」）
//
// 现场（HK `192.0.2.11`，`packet_log.txt`，char 6 = `test78`，account `dnf:10011`）：
//
//	16:15:41.501Z  边界提交 `commit_trigger=boundary_open_window`
//	               → 落库 **11 行 ModeRegister**：`slot_12=0c003600`（Value 54）、
//	                 `slot_14=0e003e00`（62）、`slot_15=0f003d00`（61）…
//	                 `slot_23=17003700`（55）——`Value` 就是 list 1 里那 11 件换装物品的槽位
//	                 （`_dist/weapon-state-final.md` §10.2 已把这一条钉成实测）。
//	16:58–17:12    四次选角/进场景边界都原样重发这 11 对（`loadout_rows=11`）。
//	17:12:45–47Z   客户端推上来 **9 条 ModeConfirm**（`Slot == Value`）：
//	               `000011031100`(17,17) `000012031200`(18,18) `00000e030e00`(14,14) …
//	               —— 只覆盖 {14..22}，**一个绑定目标都没带**。
//	17:12:48.311Z  一条 `ModeClear`（`record_hex=052cffff`，Slot=5/Mode=0x2c/Value=0xffff）
//	               关闭窗口 ⇒ `registration_committed=true registration_kept_count=9
//	               registration_dropped_count=2 dropped_entries=slot_12=0c003600,slot_23=17003700`。
//
// 净效果（SQL 实证，`character:6:skill_switch_inventory`）：
//
//	slot_14..slot_22 的 `Value` 被**覆盖**成 14..22（绑定的目标槽 54..64 全丢），
//	slot_12 / slot_23 被**删除**；此后每一次重发都是
//	`pairs=(14,14)(15,15)…(22,22)` —— map 的 value 不再指向 list 1 里那 11 件换装物品，
//	面板因此为空（操作者原话"buff强化为空"），而这一切发生在**登记那一刻**
//	（17:12:48Z），比"切换角色"（17:12:53Z）早 5 秒。
//
// # 本档的规则（默认 = 修复档 `keep`）
//
//	一条 `Value == Slot` 的记录（不含绑定信息）：
//	  写入时 → **不覆盖**同槽位上已落库的**携带绑定**的行（该行原样保留）；
//	  窗口提交时 → 窗口里一条携带绑定的记录都没有 ⇒ 提交**只删**那些本身就不含
//	               绑定信息的行；携带绑定的行即使不在窗口里也**保留**
//	               （它们只在"这次登记自己声明了绑定"时才会被替换/删除）。
//
// 三条硬性质：
//
//  1. **移除照旧生效**：窗口里全是 `Value == Slot` 的记录时（现场 char 3 @15:35
//     那段 8 条就是这一形），窗口外的**无绑定**行照删 ⇒ 操作者仍然能移除槽位
//     （`TestRegistrationWindowCommitAppliesRemovals` 与
//     `TestSkillSwitchRegistrationPreArmBurstCommitsOnSingleSentinel` 保持绿色）；
//  2. **"没有哨兵 ⇒ 逐字节等于改动前"不受影响**：提交仍以 `window_armed` 为前提，
//     本档只在**已经会提交**的那条路径上改变"删哪些行"；
//  3. **空窗口永不提交**不变：`Value == Slot` 的记录照样进窗口（`window_count` 计入），
//     所以"裸哨兵不能清空换装表"的护栏原封不动。
//
// 回退字面量 `replace` 复现的是**线上当前这一版的字节**（覆盖 + 全量替换删行）。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. `Value == Slot` 是"只声明集合"还是"声明绑定到槽位 N 自己"，**没有客户端侧证据**；
//     本档只依赖它的可观测形状（现场 18/18 `Slot == Value`，从不携带独立目标）；
//  2. 因此"玩家把一个**已绑定**的槽位从登记里去掉"这件事，在 `keep` 档下只能由
//     一次**携带绑定**的登记来表达（客户端现场三次登记里两次是这一形）。
//     若某天实测证明客户端只用 `Value == Slot` 表达移除，用
//     `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND=replace` 一个字面量即可回到今天。
const RegistrationBindEnv = "DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND"

const (
	// RegistrationBindKeep 是**默认档（修复档）**：不含绑定信息的记录既不覆盖
	// 已落库的绑定行，也不在窗口提交时删除它们。
	RegistrationBindKeep = "keep"
	// RegistrationBindReplace 是**一键回退字面量**：与线上当前这一版**逐字节相同**
	// （同槽位覆盖 + 窗口全量替换）。
	RegistrationBindReplace = "replace"
)

// NormalizeRegistrationBind 归一化 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`。
//
// 口径与 `NormalizeRegistrationArm` 一致：**只有回退字面量才离开修复档**，
// 未设置 / 空 / 未知取值一律 `keep`。
func NormalizeRegistrationBind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationBindReplace, "replace_all", "overwrite", "today", "live",
		"legacy", "rollback", "0", "off", "disable":
		return RegistrationBindReplace
	default:
		return RegistrationBindKeep
	}
}

// CurrentRegistrationBind 直接自读 env（与 `CurrentRegistrationArm` 同一口径）。
func CurrentRegistrationBind() string {
	return NormalizeRegistrationBind(os.Getenv(RegistrationBindEnv))
}

// BindingRestoreEnv 是"**一条不含绑定信息的登记行能不能被客户端自己上一次的绑定声明补回来**"
// 这一个开关（`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`，默认 = 修复档）。
//
// # 它修的是什么（`keep` 上线之后运维仍然报"选择角色后为空"，第四轮现场）
//
// `keep` 档只保证"不含绑定信息的记录**不会**改掉已落库的绑定"；但 2026-09-28T17:12:48Z
// 那次覆写发生在 `keep` 上线**之前**，所以 char 6 的 `slot_14..slot_22` 当时就已经是
// `(N,N)`，`slot_12` / `slot_23` 也已经被删掉。运维随后在**新二进制**上重登记
// （17:50:26..17:50:30Z 推 13 条 `Value == Slot` 的记录 + `042cffff` 哨兵），
// 落库 13 行、`registration_dropped_count=0` —— 没有任何东西再被破坏，
// 但**那 13 行里一条绑定都没有**，于是重发帧是
// `pairs=(12,12)(13,13)…(25,25)`：这些 value 指向主背包（list 0）的 12..25 号格子，
// 而 char 6 的 11 件换装物品在主背包的 **54..64** 号格子（SQL 实证：装备类型与登记键
// 1:1 对上 —— 武器 12←54、上衣 14←62、护肩 15←61、下装 16←57、鞋 17←59、
// 腰带 18←63、手镯 19←58、项链 20←64、戒指 21←60、辅助 22←56、魔法石 23←55）。
// 面板 = op1428 map × 容器，value 指不到物品 ⇒ **面板为空**（既不是"没登记"，
// 也不是"又被删了"，而是"这一次登记里没有绑定"）。
//
// # 本档的规则（默认 = 修复档 `restore`，**只读，不写库**）
//
//	对每一条 `slot_<n>` 状态行：若它是**不含绑定信息**的（`Value == Slot`），
//	而同一个 scope 的 `record_*` 取证键里存着**客户端自己**为这个槽位声明过的
//	**携带绑定**的记录（`Value != Slot`，现场 `ModeRegister`），就把那一行在**读取视图**里
//	补成那条绑定 —— 于是重发帧的 value 重新指向换装物品所在的背包格子。
//
// 四条边界（都是构造性的，有用例钉着）：
//
//  1. **只补"已经有行"的槽位**：被删掉的槽位不会被复活（`record_*` 不是状态源）；
//     一个只有取证键、没有 `slot_*` 行的 scope 仍然投影 **0 帧**
//     （`TestLoadNeverUnionsForensicKeysIntoTheLoadout` 保持绿色）；
//  2. **绝不覆盖携带绑定的行**：只有 `Value == Slot` 的行才会被补；
//  3. **只补客户端自己声明过的绑定**：候选来自 `record_<hex>`（`Apply` 写下的
//     **客户端上行记录**），不是服务端发明的值；同槽位有多条候选时按 hex 升序取第一条
//     （确定性、可复现）；
//  4. **不写库**：这是读取视图的修补，DB 里那几行保持原样；玩家在面板上重新绑定
//     （客户端再推 `ModeRegister`）时这些行会被真正的绑定覆盖，本档自动变成 no-op。
//
// 回退字面量 `off` 复现的是**线上当前这一版的字节**（`Load` 原样返回 `slot_*` 行）。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. `Value` 指向**主背包（list 0）**这一点，本轮的判据是"11 条真绑定的 value 与
//     0:54..0:64 那 11 件装备的**类型**逐一对应"（见 `_dist/buff-swap-round4.md` §2）。
//     value 的字段语义仍没有客户端侧证据；
//  2. 若 `Value == Slot` 是客户端在说"这个槽位**不要**换装（用身上那件）"，那本档
//     会把一个更早的绑定补回来 —— 与 `...REGISTRATION_BIND=keep` 的残余风险同源。
//     一个字面量 `off` 即可回到今天。
const BindingRestoreEnv = "DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE"

const (
	// BindingRestoreRestore 是**默认档（修复档）**：不含绑定信息的行按客户端自己
	// 上一次的绑定声明补回来（只读）。
	BindingRestoreRestore = "restore"
	// BindingRestoreOff 是**一键回退字面量**：与线上当前这一版**逐字节相同**
	// （`Load` 原样返回 `slot_*` 行，一个字节都不改）。
	BindingRestoreOff = "off"
)

// NormalizeBindingRestore 归一化 `DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`。
//
// 口径与其它档位一致：**只有回退字面量才离开修复档**，未设置 / 空 / 未知取值一律
// `restore`。
func NormalizeBindingRestore(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case BindingRestoreOff, "today", "live", "legacy", "rollback", "0", "disable", "none", "keep":
		return BindingRestoreOff
	default:
		return BindingRestoreRestore
	}
}

// CurrentBindingRestore 直接自读 env（与 `CurrentRegistrationBind` 同一口径）。
func CurrentBindingRestore() string {
	return NormalizeBindingRestore(os.Getenv(BindingRestoreEnv))
}

// RegistrationDeleteEnv 是"**窗口提交要不要删掉窗口外的行**"这一个开关
// （`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`，默认 = 修复档 `keep`）。
//
// # 它修的是什么（操作者第七次报障：「选择角色后 面板里原本有的那些行消失了」）
//
// 现场（HK `192.0.2.11`，`packet_log.txt` + `dnf_setting_values`，2026-09-28）：
//
//   - char 1 = `test1` 的 scope 在 18:38:52.817Z 之前有 **20 行**
//     （槽位 1..7 / 12..23 / 25），服务端在入场边界上把这些行原样重发
//     （`pairs=(1,1)(2,2)…(23,23)(25,25)`，20 对）；
//   - 18:38:57.839..18:39:00.526Z 操作者在面板里只改了**一行**
//     （`slot 12 -> value 20`，`00000c001400`，`binding_bearing=true`），
//     客户端这一趟只推了 **8 条**（槽位 12,14,15,16,17,18,19,20）；
//   - 18:39:04.534Z 收尾哨兵关闭窗口 ⇒ 第二轮起就有的"**替换**"语义把窗口外的
//     **12 行删掉**，集合从 20 变成 8（`registration_kept_count=8
//     registration_dropped_count=12`）。
//
// 操作者对这一幕的原话就是"面板里原本有的那些行消失了"。
// 而**协议里没有任何"移除"信号**：客户端上行只有 `ModeRegister`(0x00) /
// `ModeUnread`(0x01) / `ModeConfirm`(0x03) / `ModeClear`(0x2c 哨兵) 四种形态，
// 没有"删掉第 N 行"这种记录；**厂商参考实现里根本没有 op1428 处理器**
// （`ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY` 这个名字在整个工作区里只出现在本仓
// 自己的源码里，86JP / A21 的 C# 树里一次都没有；运行期日志也一直写着
// `vendor has no op1428 handler`）。
// ⇒ "提交即替换"是**本仓第二轮自己发明的**语义，不是厂商设计；现场证明它会把
// 玩家没碰过的行删掉。本档把默认改成**永不删除**。
//
// # 本档的规则（默认 = 修复档 `keep`）
//
//	窗口提交（哨兵关闭 / 重投影边界关闭 / 提升 pending 后关闭）**不删任何行**：
//	窗口里的行照旧被"确认"（值以落库行为准），窗口外的行**原样保留**；
//	"按旧语义本来会被删掉"的那些行写进 `retained_*` 字段，便于判读与取证。
//
// 三条硬性质：
//
//  1. **集合只增不减**：本档下 `slot_*` 键只会被新增/改写，不会被删
//     （落库形态与 `union` 档一致，但**下行语义不同**：本档仍然按窗口集合提交、
//     仍然写 `window_*` 记账、仍然有 `commit_trigger`）；
//  2. **"裸哨兵不能清空换装表"不受影响**：空窗口仍然永不提交（那条护栏在本档之下）；
//  3. **回退字面量 `replace` 逐字节等于线上当前这一版**：窗口替换（删行）照旧。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. 玩家"确实想删掉一行"这件事目前**无法表达**（协议里没有这个信号）；
//     在拿到客户端侧证据之前，宁可留着也不删 —— 要恢复删除语义用 `replace`；
//  2. `ModeClear` 的 `Slot` 字节（现场实测 ∈ {3,4,5,9,33}）是否携带"要清掉哪一行"
//     的含义，至今没有证据。
const RegistrationDeleteEnv = "DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE"

const (
	// RegistrationDeleteKeep 是**默认档（修复档）**：窗口提交不删行。
	RegistrationDeleteKeep = "keep"
	// RegistrationDeleteReplace 是**一键回退字面量**：窗口提交 = 替换
	// （窗口外的行被删掉），与线上当前这一版**逐字节相同**。
	RegistrationDeleteReplace = "replace"
)

// NormalizeRegistrationDelete 归一化 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`。
//
// 口径与其它档位一致：**只有回退字面量才离开修复档**，未设置 / 空 / 未知取值一律 `keep`。
func NormalizeRegistrationDelete(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationDeleteReplace, "window", "today", "live", "legacy", "rollback", "0", "disable":
		return RegistrationDeleteReplace
	default:
		return RegistrationDeleteKeep
	}
}

// CurrentRegistrationDelete 直接自读 env（与 `CurrentRegistrationBind` 同一口径）。
func CurrentRegistrationDelete() string {
	return NormalizeRegistrationDelete(os.Getenv(RegistrationDeleteEnv))
}

// 登记窗口的键前缀。两者都**不**与 `slot_` / `record_` / `sentinel_` 前缀相交，
// 所以 `Load` / `countEntries` / `countForensicEntries` / `isRecordEntryKey`
// 都不会把它们当成状态行或取证行（这是"窗口键不改变任何既有计数"的根据）。
//
// `entryWindowArmed` 是**记账开关**：只有真的见过一次哨兵才开始写窗口键。
// 于是"某些客户端版本不发哨兵"那条路径**连一个键都不会新增** ——
// 那个客户端的落库字节与改动前逐字节相同（不是"写了键但从不提交"）。
const (
	entryWindowPrefix = "window_"
	entryWindowCount  = "window_count"
	entryWindowArmed  = "window_armed"
)

// windowArmed 报告这个 scope 是否已经"见过哨兵"（= 登记窗口记账已启用）。
func windowArmed(values map[string]string) bool {
	return strings.TrimSpace(values[entryWindowArmed]) == "1"
}

// armRegistrationWindow 记下"这个客户端的登记是带哨兵分隔符的"。
// 只在**真的收到哨兵**时调用；`window_armed=1` 之后一直保留（提交也不清）。
func armRegistrationWindow(values map[string]string) {
	if values == nil {
		return
	}
	values[entryWindowArmed] = "1"
}

func registrationWindowEntryKey(slot byte) string {
	return entryWindowPrefix + strconv.Itoa(int(slot))
}

// windowSlots 读回当前打开的窗口里的槽位（升序）。窗口为空时返回 0 条。
func windowSlots(values map[string]string) []int {
	slots := make([]int, 0, len(values))
	for key := range values {
		if !strings.HasPrefix(key, entryWindowPrefix) {
			continue
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entryWindowPrefix))
		if err != nil || slot < 0 || slot > 0xFF {
			continue
		}
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	return slots
}

// 窗口/待提升段里每一条槽位记录**含不含绑定信息**（`Record.BindingBearing()`）。
//
// 这两件事只写在**值**上，键空间一字不改（`window_<slot>` / `pending_<slot>` 仍是
// 唯一的键形，`window_count` / `pending_count` 仍是唯一的计数键）：
// `windowSlots` / `pendingBurstSlots` 只认前缀与数字后缀，值的含义它们不关心，
// 所以既有用例（"见过哨兵之前一个 `window_*` 键都不许有" 等）逐条不受影响。
const (
	// entryScopeSlotOnly：`Value == Slot`，这条记录只声明"这个槽在集合里"。
	entryScopeSlotOnly = "slot"
	// entryScopeBinding：`Value != Slot`，这条记录声明了绑定的目标槽。
	entryScopeBinding = "bind"
)

// scopeMark 把一条记录的分类映射成窗口/待提升段里的值。
func scopeMark(binding bool) string {
	if binding {
		return entryScopeBinding
	}
	return entryScopeSlotOnly
}

// openRegistrationWindow 把这条记录记进当前窗口，返回窗口里的槽位数。
// `binding` 表示这条记录**携带绑定信息**（见 `Record.BindingBearing`）；
// 它只影响窗口键的值，不影响键集合与计数。
func openRegistrationWindow(values map[string]string, slot byte, binding bool) int {
	if values == nil {
		return 0
	}
	values[registrationWindowEntryKey(slot)] = scopeMark(binding)
	count := len(windowSlots(values))
	values[entryWindowCount] = strconv.Itoa(count)
	return count
}

// windowBindingSlots 读回窗口里**携带绑定信息**的那些槽位（升序）。
// 它是"这一次提交到底有没有人声明绑定"的唯一判据（见 `commitRegistrationWindow`）。
func windowBindingSlots(values map[string]string) []int {
	slots := make([]int, 0, len(values))
	for key, value := range values {
		if !strings.HasPrefix(key, entryWindowPrefix) {
			continue
		}
		if strings.TrimSpace(value) != entryScopeBinding {
			continue
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entryWindowPrefix))
		if err != nil || slot < 0 || slot > 0xFF {
			continue
		}
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	return slots
}

// clearRegistrationWindow 关掉窗口（删掉全部 `window_<slot>` 与 `window_count`）——
// 提交之后就等于"什么都还没收到"，下一个分隔符之前不会有任何删除发生。
// `window_armed` **不清**：它记的是"这个客户端会发哨兵"，不是"窗口里有什么"。
func clearRegistrationWindow(values map[string]string) {
	for key := range values {
		if key == entryWindowArmed {
			continue
		}
		if strings.HasPrefix(key, entryWindowPrefix) {
			delete(values, key)
		}
	}
}

// registrationWindowCount 是 `window_count` 的读法（缺键 = 0）。
func registrationWindowCount(values map[string]string) int {
	raw := strings.TrimSpace(values[entryWindowCount])
	if raw == "" {
		return 0
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

// arm 之前那一段 burst 的键前缀（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）。
//
// 刻意**不**与 `window_` 相交：`window_armed` / `window_count` / `window_<slot>`
// 的语义与既有用例（"见过哨兵之前一个 `window_*` 键都不许有"）原封不动，
// pending 是另一个命名空间、另有计数键、另有日志字段。
const (
	entryPendingPrefix = "pending_"
	entryPendingCount  = "pending_count"
)

// sentinelForensicsPresent 报告这个 scope 是否已经带着**哨兵取证键**
// （`sentinel_<hex>`，新旧二进制都会写）—— "这个客户端会发哨兵"的落库证据。
// 本档的 pending 记账**不**依赖它（未 arm 就记），它只用于日志/取证判读。
func sentinelForensicsPresent(values map[string]string) bool {
	for key := range values {
		if strings.HasPrefix(key, entrySentinelPrefix) {
			return true
		}
	}
	return false
}

func pendingBurstEntryKey(slot byte) string {
	return entryPendingPrefix + strconv.Itoa(int(slot))
}

// pendingBurstSlots 读回 arm 之前那一段 burst 的槽位（升序）。为空时返回 0 条。
func pendingBurstSlots(values map[string]string) []int {
	slots := make([]int, 0, len(values))
	for key := range values {
		if !strings.HasPrefix(key, entryPendingPrefix) {
			continue
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entryPendingPrefix))
		if err != nil || slot < 0 || slot > 0xFF {
			continue
		}
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	return slots
}

// openPendingBurst 把这条记录记进"arm 之前那一段 burst"，返回段里的槽位数。
// `binding` 的含义与 `openRegistrationWindow` 完全一致（同样只写在值上）。
func openPendingBurst(values map[string]string, slot byte, binding bool) int {
	if values == nil {
		return 0
	}
	values[pendingBurstEntryKey(slot)] = scopeMark(binding)
	count := len(pendingBurstSlots(values))
	values[entryPendingCount] = strconv.Itoa(count)
	return count
}

// clearPendingBurst 丢掉这一段（提交之后 / 提升之后它就是过去式了）。
func clearPendingBurst(values map[string]string) {
	for key := range values {
		if strings.HasPrefix(key, entryPendingPrefix) {
			delete(values, key)
		}
	}
}

// promotePendingBurst 把 `pending_*` 提升成 `window_*`，返回提升了几条。
// 提升**只搬槽位集合与它的分类标记**：值仍然来自 `slot_<n>` 状态键（下面
// `commitRegistrationWindow` 就是按 `window_<slot>` 去读 `slot_<slot>` 的），
// 所以这里不复制任何 hex。分类标记必须一起搬 —— 否则"这一段 burst 里有没有人
// 声明过绑定"会在提升处丢掉，`keep` 档的护栏就会失效。
func promotePendingBurst(values map[string]string) int {
	promoted := 0
	for key, value := range values {
		if !strings.HasPrefix(key, entryPendingPrefix) {
			continue
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entryPendingPrefix))
		if err != nil || slot < 0 || slot > 0xFF {
			continue
		}
		values[registrationWindowEntryKey(byte(slot))] = scopeMark(strings.TrimSpace(value) == entryScopeBinding)
		promoted++
	}
	if promoted > 0 {
		values[entryWindowCount] = strconv.Itoa(len(windowSlots(values)))
	}
	clearPendingBurst(values)
	return promoted
}

// windowCommit 是一次"关闭窗口"的完整结果（含触发点与提升条数）。
type windowCommit struct {
	Kept    []Record
	Dropped []Record
	// Protected 是**因为窗口里没人声明绑定而被保留下来的绑定行**（`keep` 档；
	// 窗口里出现过绑定记录时它恒为空）。它们在日志里单独成字段，便于判读
	// "这一次提交本来会删掉哪些绑定"。
	Protected []Record
	// Trigger 是关闭这条窗口的边界（RegistrationTrigger* 之一）。未提交时为空。
	Trigger string
	// Promoted 是被提升进窗口的 pending 条数（0 = 没有走提升路径）。
	Promoted int
	// ConfirmOnly 为真表示这次提交的窗口里**一条携带绑定的记录都没有**
	// （整段登记只声明了槽位集合），于是它只删无绑定的行。
	ConfirmOnly bool
	// Retained 是**按旧语义（`replace`）本来会被删掉、而本档保留下来的行**
	// （`delete=keep` 时非空；`replace` 档恒空，那些行进 `Dropped`）。
	Retained []Record
	// Committed 为真表示状态键集合已经被换成窗口集合。
	Committed bool
}

// commitRegistrationWindowTiered 是 `commitRegistrationWindow` 的**档位感知外壳**：
//
//   - 默认档（`record`）：窗口为空而 arm 之前那一段 burst 非空时，先把 pending
//     提升成窗口再提交 ⇒ "一段记录 + 一个收尾哨兵"的登记能在**同一个重投影边界**上
//     提交（现场 15:35 char 3 就是这一条）；
//   - 回退档（`sentinel`）：不做提升 —— 那就是线上当前这一版的字节（窗口为空 ⇒
//     不提交 ⇒ 并集留存）。
//
// 窗口**非空**时永远优先用窗口本身（哨兵之后的记录 = 这一段登记），pending 只作为
// "哨兵之后再没有记录"时的候选；所以"两个分隔符之间没有记录就不提交"这条性质
// （裸哨兵不能清空换装表）不受影响：pending 为空时窗口也为空 ⇒ 一个键都不动。
//
// `bind`（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）只改**删哪些行**：
// 窗口里一条携带绑定的记录都没有时（`confirmOnly`），只删无绑定的行。
//
// `del`（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`，默认 `keep`）改的是**删不删**：
// 默认档一个 `slot_*` 都不删（窗口外的行原样保留，进 `retained`）；
// 回退字面量 `replace` 才是第二轮起的那套"提交即替换"。
func commitRegistrationWindowTiered(values map[string]string, layout Layout, arm string, bind string, del string) windowCommit {
	if NormalizeRegistrationArm(arm) == RegistrationArmRecord &&
		len(windowSlots(values)) == 0 && len(pendingBurstSlots(values)) > 0 {
		promoted := promotePendingBurst(values)
		kept, dropped, protected, retained, confirmOnly, committed :=
			commitRegistrationWindow(values, layout, bind, del)
		if !committed {
			return windowCommit{Promoted: promoted}
		}
		return windowCommit{
			Kept:        kept,
			Dropped:     dropped,
			Protected:   protected,
			Retained:    retained,
			Trigger:     RegistrationTriggerBoundaryPreArmBurst,
			Promoted:    promoted,
			ConfirmOnly: confirmOnly,
			Committed:   true,
		}
	}
	kept, dropped, protected, retained, confirmOnly, committed :=
		commitRegistrationWindow(values, layout, bind, del)
	if !committed {
		return windowCommit{}
	}
	return windowCommit{
		Kept:        kept,
		Dropped:     dropped,
		Protected:   protected,
		Retained:    retained,
		Trigger:     RegistrationTriggerBoundaryWindow,
		ConfirmOnly: confirmOnly,
		Committed:   true,
	}
}

// decodeStoredRowValue 解一行 `slot_*` 的值（hex → 记录），供日志与删除前取证。
func decodeStoredRowValue(value string, layout Layout) (Record, bool) {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return Record{}, false
	}
	return parseStoredRow(raw, layout)
}

// RegistrationCommit 描述一次"窗口提交"的结果（= 状态键集合被换成窗口集合）。
type RegistrationCommit struct {
	CharacterID string
	// Mode 是提交时归一化后的档位。
	Mode string
	// Arm 是提交时的记账起点档位（`NormalizeRegistrationArm` 的取值）。
	Arm string
	// Bind 是提交时的绑定保护档位（`NormalizeRegistrationBind` 的取值）。
	Bind string
	// Delete 是提交时的**删除语义**档位（`NormalizeRegistrationDelete` 的取值）。
	Delete string
	// Trigger 是关闭这条窗口的边界（RegistrationTrigger* 之一）——
	// `boundary_pre_arm_burst` 就是"这一段登记是 arm 之前推上来的"那条路径。
	Trigger string
	// Promoted 是被提升进窗口的 `pending_*` 条数（0 = 走的是普通窗口路径）。
	Promoted int
	// ConfirmOnly 为真表示这次提交的窗口里一条携带绑定的记录都没有
	// （整段登记只声明了槽位集合），于是它只删无绑定的行。
	ConfirmOnly bool
	// WindowRecords 是这次提交的快照里保留的行数（= 窗口里的槽位数）。
	WindowRecords int
	// Kept / Dropped 是这次提交**保留**与**删除**的行，按 Slot 升序、已按档位归一化。
	Kept    []Record
	Dropped []Record
	// Protected 是**因为这一次登记没声明绑定而被保留下来**的绑定行
	// （`ConfirmOnly` 为真时才可能非空；`bind=replace` 档恒空）。
	Protected []Record
	// Retained 是**按旧语义（`replace`）本来会被删掉、而 `delete=keep` 档保留下来的行**
	// （按 Slot 升序；`replace` 档恒空 —— 那些行进 `Dropped`）。
	Retained []Record
	// KeptEntries / DroppedEntries / ProtectedEntries / RetainedEntries 是同一批行的
	// 落库字面形（`slot_<n>=<hex>`），与 settings 仓储里的字节逐字节对应 ——
	// 日志里的"写了什么 / 删了什么 / 保住了什么 / 本来会删什么"就是它们。
	KeptEntries      []string
	DroppedEntries   []string
	ProtectedEntries []string
	RetainedEntries  []string
	// SlotCount / ForensicCount 是提交后的键数（与 `ApplyResult` 同口径）。
	SlotCount     int
	ForensicCount int
	// UpdatedAt 是这次提交写进 scope 的时刻（= 重下发日志里的 `loadout_saved_at`）。
	UpdatedAt time.Time
}

// CommitRegistration 在重投影边界上关闭仍然打开的登记窗口。
//
// 语义与失败契约（与 `sendSkillSwitchInventoryProjection` 同口径）：
//   - 档位不是 `window`（`union` / `sentinel_only`）：**直接返回**，一个字节都不写；
//   - 从没见过哨兵的角色：没有 `window_*` 键 ⇒ `windowSlots` 为空 ⇒ committed=false，
//     **不做任何写入**（这就是"没有哨兵 ⇒ 逐字节等于改动前"的落点）；
//   - 窗口里有记录 ⇒ 按窗口集合提交：窗口内的行被"确认"（值以落库行为准），
//     `slot_count` / `updated_at` 重算并保存；
//   - **窗口外的行删不删**由 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE` 决定：
//     默认档 `keep` **一行都不删**（进 `Retained`，现场 char 1 的 20→8 就是这么来的），
//     回退字面量 `replace` 才是第二轮起那套"提交即替换"（进 `Dropped`）；
//   - 窗口里**一条携带绑定的记录都没有**、且绑定保护是默认档（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`
//     未设 / `keep`）⇒ 这次提交是 confirm-only：即使 `delete=replace` 也只删无绑定的行，
//     携带绑定的行保留（`Protected`），于是"一段不含绑定信息的登记"再也删不掉
//     别人登记好的换装；
//   - 窗口为空但 arm 之前那一段 burst（`pending_*`）非空、且记账起点是默认档
//     （`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM` 未设 / `record`）⇒ 先把 pending
//     提升成窗口再提交，`Trigger=boundary_pre_arm_burst`（现场 15:35 char 3 的形状）；
//   - 仓储缺失 / 角色为空：返回 committed=false 且无错误（边界不能因此被打断）。
func CommitRegistration(
	ctx context.Context,
	repos dnfrepo.Group,
	characterID string,
	layout Layout,
	mode string,
) (RegistrationCommit, bool, error) {
	characterID = strings.TrimSpace(characterID)
	result := RegistrationCommit{CharacterID: characterID, Mode: NormalizeRegistrationMode(mode)}
	if characterID == "" || repos.Settings == nil {
		return result, false, nil
	}
	if result.Mode != RegistrationWindow {
		return result, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stored, found, err := repos.Settings.Load(ctx, Scope(characterID))
	if err != nil || !found {
		return result, false, err
	}
	stored = dnfrepo.CloneSettings(stored)
	if stored.Values == nil {
		return result, false, nil
	}
	// 从没见过哨兵的客户端：**一个键都不写**。这就是"没有哨兵 ⇒ 逐字节等于改动前"
	// 的落点（不是死代码：见 `TestSkillSwitchRegistrationNoSentinelIsByteForByteToday`）。
	if !windowArmed(stored.Values) {
		return result, false, nil
	}
	arm := CurrentRegistrationArm()
	result.Arm = arm
	result.Bind = CurrentRegistrationBind()
	result.Delete = CurrentRegistrationDelete()
	commit := commitRegistrationWindowTiered(stored.Values, layout, arm, result.Bind, result.Delete)
	if !commit.Committed {
		return result, false, nil
	}
	slotCount, forensicCount := countEntries(stored.Values)
	stored.Values[entrySlotCount] = strconv.Itoa(slotCount)
	stored.Values[entryRecordCount] = strconv.Itoa(forensicCount)
	stored.UpdatedAt = time.Now().UTC()
	stored.Values[entryUpdatedAt] = stored.UpdatedAt.Format(time.RFC3339Nano)
	if err := repos.Settings.Save(ctx, stored); err != nil {
		return result, false, err
	}
	result.Trigger = commit.Trigger
	result.Promoted = commit.Promoted
	result.ConfirmOnly = commit.ConfirmOnly
	result.WindowRecords = len(commit.Kept)
	result.Kept = commit.Kept
	result.Dropped = commit.Dropped
	result.Protected = commit.Protected
	result.Retained = commit.Retained
	result.KeptEntries = recordEntries(commit.Kept, stored.Values)
	result.DroppedEntries = dropEntries(commit.Dropped)
	result.ProtectedEntries = recordEntries(commit.Protected, stored.Values)
	result.RetainedEntries = recordEntries(commit.Retained, stored.Values)
	result.SlotCount = slotCount
	result.ForensicCount = forensicCount
	result.UpdatedAt = stored.UpdatedAt
	return result, true, nil
}

// commitRegistrationWindow 把状态键集合换成窗口集合。**空窗口永不提交**
// （返回 committed=false 且一个键都不动）——这是"裸哨兵不能清空换装表"的落点。
//
// 只删 `slot_*`：取证键（`record_*` / `sentinel_*`）与 `source` / `updated_at`
// 一概不动，被删掉的行的 hex 因此仍然留在取证键里（可 grep、可追溯）。
// 提交成功时连带把 arm 之前那一段（`pending_*`）丢掉：既然窗口已经提交，
// 那一段就是过去式（下一次登记重新记）。
//
// # `bind` 档（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）
//
// 窗口里**一条携带绑定的记录都没有**（`window_<slot>` 的值全是 `slot`，
// 即整段登记只声明了槽位集合）时，本次提交是 **confirm-only**：
//
//	窗口外的行，只有**本身也不含绑定信息**的才删；
//	携带绑定的行**保留**并记进 `protected`（它们只在"这次登记自己声明了绑定"
//	的时候才允许被替换/删除）。
//
// 窗口里出现过哪怕一条携带绑定的记录 ⇒ 语义与改动前一模一样（全量替换）。
// `bind = replace`（一键回退字面量）恒走全量替换，且一个 `protected` 都不记。
//
// # `del` 档（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`，默认 `keep`）
//
// 默认档下**一个 `slot_*` 都不删**：窗口外的行原样保留，并记进 `retained`
// （= "按旧语义本来会被删掉的那些行"）。只有回退字面量 `replace` 才会 `delete`。
// 现场依据：char 1 在 18:39:04Z 被一次"只推了 8 条"的登记从 20 行缩成 8 行，
// 而操作者说的正是"面板里原本有的那些行消失了"。
func commitRegistrationWindow(values map[string]string, layout Layout, bind string, del string) (kept []Record, dropped []Record, protected []Record, retained []Record, confirmOnly bool, committed bool) {
	window := windowSlots(values)
	if len(window) == 0 {
		return nil, nil, nil, nil, false, false
	}
	keepBindings := NormalizeRegistrationBind(bind) == RegistrationBindKeep
	keepRows := NormalizeRegistrationDelete(del) == RegistrationDeleteKeep
	confirmOnly = keepBindings && len(windowBindingSlots(values)) == 0
	inWindow := make(map[int]bool, len(window))
	for _, slot := range window {
		inWindow[slot] = true
	}
	for key, value := range values {
		if !strings.HasPrefix(key, entrySlotPrefix) {
			continue
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix))
		if err != nil || slot < 0 || slot > 0xFF {
			continue
		}
		if inWindow[slot] {
			continue
		}
		record, ok := decodeStoredRowValue(value, layout)
		if confirmOnly && ok && record.BindingBearing() {
			// 这一次登记没有声明任何绑定 ⇒ 它没有资格删掉别人声明的绑定。
			protected = append(protected, record)
			if keepRows {
				retained = append(retained, record)
				continue
			}
		}
		if keepRows {
			// 默认档：**永不删行**。这一行按旧语义本来会被删掉，记进 retained 供判读。
			if ok {
				retained = append(retained, record)
			}
			continue
		}
		if ok {
			dropped = append(dropped, record)
		}
		delete(values, key)
	}
	for _, slot := range window {
		if record, ok := decodeStoredRowValue(values[slotEntryKey(byte(slot))], layout); ok {
			kept = append(kept, record)
		}
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Slot < dropped[j].Slot })
	sort.Slice(protected, func(i, j int) bool { return protected[i].Slot < protected[j].Slot })
	sort.Slice(retained, func(i, j int) bool { return retained[i].Slot < retained[j].Slot })
	clearRegistrationWindow(values)
	clearPendingBurst(values)
	values[entryWindowCount] = "0"
	return kept, dropped, protected, retained, confirmOnly, true
}

// recordEntries 把记录渲染成落库字面形（`slot_<n>=<hex>`，按 Slot 升序）。
func recordEntries(records []Record, values map[string]string) []string {
	entries := make([]string, 0, len(records))
	for _, record := range records {
		key := slotEntryKey(record.Slot)
		entries = append(entries, key+"="+strings.TrimSpace(values[key]))
	}
	return entries
}

// dropEntries 把被删掉的行渲染成 `slot_<n>=<hex>`（键已不在仓储里，字节来自记录本身）。
func dropEntries(records []Record) []string {
	entries := make([]string, 0, len(records))
	for _, record := range records {
		entries = append(entries, slotEntryKey(record.Slot)+"="+record.Hex())
	}
	return entries
}
