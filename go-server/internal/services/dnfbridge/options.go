// options.go 负责 DNF bridge 启动配置和环境变量兼容。
// 这里只归一化协议开关和监听参数，不持有运行期玩家状态。
package dnfbridge

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/skillswitch"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"longheng.io/server/internal/platform/config"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	defaultChannelListen           = ":7001"
	defaultChannelInfoFile         = "data/dnf/channel_info.etc"
	defaultServerIP                = "127.0.0.1"
	defaultScriptVersion           = "59"
	channelInfoBodyLatest          = "latest"
	gameInitialModeNotice          = "notice"
	gameInitialModeLogin           = "login_success"
	gameInitialModeUpper           = "upper_endpoint"
	gameInitialModeNone            = "none"
	gamePreBootstrapNone           = "none"
	gamePreBootstrapGuard          = "guard_clear"
	gamePostBootstrapNone          = "none"
	gamePostBootstrapCheck         = "client_check"
	gamePostBootstrapRoster        = "roster_init"
	gameUpperHeaderChannel13       = "channel13"
	gameUpperHeaderServer16        = "server16"
	gameUpperBodyCodecAuto         = "auto"
	gameUpperBodyCodecPlain        = "plaintext"
	gameUpperClientBodyCodecPlain  = "plaintext"
	gameUpperClientBodyCodecProbe  = "probe"
	gameUpperClientBodyCodecNative = "native"
	gameDprotoModeLegacy           = "legacy_patch"
	gameDprotoModeNative           = "native"
	// DOVE 明文首包里 UDP 字段是 2311/2312；这里只作为 InitialLoginNotice 的兼容默认值。
	defaultInitialUDPPort1        = 2311
	defaultInitialUDPPort2        = 2312
	defaultCommandCount           = 1382
	defaultNotificationCount      = 1326
	defaultMaxPacketBytes         = 1048576
	defaultAccountPrefix          = "dnf:"
	defaultPVFPath                = "data/dnf/Script.pvf"
	defaultPartyUDPRelayPortStart = 30000
	defaultPartyUDPRelayPortCount = 64

	// partyDungeonOwnerModeEnv 是"副本内发给队友的 op2/op9 的 owner 字节"单变量 A/B 开关。
	// 默认值 = 现行为（副本场景上下文 0），所以只改 env + 重启就能做 A/B，无需重编译。
	partyDungeonOwnerModeEnv          = "DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE"
	partyDungeonOwnerModeValueZero    = "zero"
	partyDungeonOwnerModeValueChannel = "channel"
	partyDungeonOwnerModeValueMixed   = "mixed"

	// partyDungeonAllMemberUserStateEnv 是"副本内那条 op3 USER_STATE 声明哪些成员"的单变量开关。
	// 厂商 `Network\Builders\Dungeon\EnterSelectDungeonStateBuilder.cs:12-26` 的
	// `BuildUserState` 写的是 "u8 条数 + N×(u16 key + u8 state)"，并且
	// `DungeonEntryHandler.BuildEnterSelectDungeonPrefix` 对**每个接收者发全员**
	// （注释原文见 dungeon_party_user_state_members.go）。默认档 = `self` = 今天的
	// 4 字节单条体，逐字节不变。
	partyDungeonAllMemberUserStateEnv = "DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE"
	partyDungeonUserStateValueSelf    = "self"
	partyDungeonUserStateValueAll     = "all"

	mainQuestExperienceMultiplierEnv  = "DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER"
	mainQuestExperiencePercentEnv     = "DNFBRIDGE_MAIN_QUEST_EXP_PERCENT"
	mainQuestExperiencePercentInvalid = -1
)

// partyDungeonOwnerMode 是 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的归一化取值。
// **零值必须是现行为**：测试与探针里的 `Service{options: options{...}}` 字面量不会提到这个
// 字段，它们必须继续与加开关之前逐字节一致（与 joustEventDisabled / storyDigestDisabled
// 同样的"零值即现状"约定）。
//
// 三档之所以存在，是因为 op2（mode0/mode1）与 op9 的 owner 字节在客户端吃的是**两条不同的
// 判断**，可以各自独立取值（详见 partyDungeonPeerOwnerPlan）：
//   - op2 的 owner 决定 actor 走 `current-scene-create` 还是 `current-scene-context0-remap`；
//   - op9 的 owner 决定成员行**带不带**跨频道内联身份块。
//
// 历史上两格都单独试过、都失败（changlog.md 187–214 / 142 行）：全频道号 = actor 建出来但
// op9 丢身份块；全 0 = 身份块在但 op2 走 remap 分支。`mixed`（op2=频道号、op9=0）是那两格
// 的**交叉组合**，此前从未试过。
type partyDungeonOwnerMode int

const (
	// partyDungeonOwnerSceneContext 是默认：op2 mode0/mode1 与 op9 的 owner 字节都取副本
	// 场景上下文（`currentSceneObjectContext` = 0），即现行为。
	partyDungeonOwnerSceneContext partyDungeonOwnerMode = iota
	// partyDungeonOwnerClientChannel：op2 与 op9 的 owner 都取**收包者当前所选频道号**
	// （城镇实测 121）= 00:22 之前那版的线格式。
	partyDungeonOwnerClientChannel
	// partyDungeonOwnerMixed：**op2 取频道号、op9 取副本上下文 0**（交叉组合，从未试过）。
	partyDungeonOwnerMixed
)

func (mode partyDungeonOwnerMode) String() string {
	switch mode {
	case partyDungeonOwnerClientChannel:
		return partyDungeonOwnerModeValueChannel
	case partyDungeonOwnerMixed:
		return partyDungeonOwnerModeValueMixed
	default:
		return partyDungeonOwnerModeValueZero
	}
}

// readPartyDungeonOwnerMode 解析 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE。
// 解析方式与仓库既有 envString / normalize* 一致：首尾空白与大小写不敏感，未设置/空
// 视为默认。返回值第三个是"传了非法值、已回退默认"（用于启动日志，见 Service.Start）。
func readPartyDungeonOwnerMode() (partyDungeonOwnerMode, string, bool) {
	raw := envString(partyDungeonOwnerModeEnv, "")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", partyDungeonOwnerModeValueZero, "default", "current", "context", "scene_context", "dungeon_context":
		return partyDungeonOwnerSceneContext, raw, false
	case partyDungeonOwnerModeValueChannel, "client_channel", "receiver_channel", "town_channel":
		return partyDungeonOwnerClientChannel, raw, false
	case partyDungeonOwnerModeValueMixed, "op2_channel", "actor_channel", "create_only":
		return partyDungeonOwnerMixed, raw, false
	default:
		return partyDungeonOwnerSceneContext, raw, true
	}
}

// readPartyDungeonAllMemberUserState 解析 DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE。
// 只有**显式**写成 all 一族的值才打开；未设置/空/非法一律回退 `self`（= 今天的行为）。
// 返回值第三个是"传了非法值、已回退默认"（用于启动日志，见 Service.Start）。
func readPartyDungeonAllMemberUserState() (bool, string, bool) {
	raw := envString(partyDungeonAllMemberUserStateEnv, "")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", partyDungeonUserStateValueSelf, "default", "current", "self_only", "local", "receiver":
		return false, raw, false
	case partyDungeonUserStateValueAll, "all_members", "members", "roster", "party", "everyone":
		return true, raw, false
	default:
		return false, raw, true
	}
}

