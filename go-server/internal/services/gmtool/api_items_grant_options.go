package gmtool

// J13：发放 option（expire_days / reinforce / amplify_type / equipment_quality / forging_level）。
//
// 上游依据（UP = `H:\86JP启动器\90CN\90原神开源0.1.8\dnf90-source-oneclick-main\go-server`）：
//
//	UP internal/services/dnfbridge/gm.go:1780-1947  GMSendItemMailWithOptions 的 option 段
//	  :1805        装备 + count != 1 -> 明确拒绝
//	  :1808-1813   边界：expire_days 0..3650、reinforce 0..31、amplify_type 0..4、
//	               equipment_quality ∈ {"", default, random, top}（两端裁剪 + 大小写不敏感）
//	  :1815-1817   非装备带装备专属 option -> 明确拒绝
//	  :1818-1821   装备类型不支持这些 option（时装/宠物/称号/宠物装备…）-> 明确拒绝
//	  :1843-1850   expire_days > 0 才覆盖到期时间；=0 **完全不介入**（保持 PVF 默认过期）
//	  :1865-1869   reinforce   -> extra `reinforce` + `ext_data0`
//	  :1870-1874   amplify_type -> extra `amplify_type` + `byte_13`
//	  :1875-1889   equipment_quality -> 品级种子（random 随机 / top 满品级哨兵）
//	  :1948-1955   gmItemSupportsEquipmentAttributes 的支持类型表
//	UP internal/services/dnfbridge/item_expiration_projection.go:268-317
//	             applyCurrentGMExpirationOverrideAt（本文件按计划书 §4.7 测试清单第 9 条
//	             移植为**纯日期**计算：只做 time.Time 加减与 Extra 记账键写入）
//
// 开关：`DNFBRIDGE_GM_MAIL_OPTIONS`。**空 = off，只认显式 `on`**（与
// online_guard.go:61-63 的 RequireOfflineFromEnv、api_quest_mutations.go:117-119 的
// QuestProfessionWriteFromEnv 同一约定：`true`/`1`/`yes` 一律不算）。
//
// off 档（默认）的可观察行为 = 今天：handleItemGrant 对 `options` **一个字节都不解析** ——
// 不校验、不投影、不写日志；`options` 只参与既有的幂等指纹（api_items.go:409-412），
// 发出去的邮件与改动前逐字节相同。注意 off 档**连未知字段都不拒绝**：今天的前端就在发
// `options:{qualityMode:…}`，凭空拒绝它会改变今天的可观察行为。
//
// 品级种子的唯一实现：本文件直接调用 `modules/dnf/itemquality`（TopSeed / NewRandomSeed /
// Apply）。dnfbridge 的三个包装（equipment_quality.go:12 `currentEquipmentTopQualitySeed`
// = itemquality.TopSeed、:16-18 `newCurrentEquipmentQualitySeed`、:61-63
// `applyCurrentEquipmentQualitySeed`）是一行委托，gmtool 不能引用包内未导出符号、
// 也不许改 dnfbridge，所以这里复用它们背后的**同一份实现**，绝不另写一套种子算法。
//
// # 锻造（J13-F）：第五个 option `forging_level`
//
// 现场报障（操作者原话）：「发放道具对话框里有「锻造」，填了按发放却什么都不发生」。
// 根因是前端把那个输入框的值读进 giveConfigMemory 之后就丢了（give.js:546 渲染、
// :413 记忆、:627-641 组装请求时**没有**对应字段），而服务端当时的 options 词表里
// 也没有锻造 —— 旧页面缓存发的 `forgingLevel`（驼峰）会被严格解码具名拒绝。
//
// 现在它是一个与另外四个同族的 option（键名沿用发放侧的 snake_case 约定
// `forging_level`，语义与在线配置接口的 `forgingLevel` 完全相同）：
//
//	零值 = 不介入      forging_level=0 时一个字节都不写（新发放的实例本来就是 0）
//	上限来自 PVF        etc/upgrade_separate.etc 的 [separate upgrade max]（真档 8）
//	只有武器可锻        itemcfgIsForgeableWeapon（[equipment type] 的首个 tag == "weapon"）
//	写点唯一            itemcfgSetForgingLevel（Extra 的 byte_57/value_57 + 0x77 行的 0x57）
//
// 为什么复用 api_items_configure.go 的实现而不在发放侧另写一套：锻造的落点
// （0x77 行的 0x57 字节）与上限来源都是该文件头「锻造落点」一节**取证过的结论**；
// 发放与在线配置只是同一个属性的两个入口，两套实现必然漂移。判定点只有一个
// （gmResolveForgingGrantLevel），校验与投影都走它。
//
// 失败一律具名 + 零写入（拒绝都发生在 AppendSystemMail 之前）：
//
//	forging_table_unavailable   读不到 etc/upgrade_separate.etc（说不出上限就不写）
//	forging_level_out_of_range  forging_level 不在 0..[separate upgrade max]
//	forging_not_weapon          非武器（含堆叠物/时装/称号）
//
// 开关：锻造与其余四个 option **同一个开关**（DNFBRIDGE_GM_MAIL_OPTIONS）。off 档
// 连 `forging_level` 这个已知字段都不解析，邮件逐字节与今天相同；on 档它是默认能力
// （不需要第二个开关：多一个开关只会让操作者再遇到一次"填了没反应"）。
//
// # 红字（J13-G）：第六件事 —— 类型与**数值**必须一起写
//
// 另外五个 option 落地之后，现场又暴露出同族的一个缺陷：`amplify_type` 只写了**类型**
// （amplify_type + byte_13），没写**数值**。vendor `ItemGrantOptions.cs:150-163` 在
// `AmplifyType > 0` 时是这么写的：
//
//	builder.AmplifyType  = (byte)options.AmplifyType;
//	builder.AmplifyValue = 0;
//	if (options.AmplifyType > 0) {
//	    var initial = resolveInitialAmplifyValue?.Invoke(metadata.Rarity) ?? (ushort)0;
//	    if (initial == 0) { error = "无法从 PVF 计算红字初始值"; return false; }  // 明确失败
//	    builder.AmplifyValue = initial;
//	}
//
// 客户端行 0x14..0x15（= Extra 的 marker_16 / value_d，inventory/body.go:454）就是这个
// 数值；只写类型不写数值，操作者拿到的是一件"有红字属性、数值 +0"的装备 ——
// 面板说成功、游戏里是空的，正是本仓红线 C3 的静默假成功。
//
// 判定点唯一（gmResolveAmplifyGrant），并且**复用**在线配置接口已在用的那份实现：
// api_items_configure.go:1383 `itemcfgAmplifyTable` → :1489 `initialValue`（逐字移植
// dnfbridge/amplify_item_pvf.go:240-253），写点也用同一个移植自游戏自身的写入器
// `itemcfgSetAmplifyState`（api_items_configure.go:1298 = inventory/owner_amplify.go:288-309
// setAmplifyState）。两个入口因此不可能漂移。
//
// 邮件投递路径**保留**锻造：附件 Extra 里落的就是 byte_57/value_57，领取端
// modules/dnf/mail/owner.go:819-864 mailboxAttachmentStack 只删三个 mailbox_* 信封键
// （其余 Extra 逐字带过去），客户端的 0x77 行由 modules/dnf/inventory/body.go:461
// 从 Extra 的 byte_57/value_57 生成 —— 所以邮件领进背包、乃至穿上身之后锻造等级仍在。
// 该结论由 TestP7ForgingOptionSurvivesMailClaimProjection 以源码级交叉断言钉住
// （领取发生在 dnfbridge/mail，gmtool 里没有那条路径可跑）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// 开关
// ---------------------------------------------------------------------------

