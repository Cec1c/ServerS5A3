package gmtool

// J13-G：邮件发放**对话框**（面板「发放物品」列表每一行的按钮先弹对话框，
// 由操作者填强化 / 红字 / 锻造，再按对话框里的按钮真正发邮件）。
//
// # 现场报障（操作者原话 + 截图）
//
// 「物品列表每一行的『邮件发放』按钮，点下去直接就把邮件发了；应该像 A21 GM 工具
// 那样先弹一个对话框，里面有能填的 强化 / 红字 / 锻造 三个框。」
//
// 根因（改动前的前端，wwwroot/js/give.js）：
//
//	searchItems     非"需要配置"的行走 :713 的 `data-give-action="grant"` 分支 ——
//	                行内一个数量框 + 一个按钮，onclick :725 直接 `giveItem(...)`，
//	                **不问任何参数就发**；只有装备/称号这类 `needsGrantConfiguration`
//	                的行才走 :712 的 `配置` 按钮 → 浮层卡片（对话框）。
//	所以同一张列表里，"能填参数"和"点了就发"是两套行为，操作者点的那一行恰好是后者。
//
// 上游 A21 GM 工具（`H:\86JP启动器\Temp\gm-upstream-v260910\wwwroot\js\give.js:516-598`）
// 的"发放对话框"就是这张浮层卡片（`#give-config-card` + floating-config.js，
// 本仓逐字沿用同一套 DOM/CSS），字段集与语义见：
//
//	Services/GmService.Inventory.cs        发放/配置接口（options 对象）
//	ServerCore/Game/Inventory/ItemGrantOptions.cs
//	  :10-25   ItemGrantOptions{QualityMode, UpgradeLevel, AmplifyType, ForgingLevel, …}
//	  :44-45   MaximumUpgradeLevel = 31、MaximumForgingLevel = 8
//	  :111-142 边界：UpgradeLevel 0..31、AmplifyType 0..4（:167-177 GetAmplifyTypeLabel
//	           1 体力 / 2 精神 / 3 力量 / 4 智力 / 否则"无"）、ForgingLevel 0..8
//	  :150-163 写入：Upgrade / Forging / AmplifyType + （AmplifyType>0 时）
//	           AmplifyValue = resolveInitialAmplifyValue(rarity)，算不出来就明确失败
//
// # 本开关：只决定"点行内按钮先弹对话框还是直接发"
//
//	`DNFBRIDGE_GM_MAIL_GRANT_DIALOG`
//	  未设置 / 空 / 任何未知值（含笔误）= **对话框档（默认，操作者要的修法）**
//	  `off` / `0` / `false` / `no` / `disable` / `disabled` / `none` / `legacy` / `direct`
//	                                 = **回退档**：give.js 与加这条开关之前**逐字节相同**
//	                                   （行内按钮直接发那一封邮件，没有对话框）
//
// "默认启用 + 显式 off 字面量回退"与 api_character_rename.go:181-188 的
// CharacterRenameFromEnv、api_quest_mutations.go:110 的 QuestMutateFromEnv 同一条约定：
// 新行为默认可用，回退必须写成显式字面量（`direct` 是给这条开关的中文语义补的别名）。
//
// # 为什么开关只在服务端读、行为却在前端
//
// 前端是**内嵌静态资源**（gmtool.go:34-35 的 `//go:embed wwwroot`，index.html + 裸 JS，
// 无构建步骤），它无法自己读环境变量。本仓既有的"服务端广告 + 前端读广告"手法有两条：
//
//	quests.js:15-24   读 GET …/quests 响应里的 completionBatch 字段（每条数据自带广告）
//	environment.js    读 GET /api/status（数据源面板）
//
// 本开关取第三条路的**服务端真源**：`GET /api/environment` 的 `switches[]`
// （api_environment.go:326-388，每个开关都在那里如实报 name/value/enabled/
// defaultWhenUnset/effect）。give.js 用 `giveDeliveryDialogMode()` 懒取一次并缓存，
// **取不到就按默认档（对话框）走** —— 读不到开关绝不等于"悄悄回退成直发"。
//
// # 与"发送"相关的服务端契约一个字都没动
//
// 回退档要求"与今天逐字节相同"，而今天的直发路径打的本来就是同一条
// `POST /api/characters/{id}/items`（模板 + 数量 + requestId + deliveryMode，
// 不带 options），所以：
//
//	· 回退档：请求体与邮件行与原实现逐字节相同（选项解析仍由既有的
//	  DNFBRIDGE_GM_MAIL_OPTIONS 把关，本开关不介入 —— 见 api_items_grant_options.go）；
//	· 对话框档：带上操作者填的 options，校验/投影全部复用 J13 的既有实现
//	  （api_items_grant_options.go 的 validateGMMailGrantOptions / applyGMMailGrantOptions）。
//
// 本文件只有开关解析与模式字面量，**不**另建能力系统：那三个框渲不渲染由既有的
// `GET /api/characters/{id}/items/{templateId}/grant-options` 能力位决定。

import (
	"os"
	"strings"
)

// gmMailGrantDialogEnv 是"发放列表点按钮先弹对话框"的开关名。
// **默认启用**；只有下面 GMMailGrantDialogFromEnv 列出的显式字面量才回退成直发。
const gmMailGrantDialogEnv = "DNFBRIDGE_GM_MAIL_GRANT_DIALOG"

// 模式字面量（面板/脚本/环境报告共用同一对取值，不各自拼字符串）。
const (
	gmMailGrantDialogModeDialog = "dialog"
	gmMailGrantDialogModeDirect = "direct"
)

// GMMailGrantDialogFromEnv 解析 DNFBRIDGE_GM_MAIL_GRANT_DIALOG。
//
// 与 CharacterRenameFromEnv（api_character_rename.go:181-188）同一组字面量：
// **空 = on**（默认档就是操作者要的对话框），只有显式
// off/0/false/no/disable/disabled/none/legacy/direct 才回退成直发；
// 未知值（笔误）取默认档 —— 与"回退必须写成显式字面量"一致。
func GMMailGrantDialogFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no", "disable", "disabled", "none", "legacy", "direct":
		return false
	default:
		return true
	}
}

// GMMailGrantDialogFromEnvironment 读取进程环境里的同一个开关。
//
// 为什么直接 `os.Getenv` 而不经 dnfbridge/options.go：与 online_guard.go:65-73、
// api_quest_mutations.go:121-139 同一取舍（开关自读环境，不进改动最密集的包）。
func GMMailGrantDialogFromEnvironment() bool {
	return GMMailGrantDialogFromEnv(os.Getenv(gmMailGrantDialogEnv))
}

// gmMailGrantDialogMode 是本开关对外的模式字面量（报告/审计/测试共用）。
func gmMailGrantDialogMode(enabled bool) string {
	if enabled {
		return gmMailGrantDialogModeDialog
	}
	return gmMailGrantDialogModeDirect
}