type options struct {
	guildSoloCreate          bool
	channelListen            string
	gameListenHost           string
	channelInfoFile          string
	channelServerID          int
	channelAdvertiseID       int
	channelInfoBodyMode      string
	channelScriptEncoding    string
	serverIP                 string
	scriptVersion            string
	gameOuterToken           uint32
	gameInitialMode          string
	gamePreBootstrap         string
	gamePostBootstrap        string
	gameUpperHeader          string
	gameUpperBodyCodec       string
	gameUpperClientBodyCodec string
	gameDprotoMode           string
	initialUDPPort1          int
	initialUDPPort2          int
	commandCount             int
	notificationCount        int
	currentClientVersion     string
	defaultGroupLoginOnly    bool
	gameStreamWaitTrace      bool
	maxPacketBytes           int
	packetLogEnabled         bool
	packetLogPath            string
	accountID                string
	accountPrefix            string
	pvfPath                  string
	pvfMaxBytes              int64
	partyUDPRelayEnabled     bool
	partyUDPRelayPortStart   int
	partyUDPRelayPortCount   int
	// joustEventDisabled 是 DNFBRIDGE_JOUST_EVENT_ENABLED 的取反存储
	// （默认 false = 骑士马战大竞猜事件保持开启）。取反存放是刻意的：options
	// 的零值必须保持"事件开启"，这样所有不经过 optionsFromConfig 的构造路径
	// （测试与探针里的 Service 字面量）继续与加开关前的行为逐字节一致，开关
	// 只有一个变量。读取与抑制点见 Service.joustEventEnabled。
	joustEventDisabled bool

	// joustEventCatalogMode 是 DNFBRIDGE_JOUST_EVENT_CATALOG 的归一化取值，只决定
	// op108 在线活动目录里马战（竞技大赛）事件 2365 的那两行要不要下发：
	// auto（默认，零值同义）= 跟随上面的 joustEventDisabled；off = 无论马战总开关
	// 如何都不发 2365；on = 强制发 2365（A/B 与将来重新启用马战用）。
	//
	// 它是"登录后自动弹出竞技大赛窗"的服务端兜底：op1240/1241/1242/1243 已经被
	// joustEventDisabled 抑制，但 op108 目录里 2365 的 base/active 两行此前不受
	// 任何开关约束，客户端可以只凭这个目录把该活动判成全天开放。取值归一化见
	// normalizeCurrentSpendTimeJoustCatalogMode，判据见
	// Service.currentSpendTimeCatalogIncludesJoustEvent，启动日志见 Service.Start
	// 的 joust_event_catalog / joust_event_catalog_includes_2365 字段。
	joustEventCatalogMode string
	// joustEventCatalogEnvRaw 与 joustEventCatalogEnvBad 只用于把"非法取值已回退
	// 到 auto"记进启动日志（Service.Start），不参与任何发包判断。
	joustEventCatalogEnvRaw string
	joustEventCatalogEnvBad bool

	// storyDigestDisabled 是 DNFBRIDGE_STORY_DIGEST_ENABLED 的取反存储
	// （默认 false = 剧情回顾(story digest)子系统保持开启）。取反存放的理由
	// 与 joustEventDisabled 相同：options 的零值必须保持"剧情回顾开启"，这样
	// 所有不经过 optionsFromConfig 的构造路径（测试与探针里的 Service 字面量）
	// 继续与加开关前的行为逐字节一致。读取与抑制点见
	// Service.storyDigestEnabled。
	storyDigestDisabled bool

	// fallbackNameAudit 是 DNFBRIDGE_FALLBACK_NAME_AUDIT 的存储（默认 false = off）。
	// **它只加日志、不改任何下行字节**：打开后在 DPROTO 编码之前的明文内层包上
	// 扫描服务端唯一的合成角色名前缀 `dnf_<角色ID>`，命中即记一条
	// `game-fallback-name-in-outbound-packet`（msg_id / 偏移 / 名字 / 上下文）。
	// 用途：定位"每次登录弹出 pvp02 竞技场匹配房、房内徽标显示 dnf_46688"这条
	// 服务端名单的来源包。证据链、为什么必须放在 DPROTO 之前、以及两个既有观测
	// 口径的坑（服务端 hex 只记前 512 字节；客户端日志看不到受保护包的内层）
	// 见 fallback_name_audit.go。
	fallbackNameAudit bool

	// questClearTicketActiveOnly 是 DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP 的取反存储
	// （默认 false = 任务手册"任务完成"(op1429) 与任务完成券共用"整段主线"口径）。
	// 取反存放的理由与上面两个开关相同：options 的零值必须保持**线上默认口径**，
	// 这样所有不经过 optionsFromConfig 的构造路径（测试与探针里的 Service 字面量）
	// 与线上默认逐字节一致。
	//
	// 置 DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP=0 时回退到"只完成当前激活主线"的
	// 2026-09-25 之前口径，用于线上 A/B 或临时回退，**不需要重新编译**。
	// 读取点见 Service.questClearTicketScope，启动日志见 Service.Start 的
	// "dnfbridge started" 行（quest_clear_ticket_scope 字段）。
	questClearTicketActiveOnly bool
	// mainQuestExperienceMultiplier 是服务端可信的主线交付经验倍率。
	// 环境变量缺失、非整数或不在 1..100 时按 1；玩家 op34 包里的
	// Multiplier 语义未证明，不能作为该值。
	mainQuestExperienceMultiplier int
	// mainQuestExperiencePercent is the trusted final percentage for ordinary
	// standalone epic main quests. Zero keeps the previous reward path; an
	// explicitly invalid environment value is stored as -1 so settlement rejects
	// it through the existing op34 business-failure reply.
	mainQuestExperiencePercent int

	// equipMoveContainerRefreshFull 是 DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH 的
	// 取反存储（默认 false = 穿脱装备/时装后**不再重发 5 个整容器**）。取反存放的
	// 理由与上面几个开关相同：options 的零值必须保持**新默认口径**，所有不经过
	// optionsFromConfig 的构造路径（测试与探针）与线上默认一致。
	//
	// 置 full 时回退到 2026-09-25 之前的行为（`PostActionRefreshSelectedItemContainers`
	// 的 class0/op13 list 0/1/2/7/38 整容器重发，每次穿脱 ≈ 10.1 KB）。
	// 证据与读取点见 equip_move_refresh.go，启动日志见 Service.Start 的
	// equip_move_container_refresh 字段。
	equipMoveContainerRefreshFull bool
	// equipAppearanceRefreshMode1Rollback 是 DNFBRIDGE_EQUIP_APPEARANCE_REFRESH 的取反存储
	// （默认 false = 每次穿脱同步发一次 class0/op2 **mode0** 当前场景对象自投影，客户端
	// 只在 mode0 路上重挂本地角色外观并 `visual_synced=1`，模型即时刷新）。
	// 只有显式写 mode1 才把这个取反开关置 true，回退到 2026-09-25..09-27 的
	// mode1 穿戴态 + 200ms 连点合并档。证据与读取点见 equip_move_refresh.go。
	//
	// 取反存放的理由与上面几个开关相同：options 的零值必须保持**新默认口径**，
	// 所有不经过 optionsFromConfig 的构造路径（测试与探针）与线上默认一致。
	equipAppearanceRefreshMode1Rollback bool
	// equipAppearanceSlotGateOff 是 DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE 的取反存储
	// （默认 false = **外观相关性闸门生效**：只有被移动物品的穿戴槽落在 mode0 纸娃娃的
	// 可见槽 0..13（或时装列表 17）里，才补 PostActionRefreshSelectedActorAppearance；
	// 耳环/魔法石/护甲等槽位 ≥14 的装备不再触发一次零视觉变化的 op2 mode0 ⇒ 客户端不再
	// 白重建一次本地角色，直击"穿脱装备卡顿"）。
	//
	// 置 off 时回退到改动前：每次穿脱都无条件补外观刷新（result.Changed 即补）。
	// 取反存放的理由与上面几个开关相同：options 的零值必须保持**修复档默认**，所有
	// 不经过 optionsFromConfig 的构造路径与线上默认一致。证据与判据见
	// equip_move_refresh.go 的 equipAppearanceSlotGateEnv，启动日志见 Service.Start 的
	// equip_appearance_slot_gate 字段。
	equipAppearanceSlotGateOff bool
	// petMoveContainerRefreshFull 是 DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH 的
	// 取反存储（默认 false = 宠物/神器穿戴后**不再回放 5 个整容器**，改成厂商
	// PetInventoryMoveCoordinator 的 list 7 逐槽口径）。取反存放的理由与上面几个
	// 开关相同：options 的零值必须保持**新默认口径**，所有不经过 optionsFromConfig
	// 的构造路径（测试与探针）与线上默认一致。
	//
	// 置 full 时回退到 2026-09-28 第一轮的行为
	// （`PostActionRefreshSelectedItemContainers` 的 class0/op13 list 0/1/2/7/38
	// 整容器重发，每次穿/脱宠物 ≈ 23.9 KB）。证据、现场取证与读取点见
	// equip_move_refresh.go，启动日志见 Service.Start 的
	// pet_move_container_refresh 字段。
	petMoveContainerRefreshFull bool
	// petMoveContainerRefreshNone 是 DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH=none 的存储：
	// 连 list 7 的逐槽行也不发，完全依赖客户端原生 op19 搬运（与装扮档
	// DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed 同口径）。
	petMoveContainerRefreshNone bool
	// avatarAppearanceCoalesceOff 是 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE 的取反存储
	// （默认 false = mode0 档启用 leading + trailing 合并：窗口内第一次同步发、
	// 随后的并成窗口末尾一次尾随刷新，N 连点最多 2 个 mode0）。
	// 取反存放的理由与上面几个开关相同：options 的零值必须保持**新默认口径**。
	//
	// 置 off 时逐字节回到 2026-09-28 第一轮：mode0 档每次穿脱都同步发一个 mode0。
	// mode1 回退档不受影响（它一直有自己的 200 ms 合并）。证据与判据见
	// avatar_appearance_coalesce.go 与 game_aligned.go 的 mode0 分支。
	avatarAppearanceCoalesceOff bool
	// avatarAppearanceCoalesceWindowMillis 是 DNFBRIDGE_EQUIP_APPEARANCE_COALESCE_MS
	// 的解析结果（连点窗口与尾随刷新延迟，同一个值）。零值/非法值 = 内置默认
	// （currentAvatarAppearanceCoalesceWindow，200 ms）；零值即默认的约定让所有
	// 不经过 optionsFromConfig 的构造路径与线上默认一致。
	avatarAppearanceCoalesceWindowMillis int
	// avatarAppearanceTrailingMillis 是 DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS 的解析结果
	// （**尾随刷新延迟**，与上面的"连点窗口"分开）。零值/非法值 = 沿用连点窗口
	// ⇒ 未配置时逐字节等于改动前。拆开之后可以配「窗口 3000 ms + 尾随 250 ms」：
	// 整身换装算一次连点（N 次穿脱最多 2 个 mode0 = 2 次客户端重建），而收手后
	// 250 ms 就能看到最终外观。证据见 avatar_appearance_coalesce.go。
	avatarAppearanceTrailingMillis int
	// equipMode0RepositoryMemoMode 是 DNFBRIDGE_EQUIP_MODE0_REPOSITORY_MEMO 的
	// 归一化取值。零值 "" 走**修复档 reuse**：穿脱装备/换时装那一次外观刷新
	// （sendSelectedActorMode0AppearanceRefresh）在**本次调用内**对
	// Equipment/Character/Account 三个仓做只读记忆（实测 6 次装备读 → 1 次），
	// 任何 Save 立刻作废记忆。写 off（别名 disable/0/legacy/none）一键回到
	// 改动前：每次读都真的落到仓储。证据、调用点清单与残余不确定见
	// equip_mode0_repository_memo.go。
	equipMode0RepositoryMemoMode string
	// upgradeRoomReplyMode 是 DNFBRIDGE_UPGRADE_ROOM_REPLY 的归一化取值
	// （零值 "" 走参考档 reference_drop）。见 upgrade_room.go。
	upgradeRoomReplyMode string
	// skillSwitchInventoryMode 是 DNFBRIDGE_SKILL_SWITCH_INVENTORY 的归一化取值
	// （零值 "" 走**修复档 shape_guard**：op1428 的记录按角色落库，并在下发前校验
	// 包体形状与客户端 1428 入站解析器一致；形状不符一个字节都不发）。
	// 写 project（别名 verbatim）= 原样回放（取证/回退档）；
	// 写 before_transition_only（别名 early_only）= 改动前的四条边界档；
	// 写 record = 只落库不下发；
	// 写 reference_drop（别名 off/0/disable/legacy）一键回到更早的旧行为：
	// 收下即丢、一个下行字节都不发。见 skill_switch_inventory.go。
	skillSwitchInventoryMode string
	// skillSwitchInventoryRecordBodyLayout 是 DNFBRIDGE_SKILL_SWITCH_RECORD_BODY 的
	// 归一化取值（**零值 "" 走修复档 v4**：op1428 上行包体是 4 字节
	// `u8 slot | u8 mode | u16 value`，现行分帧交来的 6 字节切片按 dual-read
	// 去掉前 2 个非包体字节；落库写 4 字节规范形）。
	// 写 v6（别名 legacy/today/old/6）= 逐字节回到改动前：把 6 字节当包体。
	// 证据与逐字节映射见 internal/modules/dnf/skillswitch/record.go。
	skillSwitchInventoryRecordBodyLayout skillswitch.Layout
	// skillSwitchCandidatePairsMode 是 DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS 的
	// 归一化取值（**零值 "" 走 off = 默认关**：不发任何实验帧、逐字节等于今天）。
	// 写 identity12 = 在既有合法投影边界上多发一帧 `u8 count=12 + 12×(u32,u32)`
	// 恒等对，用于判定客户端 manager 329 期望的 pair 语义（**实验不是修复**，
	// 那 12 对未被证明）。见 skill_switch_inventory_candidate_pairs.go。
	skillSwitchCandidatePairsMode string

	// boxOpenContainerRefreshChangedOnly 是 DNFBRIDGE_BOX_OPEN_CONTAINER_REFRESH 的
	// 归一化结果（默认 false = current = **逐字节保持今天**：op208/op1128 回包后回放
	// 五个整容器 list 0/1/2/7/38）。置 changed_only 后这两个 opcode 只重发本次真正
	// 改变的容器（源 list 0 + 本次实际发放到的奖励容器）。
	//
	// 默认取 current 而不是新口径，是因为那次分诊明确写了"没有客户端/厂商证据可以翻转"
	// （见 changlog.md:1103-1106）。零值 = 现状的约定与 equipMoveContainerRefreshFull
	// 相同：所有不经过 optionsFromConfig 的构造路径（测试与探针里的 Service 字面量）
	// 与线上默认逐字节一致。证据、残余不确定与一键回退见 box_open_container_refresh.go，
	// 启动日志见 Service.Start 的 box_open_container_refresh 字段。
	boxOpenContainerRefreshChangedOnly bool

	// partyDungeonOwner 是 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的归一化取值
	// （零值 = partyDungeonOwnerSceneContext = 现行为）。它只影响"副本内发给
	// **其他**队员"的 op2 mode0/mode1 与 op9 的 owner 字节；本地角色自己那条包
	// 任何取值下都逐字节不变。权威字段来源与 A/B 判据见
	// dungeon_party_copresence.go 的 partyDungeonPeerOwner。
	partyDungeonOwner partyDungeonOwnerMode
	// partyDungeonOwnerEnvRaw 与 partyDungeonOwnerEnvBad 只用于把"非法取值已回退到
	// 默认"记进启动日志（Service.Start），不参与任何发包判断。
	partyDungeonOwnerEnvRaw string
	partyDungeonOwnerEnvBad bool

	// partyDungeonUserStateAll 是 DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE 的归一化取值
	// （**零值 = false = self = 现行为**：副本内 op3 USER_STATE 的 body 恒为
	// [1, keyLo, keyHi, 1] 四个字节）。置 `all` 后条数 = **当前队伍副本运行时里实际在场
	// 的成员**数（receiver-first + 稳定 slot 升序），逐条 `u16 key + u8 1`。
	//
	// 零值取 self 的理由与 partyDungeonOwner / joustEventDisabled 一致：所有不经过
	// optionsFromConfig 的构造路径（测试与探针里的 Service 字面量）必须继续与加这个开关
	// 之前逐字节一致。成员取源链、顺序口径与四项回落原因见
	// dungeon_party_user_state_members.go。
	partyDungeonUserStateAll bool
	// partyDungeonUserStateEnvRaw 与 partyDungeonUserStateEnvBad 只用于把"非法取值已回退到
	// self"记进启动日志（Service.Start），不参与任何发包判断。
	partyDungeonUserStateEnvRaw string
	partyDungeonUserStateEnvBad bool

	// collectboxMode 是 DNFBRIDGE_COLLECTBOX_MODE 的归一化取值
	// （零值 = off = 修复前行为：op1021~1024 只记 game-collectbox-deferred，
	// 一个字节都不回）。默认 reference = 按厂商 9.16 参考实现的形状与顺序下发，
	// 见 collectbox_state.go。
	//
	// 零值取 off 的理由与 joustEventDisabled / storyDigestDisabled 一致：所有
	// 不经过 optionsFromConfig 的构造路径（测试与探针里的 Service 字面量）必须
	// 继续与加这个功能之前逐字节一致。
	collectboxMode string
	// collectboxInit 是 DNFBRIDGE_COLLECTBOX_INIT 的归一化取值
	// （零值 = select_ack = 与厂商一致的时机；request_only = 只在客户端开窗请求时补发）。
	collectboxInit string
	// collectboxMutation 是 DNFBRIDGE_COLLECTBOX_MUTATION 的归一化取值
	// （零值 = observe_only = 修复前行为：op1022/1023/1024 只记
	// game-collectbox-mutation-observed，一个字节都不回）。默认 reference =
	// 按参考实现受理 op1022（把宝珠放进箱子）并落库，见 collectbox_add.go。
	collectboxMutation string

	// partyDungeonPeerPosition 是 DNFBRIDGE_PARTY_DUNGEON_PEER_POSITION 的归一化取值
	// （零值 = partyDungeonPeerPositionDisabled = 现行为：副本内一条队友位移都不发）。
	// 开关、证据与 A/B 判据见 dungeon_party_peer_position.go。
	partyDungeonPeerPosition partyDungeonPeerPositionMode

	// partyUDPRelayEndpointFallback 是 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK 的
	// 归一化取值（零值 = off = 现行为：反向腿未学习时丢弃 + 计数 + 首次日志）。
	// 打开（`advertised`）后用对端**自报**端口 + 其已被游戏 TCP 会话认证的 IPv4 再写一次。
	// 证据、安全边界与判读串见 party_udp_relay_endpoint_fallback.go。
	partyUDPRelayEndpointFallback partyUDPRelayEndpointFallbackMode

	// partyUDPRelayProbeReplay 是 DNFBRIDGE_PARTY_UDP_RELAY_PROBE_REPLAY 的归一化取值。
	// **零值 = Disabled = 加开关前的旧行为**（首包被丢），但 optionsFromConfig 的默认档
	// 是 Enabled（env 空/未设置 = on），因为这是"单向死锁"的修法本身。
	// 证据、安全边界与判读串见 party_udp_relay_probe_replay.go。
	partyUDPRelayProbeReplay partyUDPRelayProbeReplayMode

	// partyUDPRelayProbeListen / partyUDPRelaySeedForward / partyUDPRelayDatagramTrace 是
	// party_udp_relay_probe_listener.go 的三个开关（第七次现场取证的根因修法）：
	//   - PROBE_LISTEN（零值 off，默认 **on**）：常驻原生探针监听（默认 30000 + PORT_START），
	//     把"客户端在组队前自己发到中继端口的端点"记下来；
	//   - SEED_FORWARD（零值 off，默认 **on**）：腿建立时把该腿 owner 的观测端点写成种子目标，
	//     让第一个到达的数据报被立即投递（厂商行为：组队前两端端点已 bind），并启动待投清扫器；
	//   - DATAGRAM_TRACE（零值 off，默认 **off**）：每个入向数据报一行，复测判读用。
	partyUDPRelayProbeListen   partyUDPRelayProbeListenMode
	partyUDPRelaySeedForward   partyUDPRelaySeedForwardMode
	partyUDPRelayDatagramTrace bool
	// partyUDPRelayObservedEndpointFanout 是
	// DNFBRIDGE_PARTY_UDP_RELAY_OBSERVED_ENDPOINT_FANOUT（**默认 on**）：把探针监听看到过的
	// **每一个**源端点（无归属、不需要角色 id）接进反向腿的首包投递候选集
	// （候选 kind = observed_probe_endpoint）。零值 false = 加本功能之前的行为。
	partyUDPRelayObservedEndpointFanout bool
	// partyUDPRelayViaPort 是 DNFBRIDGE_PARTY_UDP_RELAY_VIA_PORT（**默认 0 = off**）：
	// >0 时 op11 通告给两端的对端端口改为这个**共享端口**（现场取 2312 = 客户端唯一持续保活的
	// UDP 端口），P2P 载荷也从这个端口收与发。零值 = 现行为（各自腿口），见 party_udp_relay_via_port.go。
	partyUDPRelayViaPort int
	// partyUDPRelayProbePorts 是 DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS 的解析结果。
	partyUDPRelayProbePorts []int
	// partyUDPRelayMemoTTL 是 DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS（默认 30 秒）：
	// 陈旧记忆（加速器 NAT 端口几十秒就回收，21:15 那次用的就是 8 分钟前的旧端口）不得单独使用。
	partyUDPRelayMemoTTL time.Duration
	// partyUDPRelaySilentPeerKick 是 DNFBRIDGE_PARTY_UDP_RELAY_SILENT_PEER_KICK（默认 on）：
	// 反向腿从未发过包时，给沉默那个成员**重推一次名册**（含 op11 行）促使它自己发探测包。
	partyUDPRelaySilentPeerKick bool
	// partyUDPRelayKickMinAge 是 DNFBRIDGE_PARTY_UDP_RELAY_SILENT_PEER_KICK_SECONDS（默认 2s）：
	// 腿建立后多久才允许第一次催（formation/进副本窗口内不打扰）。
	partyUDPRelayKickMinAge time.Duration
	// partyUDPRelayResendWindow 是 DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS（默认 60s）：
	// 对端仍沉默期间"周期重投待投首包"的总时长（每 3s 一轮）。
	partyUDPRelayResendWindow time.Duration
	// partyUDPRelayKickTiming 是上面两个 env 归一化后的节拍（minAge/interval/window/maxKicks）。
	partyUDPRelayKickTiming partyUDPRelayKickTiming

	// partyUDPRelayMode 是 DNFBRIDGE_PARTY_UDP_RELAY_MODE 的归一化取值。
	// **零值 = embedded = 改动前行为**（自建内嵌中继）；optionsFromConfig 的默认档是
	// `remote`（厂商架构：注册到独立进程 a21-relay，本进程不 bind 中继端口）。
	// 证据、命名空间隔离与一键回退见 party_udp_relay_remote.go 文件头。
	partyUDPRelayMode partyUDPRelayMode
	// partyUDPRelayRemoteEndpoint 是 DNFBRIDGE_PARTY_UDP_RELAY_REMOTE（host:port，
	// 默认 38.22.90.52:10250 = 港机 a21-relay 的控制通道）。
	partyUDPRelayRemoteEndpoint string
	// partyUDPRelayRemoteSecret 是解析后的 32 字节密钥；**nil = 未配/非法 ⇒ 回退 embedded**。
	// 它只用于 HMAC，绝不进日志（日志里只有 partyUDPRelayRemoteSecretState 那三个词）。
	partyUDPRelayRemoteSecret []byte
	// partyUDPRelayRemoteSecretState 是密钥状态词：configured / missing / invalid。
	partyUDPRelayRemoteSecretState string
	// partyUDPRelayRemoteRoomBase 是 DNFBRIDGE_PARTY_UDP_RELAY_ROOM_BASE（默认 100000）：
	// a21-relay 是**共享**中继（同时在为另一台有真实玩家的服务器转发），房间号必须做
	// 命名空间隔离，实际注册的 roomId = ROOM_BASE + partyID。
	partyUDPRelayRemoteRoomBase int

	// partyPeerSelfEndpoint 是 DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT 的归一化取值
	// （零值 = observed = 现行为：op11 里自己那一行用 TCP 观察到的 IPv4）。
	// `reported` = 改用客户端 CS op2 自报的 inner/outer/port。
	// 证据与风险见 party_peer_self_endpoint.go。
	partyPeerSelfEndpoint partyPeerSelfEndpointMode

	// partyPeerPortEndian 是 DNFBRIDGE_PARTY_PEER_PORT_ENDIAN 的归一化取值
	// （零值 = be = 默认档：厂商 9.16 同会话同秒的"CS op2 原始字节 ↔ 中继源端口"配对
	// 证明端口是**大端**，而同一结构里的 MTU 仍是小端 ⇒ 混合字节序）。
	// `le` = 2026-09-25 上一轮 M0 的小端读数（一键回退）。
	// 取证与残余不确定见 party_peer_endpoint.go 文件头。
	partyPeerPortEndian partyPeerPortEndianMode

	// partyPeerEndpointIncludeSelf 是 DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF 的归一化取值
	// （零值 = off = 厂商 9.16 形状：op11 只发对端行、员数 = 对端数）。
	// `on` = 我们改动前的形状（含收包者自己那一行，45B/count=2）。
	// 证据与边界见 party_peer_endpoint_include_self.go。
	partyPeerEndpointIncludeSelf partyPeerEndpointIncludeSelfMode

	// partyLANDirect 是 DNFBRIDGE_PARTY_LAN_DIRECT 的归一化取值
	// （**零值 = relay = 改动前行为**：op11 对端行写中继端点）。
	// `lan` = 同局域网（自报 inner 相同 + 服务端观测到的 outer 相同）时把对端行改写为
	// 对端的 inner 地址 + 自报端口，客户端因此不经中继直连；条件不成立仍原样回退中继。
	// 安全条件、假阳性护栏与不改的边界见 party_lan_direct.go。
	partyLANDirect partyLANDirectMode

	// partyFormationOrder 是 DNFBRIDGE_PARTY_FORMATION_ORDER 的归一化取值
	// （零值 = vendor = 厂商两阶段：op11 barrier → op153 → op9）。
	// `vendor_wire` = 厂商日志实际字节序（op11 → op9 → op153）；
	// `legacy` = 改动前的 op153 → op11 → op9（一键回退）。
	// 证据与两档差异见 party_formation_order.go。
	partyFormationOrder partyFormationOrderMode

	// partyLeaveRepair 是 DNFBRIDGE_PARTY_LEAVE_REPAIR 的归一化取值
	// （零值 = repair = 修好后的行为：`removed=false` 而管理器仍持有该角色时按角色再摘一次，
	// 并广播给幸存者）。`legacy` = 改动前（只清离队者自己的客户端投影 + 回 ACK，不通知队长；
	// 会话未绑定时静默返回）逐字节回退。证据、安全性论证见 party_leave_repair.go。
	partyLeaveRepair partyLeaveRepairMode

	// partyForce 是 DNFBRIDGE_PARTY_FORCE 的归一化取值（**零值 = off = 现行为**）。
	// 只有显式 `on` 才起"命令文件轮询 → 强制组队"的测试触发器；
	// off 档不启动 goroutine、不读命令文件、不发一个字节。见 party_force_trigger.go。
	partyForce partyForceMode

	// partyForceCommandPath / partyForceResultPath 是触发器的命令文件与结果文件路径
	// （默认 `force_party.cmd` / `force_party.result`，都是**相对进程工作目录**的相对路径，
	// 现场即 `/opt/dnf90/force_party.cmd`）。partyForceInterval 是轮询间隔。
	partyForceCommandPath string
	partyForceResultPath  string
	partyForceInterval    time.Duration

	// partyForceInvite 是 DNFBRIDGE_PARTY_FORCE_INVITE 的归一化取值
	// （**零值 = off = 现行为**）。只有显式 `on` 才起"命令文件轮询 → 服务端代发
	// 真实组队邀请提示（class0/op7）"的测试触发器；off 档不启动 goroutine、
	// 不读命令文件、不发一个字节。见 party_force_invite.go。
	partyForceInvite partyForceMode

	// partyForceInviteCommandPath / partyForceInviteResultPath / partyForceInviteInterval
	// 同上（默认 `force_invite.cmd` / `force_invite.result`）。
	partyForceInviteCommandPath string
	partyForceInviteResultPath  string
	partyForceInviteInterval    time.Duration

	// partyForceInviteCoPresence 是 DNFBRIDGE_PARTY_FORCE_INVITE_CO_PRESENCE 的归一化取值
	// （**零值 = require_same_area**）：代发邀请前套用真实路径那条
	// `PeerInSameArea` 闸门，且只在"两端都有存在记录却不同桶"时具名拒绝、零发包；
	// `off` = 逐字节回到改动前。见 party_force_invite.go 文件头。
	partyForceInviteCoPresence partyForceInviteCoPresenceMode

	// partyPeerEndpointsAfterRoster 是 DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER 的
	// 归一化取值（零值 = off = 现行为：formation 里 op11 只发一次）。
	// `repeat` = 在 op9 之后再补发一次同一份端点信息（对齐 A21，D6）。
	// 证据与边界见 party_peer_endpoints_after_roster.go。
	partyPeerEndpointsAfterRoster partyPeerEndpointsAfterRosterMode

	// partyPeerEndpointRebroadcast 是 DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST 的
	// 归一化取值（零值 = off = 现行为：收 CS op2 只记录不重播）。
	// `on` = 端口真的变了就重播整份队伍信息（对齐 A21+86JP，D7）。
	// 证据与边界见 party_peer_endpoint_rebroadcast.go。
	partyPeerEndpointRebroadcast partyPeerEndpointRebroadcastMode

	// partyPeerRowVendorFields 是 DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS 的归一化取值
	// （零值 = vendor = 厂商 9.16 实测：op11 对端行的 acc/nat/mtu/char_attr =
	// 会话序号 / 1 / 512 / 0x64）。`legacy` = 改动前（char_id / 0 / 1500 / 0）逐字节回退。
	// 证据与 acc 规律见 party_peer_row_vendor_fields.go 文件头。
	partyPeerRowVendorFields partyPeerRowVendorFieldsMode

	// partyPeerRowAcc 是 DNFBRIDGE_PARTY_PEER_ROW_ACC 的归一化取值
	// （零值 = auto = 按厂商规律算：每个会话一个进程内序号；显式 u32 = 钉死）。
	// 只在 partyPeerRowVendorFields = vendor 时生效；raw/bad 只喂启动日志。
	partyPeerRowAcc partyPeerRowAccSetting

	// partyDungeonEntryPeerEndpoints 是 DNFBRIDGE_PARTY_DUNGEON_ENTRY_PEER_ENDPOINTS 的
	// 归一化取值（零值 = vendor = 厂商行为：副本进场序列在 op2 mode1 之后补发一条
	// op11 remotes）。`legacy` = 改动前（进场不发 op11）。
	// 证据与形状复用见 party_dungeon_entry_peer_endpoints.go。
	partyDungeonEntryPeerEndpoints partyDungeonEntryPeerEndpointsMode

	// dungeonPartyRoomSlotTable 是 DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE 的归一化取值
	// （零值 = vendor = 厂商行为：每次 start-map（op29）提交之后补发一条 class0/op9 kind0
	// 房间槽位表）。`once` = 只在本次提交还没有 kind0 槽位表时补；`legacy` = 不发。
	// 证据（厂商 68B 夹具）与边界见 dungeon_party_room_slot_table.go。
	dungeonPartyRoomSlotTable dungeonPartyRoomSlotTableMode

	// partyDungeonEntrySlotTable 是 DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE 的归一化取值
	// （零值 = current = 今天的行为：进图那一次提交里，落位中的 `…_post_start_map` kind0
	// 名册帧与厂商形状的房间槽位表**两条都发**）。`vendor_only` = 厂商口径：本次提交已经
	// 发过房间槽位表时，让位那条重复的名册帧（每个 actor 每个提交恰好一条）。
	// 现场证据（p21 退队序列 + p14/p18 单变量）与边界见 dungeon_party_room_slot_table.go。
	partyDungeonEntrySlotTable dungeonPartyEntrySlotTableMode

	// partyDungeonEntryFollowerFrame 是 DNFBRIDGE_PARTY_DUNGEON_ENTRY_FOLLOWER_FRAME 的
	// 归一化取值（零值 = current = 今天的行为：队员"跟随者选择器转换"那一批里
	// op153 实时信息 + 自己那条 op9 名册帧都发）。`skip` = 只让位那条 op9（op153 照发）。
	// 现场证据（H1 实机排除后的单变量阶梯）与安全阀见 game_enter_select_dungeon.go 的
	// partyDungeonEntryFollowerFrameEnv。
	partyDungeonEntryFollowerFrame partyDungeonEntryFollowerFrameMode

	// dungeonStartMapFatigue 是 DNFBRIDGE_PARTY_DUNGEON_START_MAP_FATIGUE 的归一化取值
	// （零值 = vendor = 厂商行为：start-map（op29）提交之后补一条 class0/op36 fatigue 状态，
	// 位置在 op9 房间槽位表之前）。`legacy` = 不发。
	// 证据见 dungeon_party_room_slot_table.go。
	dungeonStartMapFatigue dungeonStartMapFatigueMode

	// partyDungeonEntryBarrier 是 DNFBRIDGE_PARTY_DUNGEON_ENTRY_BARRIER 的归一化取值
	// （零值 = vendor = 厂商行为：所有成员都走到 op28 之后才统一放行 op800+op29）。
	// `legacy` = 改动前（队长整条序列跑完再异步驱动队员、不发 op800）。
	// 证据见 party_dungeon_entry_barrier.go。
	partyDungeonEntryBarrier partyDungeonEntryBarrierMode

	// dungeonDeathRoomProof 是 DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF 的归一化取值
	// （零值 = strict = 改动前行为：op39 的 owner/形状不可证就拒收、只记日志）。
	// `room_proven` = op39 目标确实是本房间在册活怪时按普通怪死亡提交。
	// 证据见 dungeon_death_room_proof.go。
	dungeonDeathRoomProof dungeonDeathRoomProofMode

	// partyDungeonLoadingBarrier 是 DNFBRIDGE_PARTY_DUNGEON_LOADING_BARRIER 的归一化取值
	// （零值 = vendor = 厂商行为：组队副本里落位与 op37 主状态按"全队 op37 到齐"放行）。
	// `legacy` = 改动前（各自请求各自放行）。
	// 证据见 party_dungeon_loading_barrier.go。
	partyDungeonLoadingBarrier partyDungeonLoadingBarrierMode

	// hellPartyEntry 是 DNFBRIDGE_HELL_PARTY_ENTRY 的归一化取值
	// （零值 = vendor = 校验 worldmap area 的 `[hell dungeon]` / `[hell quest]`
	// 前置条件，并且一定用 `{0, code, 0}` 把拒绝回给客户端，绝不静默丢弃）。
	// `legacy` = 改动前行为（不校验、不回包 —— 现场就表现为"卡在地下城选择界面"）。
	// 证据见 dungeon_hell_party_entry.go。
	hellPartyEntry hellPartyEntryMode

	// hellBlockedSlots 是 DNFBRIDGE_HELL_BLOCKED_SLOTS 的归一化取值
	// （**零值 = off = 逐字节改动前**：深渊派对请求一律走 hellPartyEntry 那一层的
	// 拒绝；`on` = 逐槽位判定 `[hell quest]`、不合规槽位进 blocked 列表随 class0/op27
	// 下发，并且**不再因为深渊准入拒绝进图** —— 操作者清单第 69 条）。
	// 证据与下发体布局见 dungeon_hell_blocked_slots.go / dungeon_context_op27.go。
	hellBlockedSlots hellBlockedSlotsMode

	// dungeonStartMapPrecedence 是 DNFBRIDGE_DUNGEON_START_MAP_PRECEDENCE 的归一化取值
	// （**默认 = specification = PVF `[map specification]` / `[boss map]` 显式声明优先**；
	// `ownership` = 改动前口径，文件名启发式排在显式声明之前，一键回退）。
	// 现场症状：格兰迪发电站 104 / 克雷发电站 101 的起始房被启发式换成
	// `15424quest(0,0)start.map` / `18000(0,0)S.map`，两张图都缺
	// `[pathgate recognize range]` ⇒ 清房后门不开。
	// 证据见 dungeon_start_map_precedence.go 文件头。
	dungeonStartMapPrecedence worldmap.StartMapPrecedence

	// hellSelectPageEligibility 是 DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY 的归一化取值
	// （**默认 = vendor = 开选择页（class0/op27 = 厂商 NOTI 0x001B）时先判一次
	// 逐槽位深渊资格，结果随这一条下发**；`legacy` = 改动前只在 op16 判，
	// 页面那条 op27 的 blocked 列表恒空 ⇒ 按钮永远亮的）。
	// 只有 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 时才有落点。见 dungeon_hell_select_page.go。
	hellSelectPageEligibility hellSelectPageEligibilityMode

	// dungeonHellRoom 是 DNFBRIDGE_HELL_ROOM 的归一化取值
	// （**默认 = vendor = 修复档**：按厂商 DungeonEntryHandler.cs:1329-1334 在
	// op28 下发深渊房间坐标 + hellPartyEnabled=1 + value2=0x0B，并按
	// DungeonMapHandler.cs:509-513 把 op29 offset 7 摆到标准值 2；
	// `legacy` = 改动前的缺席投影 0xff/0xff、0、0、offset 7 = 0，一键回退）。
	// 证据见 dungeon_hell_room.go 文件头。
	dungeonHellRoom dungeonHellRoomMode

	// reviveStateNotify 是 DNFBRIDGE_REVIVE_STATE_NOTIFY 的归一化取值
	// （**默认 = vendor = 修复档**：op41 复活成功后**先**发 class-0/32 的 state=1
	// "角色已复活"通知，**再**发 class-1/op41 ACK，与厂商
	// DungeonCombatHandler.cs:593-608 同序；`legacy` = 改动前那样完全不发，
	// 一键回退）。证据见 dungeon_revive_notify.go。
	reviveStateNotify reviveStateNotifyMode

	// dungeonEntrySelectionMode 是 DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE 的归一化取值
	// （**零值 = strict = 逐字节今天**：op16 `body[7]` 非 0 一律按"当前 EXE 不支持的模式"
	// 拒绝；`allow` = 放行 `body[7]` 的任意值，其余每一个判据逐条保持 fail-closed）。
	// 现场症状：地轨中心（area 39）的 310/311/312/313/314 全部用 `body[7]=1` 发请求，
	// 被误拒成 `failure_reason_byte=19`，整片区域"点了进不去"。
	// 证据见 dungeon_entry_packets.go 的 readDungeonEntrySelectionMode 块。
	dungeonEntrySelectionMode currentDungeonEntrySelectionModeMode

	// dungeonTournamentRewardAck 是 DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK 的归一化取值
	// （**零值 = select = 修复档**：客户端自己发来的 2 字节选奖回一条 458 选中投影
	// （7 字节），选奖未完成时再补 9 字节 457 选奖态；`state` = 一键回退到改动前，
	// 任何路径都不发 458。真机 `sub_1CF5010` 就是那条 458 的 reader。
	// 现场、二进制派发表取证与崩溃边界见 dungeon_tournament_config.go。
	dungeonTournamentRewardAck dungeonTournamentRewardAckMode

	// epicPieceBook 是 DNFBRIDGE_EPIC_PIECE_BOOK 的归一化取值
	// （零值 = on = 史诗碎片计入**账号级**史诗图鉴、不再进背包；`always` = 全量快照
	// 连 entries=0 也发（厂商 9.16 逐字节形状）；`off` = 一键回到"碎片当普通材料进背包"）。
	// 目录真源、下行包形状与三方证据见 epic_piece_book.go 文件头。
	epicPieceBook epicPieceBookMode

	// epicPieceGrant 是 DNFBRIDGE_EPIC_PIECE_GRANT 的归一化取值
	// （零值 = on = 邮件/GM 领取与登录清扫都改走账号图鉴账本，碎片一个背包格都不占；
	// `off` = 这两条路径一键回到"碎片当普通 [material] 进材料页"）。
	// 与 epicPieceBook 相互独立：BOOK=off 是全局回退（含地面拾取），GRANT=off 只回退
	// 领取期与清扫期。证据与边界见 epic_piece_grant.go。
	epicPieceGrant epicPieceGrantMode

	// epicPiecePush 控制史诗图鉴下行同步，不改变账本和背包语义。
	// 生产加载默认always，选角全量初始化，领取/拾取/扣除成功后实时覆盖数量。
	// options字面量零值仍是login_only，显式回退档也保持；原生契约见epic_piece_push.go。
	epicPiecePush epicPiecePushMode

	// 远程部署用的公网账号 API（启动器注册/登录 + 按来源 IP 绑定账号）。
	accountAPIListen   string
	accountAPIDSN      string
	accountAPITable    string
	accountAPIRegister bool
	// accountAPILoginToken 控制登录时是否签发启动器令牌（账号绑定用）。
	// 置 0 时登录不返回令牌，所有启动器都会退回固定的兼容串 + 按来源 IP 绑定，
	// 用于客户端不接受改后 outer token 时的即时回退，无需重新分发登陆器。
	//
	// 注意：置 0 时按来源 IP 的那条兜底通道**已经不存在**（2026-09-25 删除，
	// 见 client_account_ip.go），会话会直接落到共享兜底账号并写 WARN。
	accountAPILoginToken    bool
	playerStrict            bool
	playerAdminSecret       string
	playerProxySecret       string
	playerRegistrationLimit int
	playerMaxOnline         int

	// gameProactiveEndpointWhenUnbound 是握手顺序的回退开关
	// （DNFBRIDGE_GAME_PROACTIVE_ENDPOINT_WHEN_UNBOUND=1）。
	// 零值 = 现行为：账号还没定案也先发 class0/op1 CHANNELINFO 通知，
	// 等客户端交出带启动器令牌的 class1/op1 登录包后再绑账号。
	// 置 1 恢复旧行为：账号未绑定的连接直接收主动 class1/op1 端点成功包 ——
	// 客户端因此不会发登录包，令牌永远读不到，这类连接只能落到共享账号。
	gameProactiveEndpointWhenUnbound bool

	// patchDistDir 是客户端补丁负载目录（登陆器「检查并拉取更新」的只读来源，
	// 路由见 account_api_patch.go）。清单 manifest.json 由
	// /root/make-patch-manifest.sh 生成在同一目录里。
	patchDistDir string

	// patchPayloadDir 是 /patch/* 那套端点的负载目录（路由见 patch_files.go），
	// 由 DNFBRIDGE_PATCH_DIR 配置，默认 /opt/dnf90/patch-payload。清单由
	// /root/patch-publish.sh 生成在同一目录里；这套端点支持 Range 断点续传，
	// 是登陆器云端自动更新的主端点。
	patchPayloadDir string

	// tutorialDeferUserState 控制 PVF 教程副本的 class0/op3 state 1 处理方式：
	//   true（默认）= 把教程 user state 延迟到客户端 finish-loading 边界再发
	//     （上游 0.1.8 血统的做法）。2026-09-24 操作者在 HK 实测：新手教程
	//     可以正常打通（此前完全抑制时教程卡在移动教学、过不了图）。
	//   false = 完全抑制（旧行为）。当前 EXE（8A7AF614）在本教程副本里
	//     active-room object manager 在 post-op120 等较早边界为 nil，
	//     那些边界上发 op3 state 1 会 0xC0000005 @ E44ED6；抑制能避免崩溃
	//     但教程无法推进，所以只作为应急回退。
	tutorialDeferUserState bool

	// epicProductionAccountScope 是 DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE 的归一化取值
	// （零值 = session = 修正后行为：用**会话已识别的账号**判定角色归属，与角色表
	// buildUpperGetUserInfoRosterBody 的账号口径一致）。
	// `shared` = 一键回退到改动前行为（用 s.accountID() = options.accountID /
	// 本地单账号 profile 兜底账号比对；香港那台是共享兜底账号 dnf:1，因此每一次
	// 史诗制作请求都会被判成"角色不属于本会话"并回失败码 3）。
	// 现场、逐字段读法与 PVF 证据见 epic_production_account_scope.go 文件头。
	epicProductionAccountScope epicProductionAccountScopeMode

	// epicProductionInfoPush 是 DNFBRIDGE_EPIC_PRODUCTION_INFO_PUSH 的归一化取值
	// （零值 = always = 厂商 9.16 形状：actor bound、scene commit 之前就发 op1355，
	// 还没选武器（target=0）也发）。`selected_only` = 改动前行为（target<=0 不发）。
	// 厂商夹具与逐字节对照见 epic_production_account_scope.go 文件头。
	epicProductionInfoPush epicProductionInfoPushMode

	// expertJobGoldSource 是 DNFBRIDGE_EXPERT_JOB_GOLD_SOURCE 的归一化取值
	// （零值 = character_stats = 修正后行为：副职业分解机/修理/升级/放弃的金币
	// 一律读 `Character.Stats["gold"]`，即 `dnf_characters.gold`，与客户端钱包
	// 投影和全服其它金币链路同源）。
	// `inventory_wallet` = 一键回退到改动前行为（读背包伪槽 `list 0 slot 0`，槽不
	// 存在即判 ErrInsufficientGold；香港那台金币只在 gold 列，因此分解机会回
	// 失败码 21 → 客户端「所持金币不足」）。
	// 现场、逐字段读法与 DB 证据见 expert_job_gold_source.go 文件头。
	expertJobGoldSource expertJobGoldSourceMode
}