// gmMailOptionsEnv 是"发放路由是否消费这 4 个 option"的开关名。
const gmMailOptionsEnv = "DNFBRIDGE_GM_MAIL_OPTIONS"

// GMMailOptionsFromEnv 解析 DNFBRIDGE_GM_MAIL_OPTIONS。**空 = off**，且只有显式 `on`
// （两端裁剪、大小写不敏感）才启用 —— 与 RequireOfflineFromEnv（online_guard.go:61-63）
// 同一约定：安全开关只认显式 on，`true`/`1`/`yes` 一律不算。
func GMMailOptionsFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

// GMMailOptionsFromEnvironment 读取进程环境里的同一个开关。
//
// 为什么直接 `os.Getenv` 而不经 `dnfbridge/options.go`：本仓先例是
// online_guard.go:65-73 与 api_quest_mutations.go:121-139 —— 开关自读环境，
// 不进改动最密集、且此刻有其它子代理在写的 dnfbridge 包。
func GMMailOptionsFromEnvironment() bool {
	return GMMailOptionsFromEnv(os.Getenv(gmMailOptionsEnv))
}

// ---------------------------------------------------------------------------
// 请求体：UP GMMailOptions（gm.go:259-266）的移植
// ---------------------------------------------------------------------------

// gmMailGrantOptions 是发放请求 `options` 对象里的 5 个字段。
// **零值 = 不介入**：ExpireDays=0 时完全不碰到期时间，其余四个零值/空串时也不写任何 Extra。
type gmMailGrantOptions struct {
	ExpireDays       int    `json:"expire_days"`
	Reinforce        int    `json:"reinforce"`
	AmplifyType      int    `json:"amplify_type"`
	EquipmentQuality string `json:"equipment_quality"`
	// ForgingLevel 是**锻造**（别升级 / separate upgrade）等级：0x77 物品行的 0x57 字节，
	// 与在线配置接口的 forgingLevel 同义（发放侧按既有约定用 snake_case）。
	// 0 = 不介入；> 0 时必须是武器且不超过 PVF 的 [separate upgrade max]。
	ForgingLevel int `json:"forging_level"`
}

// gmMailGrantOptionKeys 是 on 档唯一接受的字段名（严格解码用）。
var gmMailGrantOptionKeys = []string{"expire_days", "reinforce", "amplify_type", "equipment_quality", "forging_level"}

// parseGMMailGrantOptions 解析发放请求里的 options。
//
// 空 / `null` = 零值（合法）。**未知字段一律明确失败**：静默丢掉 operator 选好的品级、
// 却回一句"发放成功"，正是本仓红线 C3 禁止的"静默假成功"。旧页面缓存发的
// `qualityMode`/`upgradeLevel`/`forgingLevel`（wwwroot/js/give.js 改动前的字段名）会因此
// 被点名拒绝，操作者按提示强制刷新即可 —— 比默默发一件白板装备好。
func parseGMMailGrantOptions(raw json.RawMessage) (gmMailGrantOptions, error) {
	var options gmMailGrantOptions
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return options, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return gmMailGrantOptions{}, fmt.Errorf(
			"发放参数 options 无法识别（%s）。只接受 %s；"+
				"如果页面还在发送 qualityMode/upgradeLevel/forgingLevel 这类旧字段名，请强制刷新页面（Ctrl+F5）后重试",
			err.Error(), strings.Join(gmMailGrantOptionKeys, " / "))
	}
	return options, nil
}

// gmGrantOptionRejection 是"拒绝、零写入"的结构化原因：message 回给面板，
// reason 进具名日志（与 P2 的 mailboxDeleteRejection、P5 的 questMutationRejection 同形态）。
type gmGrantOptionRejection struct {
	reason  string
	message string
}

// Error 让拒绝本身可以作为 error 从投影阶段返回（applyGMMailGrantOptions 的第二道闸），
// 于是 reason 在两层之间同名 —— 见 gmGrantOptionRejectionReason。
func (e *gmGrantOptionRejection) Error() string { return e.message }

// 边界（UP gm.go:1809-1812）。
const (
	gmMailExpireDaysMax  = 3650
	gmMailReinforceMax   = 31
	gmMailAmplifyTypeMax = 4
)

// 品级词表（UP gm.go:1808/1812）。空串与 default 同义：不投影品级种子。
const (
	gmQualityDefault = "default"
	gmQualityRandom  = "random"
	gmQualityTop     = "top"
)

// normalizeGMMailEquipmentQuality 复刻 UP gm.go:1808 的归一化（两端裁剪 + 转小写）。
// ok=false 表示 token 不在词表里；空串与 default 都是合法值（不投影）。
func normalizeGMMailEquipmentQuality(value string) (string, bool) {
	quality := strings.ToLower(strings.TrimSpace(value))
	switch quality {
	case "", gmQualityDefault, gmQualityRandom, gmQualityTop:
		return quality, true
	default:
		return quality, false
	}
}

// gmEquipmentAttributeTags 是 UP gm.go:1948-1955 gmItemSupportsEquipmentAttributes 的
// 支持类型表。token 口径与 itemEntry.Tag 完全一致（items_index.go:334 firstItemTag：
// 取第一个 `[tag]`、lowercase、空白折叠），所以这里不再做第二次归一化。
var gmEquipmentAttributeTags = map[string]struct{}{
	"weapon": {}, "coat": {}, "shoulder": {}, "pants": {}, "shoes": {}, "waist": {},
	"amulet": {}, "wrist": {}, "ring": {}, "support": {}, "magic stone": {}, "support weapon": {},
}

// gmEntrySupportsEquipmentAttributes 对应 UP gm.go:1948-1955。
func gmEntrySupportsEquipmentAttributes(tag string) bool {
	_, supported := gmEquipmentAttributeTags[strings.ToLower(strings.TrimSpace(tag))]
	return supported
}