func optionsFromConfig(cfg config.ServiceConfig) options {
	opts := options{
		guildSoloCreate: envBool("DNFBRIDGE_GUILD_SOLO_CREATE", false),
		channelListen:   envString("DNFBRIDGE_CHANNEL_LISTEN", defaultChannelListen),
		gameListenHost:  strings.TrimSpace(os.Getenv("DNFBRIDGE_GAME_LISTEN_HOST")),
		channelInfoFile: firstEnv([]string{"DNFBRIDGE_CHANNEL_INFO_PATH", "DNF_CHANNEL_INFO_FILE"}, defaultChannelInfoFile),
		channelServerID: envIntAny([]string{"DNFBRIDGE_CHANNEL_SERVER_INDEX", "DNF_CHANNEL_SOURCE_SERVER_INDEX"}, 1),
		channelAdvertiseID: envIntAny(
			[]string{"DNFBRIDGE_CHANNEL_ADVERTISE_SERVER_INDEX", "DFO_CHANNEL_SERVER_INDEX"},
			-1,
		),
		channelInfoBodyMode: normalizeChannelInfoBodyMode(envString(
			"DNFBRIDGE_CHANNEL_INFO_BODY_MODE",
			channelInfoBodyLatest,
		)),
		channelScriptEncoding: envString("DNFBRIDGE_CHANNEL_SCRIPT_ENCODING", "utf8"),
		serverIP:              chooseServerIP(cfg),
		scriptVersion:         envString("DNF_SCRIPT_VERSION", defaultScriptVersion),
		gameOuterToken:        envUint32Any([]string{"DNFBRIDGE_GAME_OUTER_TOKEN", "DFO_GAME_OUTER_TOKEN"}, 0),
		gameInitialMode:       normalizeGameInitialMode(envString("DNFBRIDGE_GAME_INITIAL_MODE", gameInitialModeNotice)),
		gamePreBootstrap:      normalizeGamePreBootstrap(envString("DNFBRIDGE_GAME_PRE_BOOTSTRAP", gamePreBootstrapNone)),
		gamePostBootstrap:     normalizeGamePostBootstrap(envString("DNFBRIDGE_GAME_POST_BOOTSTRAP", gamePostBootstrapNone)),
		gameUpperHeader:       normalizeGameUpperHeader(envString("DNFBRIDGE_GAME_UPPER_HEADER", gameUpperHeaderChannel13)),
		gameUpperBodyCodec: normalizeGameUpperBodyCodec(envString(
			"DNFBRIDGE_GAME_UPPER_BODY_CODEC",
			gameUpperBodyCodecAuto,
		)),
		gameUpperClientBodyCodec: normalizeGameUpperClientBodyCodec(envString(
			"DNFBRIDGE_GAME_UPPER_CLIENT_BODY_CODEC",
			gameUpperClientBodyCodecPlain,
		)),
		gameDprotoMode:         normalizeGameDprotoMode(envString("DNFBRIDGE_DPROTO_MODE", gameDprotoModeLegacy)),
		initialUDPPort1:        envInt("DNF_INITIAL_UDP_PORT1", defaultInitialUDPPort1),
		initialUDPPort2:        envInt("DNF_INITIAL_UDP_PORT2", defaultInitialUDPPort2),
		commandCount:           envInt("DNF_COMMAND_PACKET_COUNT", defaultCommandCount),
		notificationCount:      envInt("DNF_NOTIFICATION_PACKET_COUNT", defaultNotificationCount),
		currentClientVersion:   envString("DNFBRIDGE_CURRENT_CLIENT_VERSION", "90cn"),
		defaultGroupLoginOnly:  envBool("DNFBRIDGE_DEFAULT_GROUP_LOGIN_ONLY", false),
		gameStreamWaitTrace:    envBool("DNFBRIDGE_GAME_STREAM_WAIT_TRACE", false),
		maxPacketBytes:         envInt("DNF_MAX_PACKET_BYTES", defaultMaxPacketBytes),
		packetLogEnabled:       envBool("DNFBRIDGE_PACKET_LOG", false),
		packetLogPath:          envString("DNFBRIDGE_PACKET_LOG_PATH", "logs/packet_log.txt"),
		accountID:              envString("DNFBRIDGE_ACCOUNT_ID", ""),
		accountPrefix:          envString("DNFBRIDGE_ACCOUNT_PREFIX", defaultAccountPrefix),
		pvfPath:                choosePVFPath(cfg),
		pvfMaxBytes:            choosePVFMaxBytes(cfg),
		partyUDPRelayEnabled:   envBool("DNFBRIDGE_PARTY_UDP_RELAY_ENABLED", true),
		partyUDPRelayPortStart: envInt("DNFBRIDGE_PARTY_UDP_RELAY_PORT_START", defaultPartyUDPRelayPortStart),
		partyUDPRelayPortCount: envInt("DNFBRIDGE_PARTY_UDP_RELAY_PORT_COUNT", defaultPartyUDPRelayPortCount),
		// 组队中继后端档：**默认 remote = 厂商架构**（注册到独立进程 a21-relay，
		// 本进程不再 bind 31000+X 的腿）；写 `embedded` 一键回到我们自建的内嵌中继。
		// 端点/密钥/房间号命名空间见 party_udp_relay_remote.go。
		partyUDPRelayMode: readPartyUDPRelayMode(
			envString(partyUDPRelayModeEnv, partyUDPRelayModeValueRemote)),
		partyUDPRelayRemoteEndpoint: envString(
			partyUDPRelayRemoteEndpointEnv, defaultPartyUDPRelayRemoteEndpoint),
		partyUDPRelayRemoteRoomBase: partyUDPRelayRemoteRoomBase(
			envString(partyUDPRelayRemoteRoomBaseEnv, "")),
		// 收集箱：默认 reference = 按厂商 9.16 参考实现下发（操作者现场是
		// "开窗后客户端只剩 xui 默认占位串 test string1/2/3"）。置
		// off/0/false/no 一键回退到修复前行为（只记 game-collectbox-deferred）。
		// 取值归一化与下发见 collectbox_state.go。
		collectboxMode: collectboxModeValue(envString(collectboxModeEnv, collectboxModeReference)),
		// 收集箱整组缓存的下发时机：默认 select_ack = 与厂商逐字节相同的时刻
		// （选角 ack 之后、op5 权限快照之前）；request_only 只在客户端开窗请求时补发。
		collectboxInit: collectboxInitValue(envString(collectboxInitEnv, collectboxInitSelectAck)),
		// 收集箱登记（op1022 放宝珠）：默认 reference = 按参考实现受理并落库；
		// 置 observe_only/off/0/false/no 一键回退到修复前行为
		// （只记 game-collectbox-mutation-observed，一个字节都不回）。
		// 取值归一化与受理见 collectbox_add.go。
		collectboxMutation: collectboxMutationValue(envString(collectboxMutationEnv, collectboxMutationReference)),
		// 单变量实验开关：置 0/false/off/no 时不推送 op1240/op1241/op1242/op1243，
		// 用来复现/排除"马战事件把客户端切进受限（赛事）状态"的怀疑。
		joustEventDisabled: !envBool("DNFBRIDGE_JOUST_EVENT_ENABLED", true),
		// 单变量实验开关：置 0/false/off/no 时不再下发 op1378（选角 ack 之后的
		// story digest last-level 通知），也不再接受客户端"开始播放"上报去推进/
		// 持久化 story_digest_last_level，用来复现/排除"剧情回顾子系统的下发与
		// 推进把部分角色推进受限状态"的怀疑。
		storyDigestDisabled: !envBool("DNFBRIDGE_STORY_DIGEST_ENABLED", true),
		// 纯只读诊断开关：默认 off = 一个字节、一行日志都不变。
		// 置 on/1/true/yes 时在明文内层包上审计合成角色名 dnf_<角色ID>，
		// 每命中一包记一条 game-fallback-name-in-outbound-packet。
		// 用途与判读见 fallback_name_audit.go。
		fallbackNameAudit: envBool(fallbackNameAuditEnv, false),
		// 单变量实验开关：任务手册"任务完成"(op1429) 与任务完成券的清任务口径。
		// 默认（未设置或为真）= 整段主线口径（CouponClearScopeMainStorySweep），
		// 与客户端确认框"自动完成 Lv89 为止的主线任务"一致；
		// 置 0/false/off/no 时回退到只完成当前激活主线的旧口径
		// （CouponClearScopeActiveOnly），不需要重新编译。
		questClearTicketActiveOnly:    !envBool("DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP", true),
		mainQuestExperienceMultiplier: readMainQuestExperienceMultiplier(),
		mainQuestExperiencePercent:    readMainQuestExperiencePercent(),

		// 穿脱装备/时装（op19）的回包后刷新口径，见 equip_move_refresh.go。
		// 默认 = 新口径（不再重发 5 个整容器 / 外观用 op2 mode0 当前场景对象自投影）；
		// 写 full / mode1 分别一键回退到 2026-09-25 之前的逐字节行为，不需要重新编译。
		equipMoveContainerRefreshFull: normalizeEquipMoveContainerRefreshMode(
			envString(equipMoveContainerRefreshEnv, equipMoveContainerRefreshSuppressed),
		) == equipMoveContainerRefreshFull,
		equipAppearanceRefreshMode1Rollback: normalizeEquipAppearanceRefreshMode(
			envString(equipAppearanceRefreshModeEnv, equipAppearanceRefreshMode0),
		) == equipAppearanceRefreshMode1,
		// 穿脱装备/换时装的外观刷新"外观相关性闸门"，见 equip_move_refresh.go 的
		// equipAppearanceSlotGateEnv。默认 on = 只在真正改变 mode0 纸娃娃外观的穿脱上
		// 补 PostActionRefreshSelectedActorAppearance；写 off 一键回退到"每次穿脱都发"。
		equipAppearanceSlotGateOff: normalizeEquipAppearanceSlotGateMode(
			envString(equipAppearanceSlotGateEnv, equipAppearanceSlotGateOn),
		) == equipAppearanceSlotGateOff,
		// 宠物/神器穿戴（op19 落点 list 3 槽 26..29）的回包后刷新口径，见
		// equip_move_refresh.go 里 petMoveContainerRefreshEnv 的长注。
		// 默认 slots = 只发本次变动的那一格 list 7（厂商逐槽口径，不回放整容器）；
		// none = 连这一格也不发；full = 一键回退到 2026-09-28 第一轮的
		// 五个整容器回放（list 0/1/2/7/38），不需要重新编译。
		petMoveContainerRefreshFull: normalizePetMoveContainerRefreshMode(
			envString(petMoveContainerRefreshEnv, petMoveContainerRefreshSlots),
		) == petMoveContainerRefreshFull,
		petMoveContainerRefreshNone: normalizePetMoveContainerRefreshMode(
			envString(petMoveContainerRefreshEnv, petMoveContainerRefreshSlots),
		) == petMoveContainerRefreshNone,
		// 时装穿脱（mode0 档）的连点合并口径 + 窗口，见
		// avatar_appearance_coalesce.go。默认 leading_trailing / 200 ms；
		// off = 一键回到"每次穿脱都同步发"。
		avatarAppearanceCoalesceOff: normalizeEquipAppearanceCoalesceMode(
			envString(equipAppearanceCoalesceEnv, equipAppearanceCoalesceLeadingTrailing),
		) == equipAppearanceCoalesceOff,
		avatarAppearanceCoalesceWindowMillis: normalizeAvatarAppearanceCoalesceWindowMillis(
			envString(equipAppearanceCoalesceWindowEnv, ""),
		),
		avatarAppearanceTrailingMillis: normalizeAvatarAppearanceTrailingMillis(
			envString(equipAppearanceTrailingEnv, ""),
		),
		// 强化直播（576 家族）的客户端回包档，见 upgrade_room.go。
		// 默认 reference_drop = 与 86JP/A21/厂商 9.16 相同（不回包，只记具名事件）；
		// echo_failure 是形状未证实的受控实验档。
		upgradeRoomReplyMode: normalizeUpgradeRoomReplyMode(
			envString(upgradeRoomReplyModeEnv, upgradeRoomReplyReferenceDrop),
		),
		// 技能换装背包 / 「Buff 强化」换装栏（op1428）档，见 skill_switch_inventory.go。
		// **默认 shape_guard = 修复档**：落库（幂等），并在下发前校验包体形状与客户端
		// 1428 入站解析器（u8 count + count×(u32,u32)）一致；形状不符**一个字节都不发**，
		// 只记 game-skill-switch-inventory-projection-blocked。
		// ⚠️ 这里的显式默认值必须与 normalizeSkillSwitchInventoryMode 的 default 分支一致：
		// envString 在变量未设时返回的是**这个字面量**，写成 project 会让"空环境"绕过
		// normalize 的兜底、静默回到旧的有害档（2026-09-28 线上回显 skill_switch_inventory=project
		// 就是这个坑，已修）。project（别名 verbatim/legacy_project）= 原样回放，仅作一键回退；
		// record = 只落库不下发（受控取证档）；reference_drop = 逐字节回到改动前。
		skillSwitchInventoryMode: normalizeSkillSwitchInventoryMode(
			envString(skillSwitchInventoryModeEnv, skillSwitchInventoryShapeGuardTier),
		),
		// op1428 **上行记录读法**（`DNFBRIDGE_SKILL_SWITCH_RECORD_BODY`）。
		// **默认 v4 = 修复档**：实机证据（活进程报文缓冲 + 帧长算术）证明客户端真实包体
		// 是 4 字节 `u8 slot | u8 mode | u16 value`；库内 6 字节行的前 2 字节是本仓
		// 13 字节 legacy 分帧把客户端 u32 序号读成 u16 之后残留的序号高 16 位。
		// 旧行按固定映射 dual-read，不需要迁移；重收一次时才改写成 4 字节规范形并打
		// `layout_upgraded=true`。写 v6（别名 legacy/today/old/6）= 逐字节回到改动前。
		// 见 internal/modules/dnf/skillswitch/record.go 与 store.go。
		// ⚠️ 这里的显式默认值必须与 skillswitch.NormalizeLayout 的 default 分支一致。
		skillSwitchInventoryRecordBodyLayout: skillswitch.NormalizeLayout(
			envString(skillSwitchInventoryRecordBodyEnv, string(skillswitch.LayoutV4)),
		),
		// 下行 `(key,value)` 候选对**实验**（`DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`）。
		// **默认 off = 关**：不发任何实验帧、逐字节等于今天。只有显式 identity12 才在
		// 既有合法投影边界上多发一帧 `u8 count=12 + 12×(u32,u32)` 的恒等对。
		// 那 12 对**未被证明**就是客户端 manager 329 的 map（structure-only candidate）；
		// family-2「slot→源槽位」已被 1.39 GB 全内存扫描证伪，永不使用。
		// 见 skill_switch_inventory_candidate_pairs.go。
		skillSwitchCandidatePairsMode: normalizeSkillSwitchCandidatePairsMode(
			envString(skillSwitchCandidatePairsEnv, skillSwitchCandidatePairsOff),
		),
		// 开盒子（op208/op1128）回包后的容器刷新口径，见 box_open_container_refresh.go。
		// **默认 current = 逐字节保持今天**的五个整容器回放（那次分诊写明没有客户端/
		// 厂商证据可以翻转，changlog.md:1103-1106）；写 changed_only 才只重发本次真正
		// 改变的容器（5 包 / 线上 19,175 B → 1~2 包），写回 current 一键回退。
		boxOpenContainerRefreshChangedOnly: normalizeBoxOpenContainerRefreshMode(
			envString(boxOpenContainerRefreshEnv, boxOpenContainerRefreshCurrent),
		) == boxOpenContainerRefreshChangedOnly,
		// 穿脱装备/换时装的外观刷新那一次调用内的只读仓储记忆，见
		// equip_mode0_repository_memo.go。**默认 reuse = 修复档**（实测
		// 6 装备读 + 2 角色读 + 1 账号读 → 各 1 次）；off = 逐字节回到今天。
		equipMode0RepositoryMemoMode: normalizeEquipMode0RepositoryMemoMode(
			envString(equipMode0RepositoryMemoEnv, equipMode0RepositoryMemoReuse),
		),

		accountAPIListen:        envString("DNFBRIDGE_ACCOUNT_API_LISTEN", defaultAccountAPIListen),
		accountAPIDSN:           envString("DNFBRIDGE_ACCOUNT_API_DSN", ""),
		accountAPITable:         envString("DNFBRIDGE_ACCOUNT_API_TABLE", defaultAccountAPITable),
		accountAPIRegister:      envBool("DNFBRIDGE_ACCOUNT_API_ALLOW_REGISTER", true),
		accountAPILoginToken:    envBool("DNFBRIDGE_ACCOUNT_API_LOGIN_TOKEN", true),
		playerStrict:            envBool("DNFBRIDGE_PLAYER_STRICT", false),
		playerAdminSecret:       envString("DNFBRIDGE_PLAYER_ADMIN_SECRET", ""),
		playerProxySecret:       envString("DNFBRIDGE_PLAYER_PROXY_SECRET", ""),
		playerRegistrationLimit: envInt("DNFBRIDGE_PLAYER_REGISTRATION_LIMIT", 0),
		playerMaxOnline:         envInt("DNFBRIDGE_PLAYER_MAX_ONLINE", 0),
		// 握手顺序回退开关：置 1 才恢复"未绑定会话收主动端点成功包"的旧行为。
		gameProactiveEndpointWhenUnbound: envBool("DNFBRIDGE_GAME_PROACTIVE_ENDPOINT_WHEN_UNBOUND", false),
		patchDistDir:                     envString("DNFBRIDGE_PATCH_DIST_DIR", defaultPatchDistDir),
		patchPayloadDir:                  envString("DNFBRIDGE_PATCH_DIR", defaultPatchPayloadDir),
		tutorialDeferUserState:           envBool("DNFBRIDGE_TUTORIAL_DEFER_USER_STATE", true),
	}
	// 单变量 A/B 开关（默认 zero = 现行为）：只影响副本内发给其他队员的 op2/op9 的
	// owner 字节。非法取值回退默认并在启动时记一条日志。
	opts.partyDungeonOwner, opts.partyDungeonOwnerEnvRaw, opts.partyDungeonOwnerEnvBad =
		readPartyDungeonOwnerMode()
	// 副本内 op3 USER_STATE 的成员口径（**默认 self = 逐字节保持今天**：只声明自己一条）。
	// 置 all 后按"当前队伍副本实例里实际在场的成员"逐条声明 state=1，见
	// dungeon_party_user_state_members.go。非法取值回退 self 并在启动时记一条日志。
	opts.partyDungeonUserStateAll, opts.partyDungeonUserStateEnvRaw, opts.partyDungeonUserStateEnvBad =
		readPartyDungeonAllMemberUserState()
	// 副本内"队友位移转发"开关（默认 off = 现行为：一条都不发）。
	opts.partyDungeonPeerPosition = readPartyDungeonPeerPositionMode(envString(partyDungeonPeerPositionEnv, ""))
	// 组队 UDP 中继的两个单变量实验开关（**默认都是现行为**，只有显式写非默认值才生效）：
	//   DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK=advertised
	//     —— 反向腿未学习时用对端自报端口再写一次（party_udp_relay_endpoint_fallback.go）；
	//   DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT=reported
	//     —— op11 里"自己那一行"改用客户端 CS op2 自报的 endpoint（party_peer_self_endpoint.go）。
	opts.partyUDPRelayEndpointFallback = readPartyUDPRelayEndpointFallbackMode(
		envString(partyUDPRelayEndpointFallbackEnv, ""))
	// 首包留存重放（**默认 on = 修法生效**，写 `off` 一键回到"首包被丢"的旧行为）：
	// 现场六次实测每次只有 1 个数据报到达中继、被 unlearned_peer_endpoint 丢掉后整场再无流量。
	// 见 party_udp_relay_probe_replay.go。
	opts.partyUDPRelayProbeReplay = readPartyUDPRelayProbeReplayMode(
		envString(partyUDPRelayProbeReplayEnv, ""))
	// 常驻原生探针监听 + 种子端点投递 + 每包 trace（**默认 on/on/off**）：
	// 现场七次实测都是"整场只有 1 个数据报到达中继、0 转发"，根因是中继手上没有任何
	// **自己观测到的**对端端点（客户端自报端口在加速器下必然不可达：61566 实发 21574）。
	// 厂商靠"组队前就把两端端点 bind 好"从第一个数据报起双向可达（见本文件头部对照）。
	// 见 party_udp_relay_probe_listener.go。
	opts.partyUDPRelayProbeListen = readPartyUDPRelayProbeListenMode(
		envString(partyUDPRelayProbeListenEnv, ""))
	opts.partyUDPRelaySeedForward = readPartyUDPRelaySeedForwardMode(
		envString(partyUDPRelaySeedForwardEnv, ""))
	opts.partyUDPRelayDatagramTrace = readPartyUDPRelayDatagramTraceMode(
		envString(partyUDPRelayDatagramTraceEnv, ""))
	// 无归属观测端点进候选集（**默认 on**）：2026-09-26 02:13 现场实测客户端的原生探测载荷
	// 只有 1 字节（`01`/`00`，= 第几个通告口），不含角色 id ⇒ 端点进不了 probes[charID]，
	// 而它恰恰是两台客户端真实的加速器映射端点。见 party_udp_relay_probe_listener.go。
	opts.partyUDPRelayObservedEndpointFanout = readPartyUDPRelayObservedEndpointFanoutMode(
		envString(partyUDPRelayObservedEndpointFanoutEnv, ""))
	// 共享端口档（默认 0 = off）：把对端端口从"各自腿口"换成"客户端唯一持续保活的口"。
	opts.partyUDPRelayViaPort = envInt(partyUDPRelayViaPortEnv, 0)
	if opts.partyUDPRelayViaPort < 0 || opts.partyUDPRelayViaPort > 65535 {
		opts.partyUDPRelayViaPort = 0
	}
	opts.partyUDPRelayProbePorts = partyUDPRelayProbePorts(
		envString(partyUDPRelayProbePortsEnv, ""), opts.partyUDPRelayPortStart)
	// 陈旧记忆的新鲜度上限（默认 30 秒）：21:15 那次转发目标 21590 是 21:07 留下的旧 NAT 端口，
	// 三份连续转发全被加速器丢掉 ⇒ 陈旧备忘只能作为多候选扇出里的一份。
	opts.partyUDPRelayMemoTTL = readPartyUDPRelayMemoTTL(
		envString(partyUDPRelayMemoTTLSecondsEnv, ""))
	// "让沉默对端开口"（默认 on）：厂商证据显示 op11 行到达后 4~72 ms 客户端就会自己发 P2P 探测，
	// 所以死锁时给沉默成员重推一次名册（严格限量，见 party_udp_relay_silent_peer_kick.go）。
	opts.partyUDPRelaySilentPeerKick = readPartyUDPRelaySilentPeerKickMode(
		envString(partyUDPRelaySilentPeerKickEnv, ""))
	// 催沉默端的门槛（默认 2s）与"沉默期间重投待投首包"的总窗口（默认 60s、每 3s 一轮）：
	// 23:31 那次唯一的数据报在腿龄 65ms 时到达 ⇒ 一次性判断必然错过，必须延迟+周期重试。
	opts.partyUDPRelayKickTiming = readPartyUDPRelayKickTiming(
		envString(partyUDPRelaySilentPeerKickDelayEnv, ""),
		envString(partyUDPRelayProbeResendSecondsEnv, ""))
	opts.partyUDPRelayKickMinAge = opts.partyUDPRelayKickTiming.minAge
	opts.partyUDPRelayResendWindow = opts.partyUDPRelayKickTiming.window
	// remote 档（厂商架构）的共享密钥：只在 optionsFromConfig 里解析一次。
	// **未配/非法 ⇒ 密钥为 nil ⇒ Service 构造时回退 embedded 并记一条具名事件**，
	// 绝不因此启动失败；状态词只用于日志，不回显内容。见 party_udp_relay_remote.go。
	opts.partyUDPRelayRemoteSecret, opts.partyUDPRelayRemoteSecretState = readPartyUDPRelayRemoteSecret(
		envString(partyUDPRelayRemoteSecretEnv, ""))
	opts.partyPeerSelfEndpoint = readPartyPeerSelfEndpointMode(
		envString(partyPeerSelfEndpointEnv, ""))
	// CS op2 端口字节序（2026-09-25 本轮厂商实测配对）：**默认 be**（端口大端、同一结构里
	// 的 MTU 仍是小端）；写 `le` 回到上一轮 M0 的小端读数。见 party_peer_endpoint.go。
	opts.partyPeerPortEndian = readPartyPeerPortEndianMode(
		envString(partyPeerPortEndianEnv, ""))
	// op11 行集合（厂商 9.16 形状）：**默认 off = 只发对端行**；写 `on` 回到我们旧的
	// "含自己那一行"形状。见 party_peer_endpoint_include_self.go。
	opts.partyPeerEndpointIncludeSelf = readPartyPeerEndpointIncludeSelfMode(
		envString(partyPeerEndpointIncludeSelfEnv, ""))
	// 同局域网直连档：**默认 relay = 改动前行为**（op11 对端行写中继端点）；
	// 写 `lan` 后在"两端自报 inner 相同 + 服务端观测到的两端 outer 相同"时改写为
	// 对端 inner 地址 + 自报端口，避免同局域网两队客户端被 NAT hairpinning 挡住。
	// 证据、假阳性护栏与一键回退见 party_lan_direct.go。
	opts.partyLANDirect = readPartyLANDirectMode(
		envString(partyLANDirectEnv, ""))
	// formation 三包顺序：**默认 vendor = op11 barrier → op153 → op9**；
	// `vendor_wire` = 厂商日志实际字节序（op11 → op9 → op153）；`legacy` = 旧顺序一键回退。
	// 见 party_formation_order.go。
	opts.partyFormationOrder = readPartyFormationOrderMode(
		envString(partyFormationOrderEnv, ""))
	// 队员"退出队伍"必须真的离队：**默认 repair = 修好后的行为**；
	// 写 `legacy` 一键回到改动前（只清自己投影 + 回 ACK，不通知队长）。见 party_leave_repair.go。
	opts.partyLeaveRepair = readPartyLeaveRepairMode(
		envString(partyLeaveRepairEnv, ""))
	// 外部可触发的"强制组队"测试入口：**默认 off**（不启动轮询 goroutine、
	// 不读命令文件、不发一个字节）。见 party_force_trigger.go。
	opts.partyForce = readPartyForceMode(
		envString(partyForceEnv, ""))
	opts.partyForceCommandPath = strings.TrimSpace(
		envString(partyForceCommandPathEnv, defaultPartyForceCommandPath))
	opts.partyForceResultPath = strings.TrimSpace(
		envString(partyForceResultPathEnv, defaultPartyForceResultPath))
	opts.partyForceInterval = partyForceIntervalFromMS(
		envInt(partyForceIntervalEnv, defaultPartyForceIntervalMS))
	// 外部可触发的"服务端代发真实组队邀请"测试入口：**默认 off**。见 party_force_invite.go。
	opts.partyForceInvite = readPartyForceMode(
		envString(partyForceInviteEnv, ""))
	opts.partyForceInviteCommandPath = strings.TrimSpace(
		envString(partyForceInviteCommandPathEnv, defaultPartyForceInviteCommandPath))
	opts.partyForceInviteResultPath = strings.TrimSpace(
		envString(partyForceInviteResultPathEnv, defaultPartyForceInviteResultPath))
	opts.partyForceInviteInterval = partyForceIntervalFromMS(
		envInt(partyForceInviteIntervalEnv, defaultPartyForceInviteIntervalMS))
	// 代发邀请的同屏前置闸门：**默认 require_same_area**（只在两端存在记录都齐全、
	// 而分桶键不同时才拒绝）；`off` = 逐字节回到改动前。
	opts.partyForceInviteCoPresence = readPartyForceInviteCoPresenceMode(
		envString(partyForceInviteCoPresenceEnv, ""))
	// M1（D6）与 M2（D7）两个对齐开关：**默认都是 off = 现行为**。
	opts.partyPeerEndpointsAfterRoster = readPartyPeerEndpointsAfterRosterMode(
		envString(partyPeerEndpointsAfterRosterEnv, ""))
	opts.partyPeerEndpointRebroadcast = readPartyPeerEndpointRebroadcastMode(
		envString(partyPeerEndpointRebroadcastEnv, ""))
	// op11 对端行的 acc/nat/mtu/char_attr 四字段：**默认 vendor = 厂商 9.16 实测值**
	// （`ROW_ACC=auto` 时 acc 按"每会话一个进程内序号"算；显式 u32 则钉死）。
	// `legacy` = char_id/0/1500/0，逐字节回退。见 party_peer_row_vendor_fields.go。
	opts.partyPeerRowVendorFields = readPartyPeerRowVendorFieldsMode(
		envString(partyPeerRowVendorFieldsEnv, ""))
	opts.partyPeerRowAcc = readPartyPeerRowAccSetting(
		envString(partyPeerRowAccEnv, ""))
	// 副本进场补 op11 remotes（厂商 packet_order ①）：**默认 vendor = 补**；
	// `legacy` = 不补（逐字节回退）。见 party_dungeon_entry_peer_endpoints.go。
	opts.partyDungeonEntryPeerEndpoints = readPartyDungeonEntryPeerEndpointsMode(
		envString(partyDungeonEntryPeerEndpointsEnv, ""))
	// 过图/开始地图补 op9 房间槽位表（厂商 packet_order ③）：**默认 vendor = 每次
	// start-map 提交各一条**；`once` = 仅当本次提交还没有 kind0 槽位表；`legacy` = 不发。
	// 见 dungeon_party_room_slot_table.go。
	opts.dungeonPartyRoomSlotTable = readDungeonPartyRoomSlotTableMode(
		envString(dungeonPartyRoomSlotTableEnv, ""))
	// 进图那一次提交里同一个 actor 的两条 kind0 槽位表：**默认 current = 都发**（今天）；
	// `vendor_only` = 让位落位里那条重复的名册帧（厂商每个提交只发一条）。
	// 见 dungeon_party_room_slot_table.go 的 dungeonPartyEntrySlotTableEnv。
	opts.partyDungeonEntrySlotTable = readDungeonPartyEntrySlotTableMode(
		envString(dungeonPartyEntrySlotTableEnv, ""))
	// 队员"跟随者选择器转换"那一批里的 op9 名册帧：**默认 current = 照发**（今天）；
	// `skip` = 只让位那条 op9（op153 实时信息照发）。
	// 见 game_enter_select_dungeon.go 的 partyDungeonEntryFollowerFrameEnv。
	opts.partyDungeonEntryFollowerFrame = readPartyDungeonEntryFollowerFrameMode(
		envString(partyDungeonEntryFollowerFrameEnv, ""))
	// start-map 收尾的 op36 fatigue 状态：**默认 vendor = 补**（位置在 op9 之前）；
	// `legacy` = 不发。见 dungeon_party_room_slot_table.go。
	opts.dungeonStartMapFatigue = readDungeonStartMapFatigueMode(
		envString(dungeonStartMapFatigueEnv, ""))
	// 进场屏障（厂商 `all-members-through-op28-before-any-op800-op29`）：
	// **默认 vendor = 全队走到 op28 之后再统一放行 op800+op29**；
	// `legacy` = 改动前（队长整条跑完再驱动队员、不发 op800）。
	// 见 party_dungeon_entry_barrier.go。
	opts.partyDungeonEntryBarrier = readPartyDungeonEntryBarrierMode(
		envString(partyDungeonEntryBarrierEnv, ""))
	// 副本死亡上报的房间证明：**默认 strict = 改动前行为**（形状/owner 不可证就拒收）；
	// `room_proven` = op39 目标在册即放行。见 dungeon_death_room_proof.go。
	opts.dungeonDeathRoomProof = readDungeonDeathRoomProofMode(
		envString(dungeonDeathRoomProofEnv, ""))
	// 全员读条屏障（厂商 `party_dungeon_shared_loading_barrier`）：
	// **默认 vendor = 组队副本里落位与 op37 主状态按"全队 op37 到齐"放行**；
	// `legacy` = 改动前（各自请求各自放行）。见 party_dungeon_loading_barrier.go。
	opts.partyDungeonLoadingBarrier = readPartyDungeonLoadingBarrierMode(
		envString(partyDungeonLoadingBarrierEnv, ""))
	// 深渊派对（hell party）进图前置条件：**默认 vendor = 校验 `[hell dungeon]` /
	// `[hell quest]` 并且一定回 `{0, code, 0}` 拒绝包**；`legacy` = 改动前
	// （不校验、不回包）。现场症状：未完成深渊派对任务也能点「挑战深渊派对」，
	// 点了以后卡在选图界面。见 dungeon_hell_party_entry.go。
	opts.hellPartyEntry = readHellPartyEntryMode(
		envString(hellPartyEntryEnv, ""))
	// 深渊派对准入的"逐槽位判定 + blocked 槽位下发"：**默认 off = 逐字节今天**
	// （深渊请求一律走上面那一层的拒绝）；`on` = 厂商口径逐槽位判定、blocked 槽位随
	// class0/op27 进本选择页下发，且不再因为深渊准入拒绝进图（第 69 条
	// 「挑战深渊进不去副本」）。一键回退 = 删除该 env。
	// 见 dungeon_hell_blocked_slots.go。
	opts.hellBlockedSlots = readHellBlockedSlotsMode(
		envString(hellBlockedSlotsEnv, ""))
	// 起始/首领房地图真源优先级：**默认 specification = PVF `[map specification]` /
	// `[boss map]` 显式声明优先**（修复 104/101 清房不开门的根因）；
	// `ownership` = 改动前口径（同副本/同目录文件名启发式优先），一键回退。
	// 见 dungeon_start_map_precedence.go。
	opts.dungeonStartMapPrecedence = readDungeonStartMapPrecedence(
		envString(dungeonStartMapPrecedenceEnv, ""))
	// 锦标赛选奖应答档：**默认 select = 客户端自己的 2 字节选奖回 458 选中投影**
	// （真机 `sub_1CF5010` 是那条包的 reader）；只有显式 `state` 才回退到改动前
	// （任何路径都不发 458）。一键回退 = 删除该 env。
	// 现场与真机派发表取证见 dungeon_tournament_config.go。
	opts.dungeonTournamentRewardAck = readDungeonTournamentRewardAckMode(
		envString(dungeonTournamentRewardAckEnvKey, ""))
	// 深渊按钮置灰的判定时机：**默认 vendor = 开选择页时判、结果随 class0/op27
	// （厂商 NOTI 0x001B）下发**（厂商 `DungeonEntryHandler.cs:723-726`/`:780-786`）；
	// `legacy` = 改动前只在 op16 判。见 dungeon_hell_select_page.go。
	opts.hellSelectPageEligibility = readHellSelectPageEligibilityMode(
		envString(hellSelectPageEligibilityEnv, ""))
	opts.dungeonHellRoom = readDungeonHellRoomMode(
		envString(dungeonHellRoomEnv, ""))
	opts.reviveStateNotify = readReviveStateNotifyMode(
		envString(reviveStateNotifyEnv, ""))
	// op16 `body[7]`（`SelectionMode`）的独立放行轴：**默认 strict = 逐字节今天**
	// （非 0 一律按"当前 EXE 不支持的模式"拒绝）；只有显式 `allow` 才放行。
	// 一键回退 = 删除该 env。见 dungeon_entry_packets.go。
	opts.dungeonEntrySelectionMode = readDungeonEntrySelectionMode(
		envString(dungeonEntrySelectionModeEnv, ""))
	// 史诗碎片 → 账号级史诗图鉴：**默认 on**（碎片不再进背包）；`always` = 连
	// entries=0 的空表也发（厂商 9.16 形状）；`off` = 一键回到改动前的背包行为。
	// 见 epic_piece_book.go。
	opts.epicPieceBook = readEpicPieceBookMode(envString(epicPieceBookEnv, ""))
	// 史诗碎片的**另外两条入包路径**（邮件/GM 领取 + 登录期存量清扫）：默认 on。
	// 现场症状：GM 工具发碎片 → 邮件领取 → 碎片作为普通 [material] 出现在材料页。
	// `off` 只回退这两条路径，不影响地面拾取。见 epic_piece_grant.go。
	opts.epicPieceGrant = readEpicPieceGrantMode(envString(epicPieceGrantEnv, ""))
	// 史诗图鉴默认实时同步；保留显式login_only/vendor_empty/off回退。
	// 当前EXE模式1覆盖绝对值并刷新UI，见epic_piece_push.go中的本机核对依据。
	opts.epicPiecePush = readEpicPiecePushMode(envString(epicPiecePushEnv, ""))
	// 史诗制作（自制史诗 NPC）的两个开关：**默认都是厂商/修正后形状**。
	//   ACCOUNT_SCOPE=session（默认）—— 归属校验改用会话账号（与角色表同口径）；
	//     `shared` 一键回退到 s.accountID()（现场症状：选武器 → 「用户信息错误」）。
	//   INFO_PUSH=always（默认）—— 进屋就发 op1355（含 target=0），对齐厂商 9.16；
	//     `selected_only` 一键回退到"没选武器不发"。
	// 见 epic_production_account_scope.go 文件头（含香港现场 hex 与厂商夹具）。
	opts.epicProductionAccountScope = readEpicProductionAccountScopeMode(
		envString(epicProductionAccountScopeEnv, ""))
	opts.epicProductionInfoPush = readEpicProductionInfoPushMode(
		envString(epicProductionInfoPushEnv, ""))
	// 副职业（分解机/附魔师）金币口径：**默认 character_stats = 读 dnf_characters.gold**
	// （= Character.Stats["gold"]，与客户端钱包投影同源）；
	// `inventory_wallet` 一键回退到改动前的背包伪槽 `list 0 slot 0`
	// （现场症状：自己用自己的分解机 → 「所持金币不足」，回包 `00 15`）。
	// 见 expert_job_gold_source.go 文件头（含香港 packet_log 行号与 DB 原文）。
	opts.expertJobGoldSource = readExpertJobGoldSourceMode(
		envString(expertJobGoldSourceEnv, ""))
	// op108 在线活动目录里 2365（竞技大赛/马战）两行的独立兜底开关：
	// 默认 auto = 跟随 DNFBRIDGE_JOUST_EVENT_ENABLED；off = 无论马战总开关如何都
	// 不发 2365；on = 强制发。非法取值回退 auto 并在启动时记一条日志。
	// 见 online_event_spend_time.go 的 currentSpendTimeJoustCatalogEnv。
	opts.joustEventCatalogEnvRaw = strings.TrimSpace(envString(currentSpendTimeJoustCatalogEnv, ""))
	opts.joustEventCatalogMode, opts.joustEventCatalogEnvBad =
		normalizeCurrentSpendTimeJoustCatalogMode(opts.joustEventCatalogEnvRaw)
	if opts.maxPacketBytes <= 0 {
		opts.maxPacketBytes = defaultMaxPacketBytes
	}
	if opts.channelServerID <= 0 {
		opts.channelServerID = 1
	}
	if opts.channelAdvertiseID < 0 {
		opts.channelAdvertiseID = opts.channelServerID
	}
	if opts.partyUDPRelayPortStart < 1 || opts.partyUDPRelayPortStart > 65535 {
		opts.partyUDPRelayPortStart = defaultPartyUDPRelayPortStart
	}
	if opts.partyUDPRelayPortCount < 1 ||
		opts.partyUDPRelayPortCount > 1024 ||
		opts.partyUDPRelayPortStart+opts.partyUDPRelayPortCount-1 > 65535 {
		opts.partyUDPRelayPortCount = defaultPartyUDPRelayPortCount
	}
	return opts
}