// gmResolveForgingGrantLevel 是发放路径上锻造等级的**唯一判定点**，规则与在线配置接口
// 逐条相同（api_items_configure.go:859-886），只是错误载体换成发放侧的
// gmGrantOptionRejection：
//
//	requested == 0             -> (0, nil)：不介入（与 reinforce=0 / amplify_type=0 同口径）
//	读不到 PVF 锻造表           -> forging_table_unavailable（绝不用写死的上限）
//	requested < 0 或 > 上限     -> forging_level_out_of_range（上限就是 [separate upgrade max]）
//	不是武器                    -> forging_not_weapon
//	否则                        -> (requested, nil)
//
// 具名错误全部复用 api_items_configure.go 的 itemcfgErr* 文案（同一属主的同一句提示），
// 面板与日志因此不会出现"同一个问题两种说法"。
func (s *Server) gmResolveForgingGrantLevel(entry itemEntry, requested int) (int, *gmGrantOptionRejection) {
	if requested == 0 {
		return 0, nil
	}
	// 上限的唯一来源：PVF。读不到就拒绝 —— api_items.go off 档能力块里那个 8 只是历史
	// 常量，不能拿来当门禁。
	table, err := s.itemcfgSeparateUpgradeTable()
	if err != nil {
		return 0, &gmGrantOptionRejection{
			reason: "forging_table_unavailable",
			message: fmt.Sprintf("%s: %s。",
				itemcfgErrForgingTableUnavailable.Error(), err.Error()),
		}
	}
	if requested < 0 || requested > table.maxLevel {
		return 0, &gmGrantOptionRejection{
			reason: "forging_level_out_of_range",
			message: fmt.Sprintf(
				"%s：forging_level=%d 必须在 0..%d（%s 的 [separate upgrade max]）。",
				itemcfgErrForgingLevelOutOfRange, requested, table.maxLevel, itemcfgSeparateUpgradePVFPath),
		}
	}
	if !itemcfgIsForgeableWeapon(entry) {
		return 0, &gmGrantOptionRejection{
			reason: "forging_not_weapon",
			message: fmt.Sprintf("%s（物品 %d（%s）的类型是 %s）。",
				itemcfgErrForgingNotWeapon, entry.ID, entry.Name, entry.Tag),
		}
	}
	return requested, nil
}

// gmResolveAmplifyGrant 是发放路径上"红字"（amplify_type）的**唯一判定点**：
// 类型合法之后，用**既有**的初值实现算出红字数值（api_items_configure.go:1383/1489，
// 与在线配置接口 :836-846 同一份 PVF 解析、同一个公式，也同样是 vendor
// AmplifyInitialValueResolver.cs 的移植）：
//
//	requested == 0               -> (0, 0, nil)：不介入（与 reinforce=0 / forging_level=0 同口径）
//	读不到 etc/amplifyitem.etc   -> amplify_table_unavailable（零写入）
//	该稀有度/类型算不出初值       -> amplify_value_unresolved（零写入，vendor :157-161 同一条明确失败）
//	否则                         -> (type, value, nil)
//
// 具名错误复用 api_items_configure.go 的 itemcfgErr* 文案（同一属主的同一句提示）。
func (s *Server) gmResolveAmplifyGrant(entry itemEntry, amplifyType int) (int, int, *gmGrantOptionRejection) {
	if amplifyType == 0 {
		return 0, 0, nil
	}
	table, err := s.itemcfgAmplifyTable()
	if err != nil {
		return 0, 0, &gmGrantOptionRejection{
			reason: "amplify_table_unavailable",
			message: fmt.Sprintf("%s: %s。",
				itemcfgErrAmplifyTableUnavailable.Error(), err.Error()),
		}
	}
	value, valueErr := table.initialValue(entry.Rarity, amplifyType)
	if valueErr != nil {
		return 0, 0, &gmGrantOptionRejection{
			reason: "amplify_value_unresolved",
			message: fmt.Sprintf("%s（物品 %d（%s）稀有度 %d / 红字类型 %d）：%s。",
				itemcfgErrAmplifyValueUnresolvable, entry.ID, entry.Name, entry.Rarity, amplifyType,
				valueErr.Error()),
		}
	}
	return amplifyType, int(value), nil
}