func choosePVFPath(cfg config.ServiceConfig) string {
	for _, key := range []string{"DNFBRIDGE_PVF_PATH", "DNF_SCRIPT_PVF_PATH", "DNF_PVF_PATH"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(cfg.PVF.Path); value != "" {
		return value
	}
	return defaultPVFPath
}

func choosePVFMaxBytes(cfg config.ServiceConfig) int64 {
	for _, key := range []string{"DNFBRIDGE_PVF_MAX_BYTES", "DNF_SCRIPT_PVF_MAX_BYTES", "DNF_PVF_MAX_BYTES"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	if cfg.PVF.MaxBytes > 0 {
		return cfg.PVF.MaxBytes
	}
	return platformpvf.DefaultMaxBytes
}

func normalizeChannelInfoBodyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", channelInfoBodyLatest, "csharp", "legacy", "legacy_csharp", "old_csharp":
		// IDA/MCP 与 86JP_protocol.log 复核后，最新客户端 0x12 明文必须带 server_count/server_id/count。
		// 旧 C# 的 2 字节保留位前缀会导致客户端把 0x000B0000 当 server_count 反复解析。
		return channelInfoBodyLatest
	default:
		return channelInfoBodyLatest
	}
}

func normalizeGameInitialMode(value string) string {
	// DOVE/C# 对齐：首包模式不再可配置，game 建连后固定主动发送 class=0 op=1。
	return gameInitialModeNotice
}