// validateGMMailGrantOptions 是 UP gm.go:1805-1821 的四条门禁，逐条按同一顺序，
// 末尾追加锻造（J13-F）与红字数值（J13-G）那两条。
// 返回 nil 表示放行。所有拒绝都发生在任何写库之前（零写入）。
func (s *Server) validateGMMailGrantOptions(entry itemEntry, count int, options gmMailGrantOptions) *gmGrantOptionRejection {
	// UP:1805 装备只能一件一封。
	if entry.Kind == grantItemKindEquipment && count != 1 {
		return &gmGrantOptionRejection{
			reason: "equipment_count_not_one",
			message: fmt.Sprintf(
				"物品 %d（%s）是装备，一封邮件只能发 1 件（收到数量 %d）。装备的强化/增幅/品级都是单件实例属性，多件无法表达。",
				entry.ID, entry.Name, count),
		}
	}
	// UP:1809-1811 三个整数边界。
	if options.ExpireDays < 0 || options.ExpireDays > gmMailExpireDaysMax ||
		options.Reinforce < 0 || options.Reinforce > gmMailReinforceMax ||
		options.AmplifyType < 0 || options.AmplifyType > gmMailAmplifyTypeMax {
		return &gmGrantOptionRejection{
			reason: "out_of_range",
			message: fmt.Sprintf(
				"发放参数越界：expire_days 必须 0..%d、reinforce 0..%d、amplify_type 0..%d（收到 %d / %d / %d）。",
				gmMailExpireDaysMax, gmMailReinforceMax, gmMailAmplifyTypeMax,
				options.ExpireDays, options.Reinforce, options.AmplifyType),
		}
	}
	// UP:1812-1814 品级词表。
	quality, ok := normalizeGMMailEquipmentQuality(options.EquipmentQuality)
	if !ok {
		return &gmGrantOptionRejection{
			reason: "invalid_quality",
			message: fmt.Sprintf(
				"equipment_quality 只接受 %s / %s / %s（收到 %q）。",
				gmQualityDefault, gmQualityRandom, gmQualityTop, options.EquipmentQuality),
		}
	}
	// 锻造（J13-F）单独一条门禁，且**排在**下面那条通用"装备专属 option"门禁之前：
	// 操作者要的是锻造，报"这个类型不支持强化/增幅/品级"是错的（上衣/戒指这类装备
	// 确实支持那三样，只是不能锻造）。三条具名原因见 gmResolveForgingGrantLevel。
	if _, rejection := s.gmResolveForgingGrantLevel(entry, options.ForgingLevel); rejection != nil {
		return rejection
	}
	// UP:1815-1817 装备专属 option 只对装备有意义；UP:1818-1821 还要装备类型支持。
	wantsEquipmentAttributes := options.Reinforce != 0 || options.AmplifyType != 0 ||
		(quality != "" && quality != gmQualityDefault)
	if !wantsEquipmentAttributes {
		return nil
	}
	if entry.Kind != grantItemKindEquipment {
		return &gmGrantOptionRejection{
			reason: "attributes_require_equipment",
			message: fmt.Sprintf(
				"物品 %d（%s）是%s，不是装备：强化 / 增幅 / 品级只能发给装备，堆叠物只能设 expire_days。",
				entry.ID, entry.Name, grantEntryKindLabel(entry.Kind)),
		}
	}
	if !gmEntrySupportsEquipmentAttributes(entry.Tag) {
		return &gmGrantOptionRejection{
			reason: "attributes_unsupported_type",
			message: fmt.Sprintf(
				"物品 %d（%s）的类型 %q 不支持强化 / 增幅 / 品级（只有武器/上衣/头肩/下装/鞋/腰带/项链/手镯/戒指/辅助装备/魔法石/副武器支持）。",
				entry.ID, entry.Name, entry.Tag),
		}
	}
	// 红字（J13-G）：类型与数值一起判定。排在最后是刻意的 —— 堆叠物 / 不支持的装备类型
	// 带 amplify_type 时仍旧报上面那两句（attributes_require_equipment /
	// attributes_unsupported_type），既有契约与既有测试逐字不变。
	if _, _, rejection := s.gmResolveAmplifyGrant(entry, options.AmplifyType); rejection != nil {
		return rejection
	}
	return nil
}

func grantEntryKindLabel(kind string) string {
	switch kind {
	case grantItemKindEquipment:
		return "装备"
	case "stackable":
		return "堆叠物"
	default:
		return kind
	}
}

// ---------------------------------------------------------------------------
// 投影
// ---------------------------------------------------------------------------

// gmGrantOptionProjection 是一次发放里 option 真正落地的结果。
type gmGrantOptionProjection struct {
	// extra 是最终要写进邮件附件的 Extra（没有任何 option 生效时就是调用方原来那张 map）。
	extra map[string]string
	// expireAt 非零时写进附件的实例到期时间（mail/body.go:409-411 会把它写进附件行 0x38）。
	expireAt time.Time
	// applied 列出真正生效的 option 名（审计日志 + 测试断言用）。
	applied []string
	// qualitySeed 非零时是写进 quality_seed 的品级种子。
	qualitySeed uint32
	// amplifyValue 非零时是随 red字类型一起写进 amplify_value/marker_16 的红字数值
	// （J13-G；来源 = 既有的 itemcfgAmplifyTable.initialValue）。
	amplifyValue int
	// forgingLevel 非零时是写进 byte_57/value_57 的锻造等级。
	forgingLevel int
}

// cloneGrantExtra 复制一张 Extra（只复制，不发明任何键）。
func cloneGrantExtra(extra map[string]string) map[string]string {
	cloned := make(map[string]string, len(extra)+4)
	for key, value := range extra {
		cloned[key] = value
	}
	return cloned
}

// applyGMMailGrantOptions 把 5 个 option 投影到附件 Extra / 实例到期时间上。
//
// 入参 extra 是 grantAttachmentExtra（api_items_durability.go:49-71）已经写好的证据
// （mailbox_pvf_kind / mailbox_equipment_type / mailbox_pvf_path / durability /
// max_durability）。**一个 option 都没生效时原样返回这张 map**（不 clone、不补键），
// 于是 off 档与"on 档但控件全默认"发出去的邮件逐字节相同。
//
// 有意未照抄 UP:1851-1864 与 :1890-1891（装备分支的身份证据 / list type / durability /
// 0x77 行），理由见文件头与交付报告 §2：本仓这些证据由**领取端**的唯一实现补齐
// （mailbox_pvf.go:158-164 的 mergeMailboxItemIdentityExtra 与
// mergeCurrentMailAttachmentDurabilityExtra、:55-74 的品级种子补齐），
// gmtool 侧今天也只写 mailbox_* 与 durability 两项；在发放端再造一套会把同一份
// 语义写成两份来源。
//
// 挂在 *Server 上是因为锻造那一项要读 PVF（上限来自 etc/upgrade_separate.etc）；
// 它是**第二道闸**：校验阶段已经跑过同一条规则，这里再跑一次，投影绝不会写出一个
// 没校验过的锻造值。
func (s *Server) applyGMMailGrantOptions(
	entry itemEntry,
	extra map[string]string,
	options gmMailGrantOptions,
	now time.Time,
) (gmGrantOptionProjection, error) {
	quality, _ := normalizeGMMailEquipmentQuality(options.EquipmentQuality)
	wantsExpire := options.ExpireDays > 0
	wantsForging := options.ForgingLevel > 0
	// 装备类型支持表是第二道闸：validateGMMailGrantOptions 已经拦住"不支持的装备类型
	// + 装备专属 option"，这里再按 UP:1851 的 `definition.Kind == equipment` 分支收口。
	wantsAttributes := entry.Kind == grantItemKindEquipment &&
		gmEntrySupportsEquipmentAttributes(entry.Tag) &&
		(options.Reinforce > 0 || options.AmplifyType > 0 || (quality != "" && quality != gmQualityDefault))
	if !wantsExpire && !wantsAttributes && !wantsForging {
		return gmGrantOptionProjection{extra: extra}, nil
	}

	projection := gmGrantOptionProjection{extra: cloneGrantExtra(extra), applied: make([]string, 0, 5)}
	stack := dnfrepo.ItemStack{ItemID: int64(entry.ID), Count: 1, Extra: projection.extra}

	if wantsExpire {
		// UP:1844-1849：expirationDate = now + expire_days*24h，然后交给
		// applyCurrentGMExpirationOverrideAt。天数换算是纯 time.Duration 运算。
		expirationDate := now.Add(time.Duration(options.ExpireDays) * 24 * time.Hour)
		stack, _ = applyCurrentGMExpirationOverrideAt(stack, expirationDate, options.ExpireDays, now)
		projection.expireAt = stack.ExpireAt
		projection.applied = append(projection.applied, "expire_days")
	}

	if wantsAttributes {
		// UP:1865-1869（reinforce -> reinforce + ext_data0）。
		// 本仓读取端：current_item_list_stack_projection.go:41 把 ext_data0 写进行 0x0A；
		// inventory/owner.go:2585/2616 也按 reinforce/ext_data0 记账。
		if options.Reinforce > 0 {
			value := strconv.Itoa(options.Reinforce)
			stack.Extra["reinforce"] = value
			stack.Extra["ext_data0"] = value
			projection.applied = append(projection.applied, "reinforce")
		}
		// UP:1870-1874（amplify_type -> amplify_type + byte_13）+ vendor ItemGrantOptions.cs:150-163
		// （AmplifyType > 0 时连同 AmplifyValue 一起写）。
		// 本仓读取端：current_item_list_stack_projection.go:67-68 把 byte_13 / marker_16
		// 写进行 0x13 / 0x14..0x15；inventory/owner_amplify.go:297-301 也按这两组键记账。
		// 写点复用在线配置接口同一个移植自游戏自身的写入器
		// （api_items_configure.go:1298 itemcfgSetAmplifyState = owner_amplify.go:288-309），
		// 类型与数值因此永远成对出现，不会出现"有红字、数值 0"的半件装备。
		if options.AmplifyType > 0 {
			amplifyType, amplifyValue, rejection := s.gmResolveAmplifyGrant(entry, options.AmplifyType)
			if rejection != nil {
				// 具名拒绝原样返回（投影是第二道闸，reason 与校验阶段同名）。
				return gmGrantOptionProjection{}, rejection
			}
			itemcfgSetAmplifyState(&stack, byte(amplifyType), uint16(amplifyValue))
			projection.amplifyValue = amplifyValue
			projection.applied = append(projection.applied, "amplify_type")
		}
		// UP:1875-1889：先定种子，再交给唯一的品级投影实现 itemquality.Apply
		// （= dnfbridge/equipment_quality.go:61-63 applyCurrentEquipmentQualitySeed）。
		switch quality {
		case gmQualityRandom:
			seed, err := itemquality.NewRandomSeed()
			if err != nil {
				return gmGrantOptionProjection{}, fmt.Errorf("生成随机品级种子失败: %w", err)
			}
			projection.qualitySeed = seed
		case gmQualityTop:
			projection.qualitySeed = itemquality.TopSeed
		}
		if projection.qualitySeed != 0 {
			if err := itemquality.Apply(&stack, projection.qualitySeed); err != nil {
				return gmGrantOptionProjection{}, fmt.Errorf("写入品级种子失败: %w", err)
			}
			projection.applied = append(projection.applied, "equipment_quality")
		}
	}

	// 锻造（J13-F）：判定与在线配置接口共用 gmResolveForgingGrantLevel，写点共用
	// itemcfgSetForgingLevel（Extra 的 byte_57/value_57 + 0x77 行的 0x57）。
	// 发放出去的附件没有 0x77 行（RawEntry 为空，helper 于是只写 Extra），
	// 客户端的行由 inventory/body.go:461 从这两个 Extra 键生成 —— 邮件投递不丢。
	if wantsForging {
		level, rejection := s.gmResolveForgingGrantLevel(entry, options.ForgingLevel)
		if rejection != nil {
			// 具名拒绝原样返回（gmGrantOptionRejectionReason 会把 reason 还原）。
			return gmGrantOptionProjection{}, rejection
		}
		itemcfgSetForgingLevel(&stack, byte(level))
		projection.forgingLevel = level
		projection.applied = append(projection.applied, "forging_level")
	}

	projection.extra = stack.Extra
	return projection, nil
}