func normalizeGamePreBootstrap(value string) string {
	// C# 端 game 连接后只主动发首包；其余初始化包都由客户端 C2S 触发。
	// 旧探针开关保留为兼容配置名，但正常链路统一关闭，避免提前推送请求触发包。
	return gamePreBootstrapNone
}

func normalizeGamePostBootstrap(value string) string {
	// C# 行为证据：GET_USERINFO/SELECT_CHARACTER 后续包必须等客户端请求后再回。
	// 因此 post bootstrap 探针不再通过环境变量开启。
	return gamePostBootstrapNone
}

func normalizeGameUpperHeader(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", gameUpperHeaderChannel13, "13", "channel", "raw13":
		return gameUpperHeaderChannel13
	case gameUpperHeaderServer16, "16", "raw16":
		return gameUpperHeaderServer16
	default:
		return gameUpperHeaderChannel13
	}
}

func normalizeGameUpperBodyCodec(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", gameUpperBodyCodecAuto, "codec", "encoded", "latest":
		return gameUpperBodyCodecAuto
	case gameUpperBodyCodecPlain, "plain", "none", "off", "decodecopy":
		return gameUpperBodyCodecPlain
	default:
		return gameUpperBodyCodecAuto
	}
}

func normalizeGameUpperClientBodyCodec(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", gameUpperClientBodyCodecPlain, "plain", "none", "off", "disabled", "passthrough":
		return gameUpperClientBodyCodecPlain
	case gameUpperClientBodyCodecProbe, "detect", "log", "log_only", "log-only":
		return gameUpperClientBodyCodecProbe
	case gameUpperClientBodyCodecNative, "decode", "decoded", "server", "server_native":
		return gameUpperClientBodyCodecNative
	default:
		return gameUpperClientBodyCodecPlain
	}
}

func normalizeGameDprotoMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case gameDprotoModeNative, "native_dproto", "protected":
		return gameDprotoModeNative
	case "", gameDprotoModeLegacy, "legacy", "patch", "compat":
		return gameDprotoModeLegacy
	default:
		return gameDprotoModeLegacy
	}
}

func chooseServerIP(cfg config.ServiceConfig) string {
	for _, key := range []string{"DNFBRIDGE_SERVER_IP", "DNFBRIDGE_PUBLIC_HOST", "DFO_PUBLIC_SERVER_IP", "SERVER_IP"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if host := hostFromAddr(cfg.Service.PublicAddr); host != "" {
		return host
	}
	return defaultServerIP
}

func hostFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstEnv(keys []string, fallback string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func readMainQuestExperienceMultiplier() int {
	raw := strings.TrimSpace(os.Getenv(mainQuestExperienceMultiplierEnv))
	if raw == "" {
		return 1
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 100 {
		return 1
	}
	return value
}

func readMainQuestExperiencePercent() int {
	raw := strings.TrimSpace(os.Getenv(mainQuestExperiencePercentEnv))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 100 {
		return mainQuestExperiencePercentInvalid
	}
	return value
}

func envIntAny(keys []string, fallback int) int {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			continue
		}
		return envInt(key, fallback)
	}
	return fallback
}

func envUint32(key string, fallback uint32) uint32 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		parsed, err = parseFlexibleUint32(value)
	}
	if err != nil {
		return fallback
	}
	return uint32(parsed)
}

func envUint32Any(keys []string, fallback uint32) uint32 {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			continue
		}
		return envUint32(key, fallback)
	}
	return fallback
}

func parseFlexibleUint32(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	trimmed := strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if len(trimmed) > 8 {
		// 启动参数里的 32 位 hex token 不是 uint32；调试时传入该值时只取前 4 字节作为 outer 校验候选。
		trimmed = trimmed[:8]
	}
	return strconv.ParseUint(trimmed, 16, 32)
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