// gmExpirationSource 是"这个到期时间是 GM 指定的"来源标记，逐字移植 UP
// item_expiration_projection.go:15 的 currentGMExpirationSource（"gm_override"）。
// 存量读取端（背包/仓库/邮件投影）按这个键判定来源，不要改成别的值。
const gmExpirationSource = "gm_override"

// applyCurrentGMExpirationOverrideAt 是 UP item_expiration_projection.go:268-317
// 的**纯日期**移植（计划书 §4.7 测试清单第 9 条）：只做 time.Time 加减与 Extra
// 记账键写入；不读 PVF、不读时钟、不碰仓储、不造任何数据。
//
// 与上游逐段对拍（UP:268-317）：
//
//	:274-276  到期时间零值 / Unix<=0 -> 原样返回、changed=false（本函数唯一提前返回）
//	:281-285  复制 Extra（本仓同样先 clone，绝不改调用方的 map）
//	:286-292  expire_time / expire_unix = 到期 Unix 字符串
//	:293-296  expiration_source = "gm_override"
//	:297-303  expireDays > 0 才写 gm_expire_days（=0 时一个字节都不写）
//	:304-307  stack.ExpireAt = 到期时间
//
// 本仓**有意未照抄**的两段（都不改变"到期时间"这个语义）：
//
//	:308-310  applyCurrentItemListExpireTimeRaw —— 把到期时间烘进 0x77 行。本仓发放路径
//	          从不写 RawEntry（gmtool 没有必然可用的行编码器：Deps.EncodeStackRow 是可选的，
//	          见 gmtool.go:117-122），而邮件附件行的 0x38 到期字段由
//	          mail/body.go:409-411 直接读 attachment.ExpireAt 写入，信息不丢。
//	:311-315  堆叠品的使用期限秒数重算（applyCurrentPVFStackableUsePeriodAt）—— 那一段要
//	          读 PVF 与 now，与"纯日期计算"的要求冲突，本批不移植。
//
// now 只作为签名保留（与上游同形，将来补上面第二段时不必改调用点）；本函数不读它，
// 因此同样的 (expirationDate, expireDays) 必然得到同样的结果。
func applyCurrentGMExpirationOverrideAt(
	stack dnfrepo.ItemStack,
	expirationDate time.Time,
	expireDays int,
	now time.Time,
) (dnfrepo.ItemStack, bool) {
	if expirationDate.IsZero() || expirationDate.Unix() <= 0 {
		return stack, false
	}
	changed := false
	stack.Extra = cloneGrantExtra(stack.Extra)
	expireUnix := strconv.FormatInt(expirationDate.Unix(), 10)
	for _, key := range []string{"expire_time", "expire_unix"} {
		if stack.Extra[key] != expireUnix {
			stack.Extra[key] = expireUnix
			changed = true
		}
	}
	if stack.Extra["expiration_source"] != gmExpirationSource {
		stack.Extra["expiration_source"] = gmExpirationSource
		changed = true
	}
	if expireDays > 0 {
		days := strconv.Itoa(expireDays)
		if stack.Extra["gm_expire_days"] != days {
			stack.Extra["gm_expire_days"] = days
			changed = true
		}
	}
	if !stack.ExpireAt.Equal(expirationDate) {
		stack.ExpireAt = expirationDate
		changed = true
	}
	return stack, changed
}

// ---------------------------------------------------------------------------
// 能力块（路由 33）/ 拒绝出口
// ---------------------------------------------------------------------------

// gmEquipmentQualityOptions 是 on 档报给前端的品级词表，与 UP gm.go:1808-1813 接受的
// 三个 token 一一对应；默认项是 default（不投影品级种子 = 与今天同一条路径）。
func gmEquipmentQualityOptions() []map[string]any {
	return []map[string]any{
		{"value": gmQualityDefault, "label": "PVF 默认品级"},
		{"value": gmQualityRandom, "label": "随机品级"},
		{"value": gmQualityTop, "label": "100% 最上级"},
	}
}

// gmGrantOptionsCapability 把「发放能力块」改成 on 档的真实能力。
//
// 只在开关 on 时由 handleGrantOptions 调用：off 档的能力块仍是
// supportsQuality/canUpgrade/canAmplify/canForge = false、maxForgingLevel = 8
// （逐字节今天的行为 —— 那个 8 是历史常量，**不是**门禁）。
// 前端的能力分叉（wwwroot/js/give.js 的 renderGrantConfiguration）正是读这几个布尔位，
// 所以 off 档前端渲染出来的控件与今天完全一样。
//
// 锻造（J13-F）：前端 give.js:545 只在 `equipment.canForge` 为真时才渲染那个「锻造」
// 输入框，上限取 `equipment.maxForgingLevel` —— 所以这里必须**如实**报：
// 只有武器（itemcfgIsForgeableWeapon）且 PVF etc/upgrade_separate.etc 可读时
// 才 canForge=true + 上限取 [separate upgrade max]；读不到表就 canForge=false +
// maxForgingLevel=0（前端于是不渲染那个框，而不是渲染一个注定被拒的框）。
func (s *Server) gmGrantOptionsCapability(entry itemEntry, payload map[string]any) {
	equipment, ok := payload["equipment"].(map[string]any)
	if !ok {
		return
	}
	supported := entry.Kind == grantItemKindEquipment && gmEntrySupportsEquipmentAttributes(entry.Tag)
	equipment["supportsQuality"] = supported
	equipment["canUpgrade"] = supported
	// 红字（J13-G）：除了"类型支持"，还必须**算得出初值**才报 true —— 否则前端会渲染出
	// 一个注定被 gmResolveAmplifyGrant 拒掉的下拉（"渲染了一个用不了的框"正是本批要修的
	// 那类缺陷）。判据复用在线配置接口同一份实现（api_items_configure.go:971-983 的
	// hasInitialValues 那一半；等级/稀有度门槛只属于配置侧，发放侧沿用 fork
	// gm.go:1948-1955 的类型表，见文件头）。
	canAmplify := false
	if supported {
		if table, err := s.itemcfgAmplifyTable(); err != nil {
			s.logEvent("gm-item-grant-capability-degraded",
				"item_id", entry.ID, "path", itemcfgAmplifyItemPVFPath,
				"reason", "amplify_table_unavailable", "error", err.Error())
		} else {
			canAmplify = table.hasInitialValues(entry.Rarity)
		}
	}
	equipment["canAmplify"] = canAmplify
	equipment["qualityOptions"] = gmEquipmentQualityOptions()
	// 锻造：能力块的三项一律由这里覆盖（off 档不走这里，仍是历史常量）。
	// currentForgingLevel 恒为 0：发放出去的是新实例，客户端那一格本来就是 0。
	// 只有武器才去读 PVF（非武器恒不可锻，省掉每次发放能力探测的一次 PVF 读）。
	canForge := false
	maxForging := 0
	if itemcfgIsForgeableWeapon(entry) {
		if table, err := s.itemcfgSeparateUpgradeTable(); err != nil {
			s.logEvent("gm-item-grant-capability-degraded",
				"item_id", entry.ID, "path", itemcfgSeparateUpgradePVFPath,
				"reason", "forging_table_unavailable", "error", err.Error())
		} else {
			canForge = true
			maxForging = table.maxLevel
		}
	}
	equipment["canForge"] = canForge
	equipment["maxForgingLevel"] = maxForging
	equipment["currentForgingLevel"] = 0
	equipment["forgingPVFPath"] = itemcfgSeparateUpgradePVFPath
	// expire_days 对任意道具都生效（UP:1843-1850 位于装备分支之前），
	// 所以 canOverride 不看 supported。
	if expiration, ok := payload["expiration"].(map[string]any); ok {
		expiration["canOverride"] = true
	}
}

// gmGrantOptionRejectionReason 把投影阶段返回的错误还原成具名 reason：
// 校验阶段已经跑过同一条规则，投影是第二道闸，两层必须报同一个名字。
// 其它错误（品级种子写入失败之类）沿用既有的 projection_failed。
func gmGrantOptionRejectionReason(err error) string {
	var rejection *gmGrantOptionRejection
	if errors.As(err, &rejection) {
		return rejection.reason
	}
	return "projection_failed"
}

// rejectGMMailGrantOptions 是 option 被拒时的唯一出口：**明确失败 + 点名原因 +
// 零写入**（拒绝都发生在 AppendSystemMail 之前）。reason 进具名日志供自动化断言，
// 面板拿到的是中文原文（本仓契约：业务失败恒 HTTP 200 + success:false）。
func (s *Server) rejectGMMailGrantOptions(
	w http.ResponseWriter,
	panelID int,
	templateID int,
	reason string,
	message string,
) {
	s.logEvent("gm-item-grant-options-rejected",
		"panel_id", panelID, "item_id", templateID, "reason", reason, "result", "rejected")
	writeFailure(w, message+"本次未做任何写入。")
}
