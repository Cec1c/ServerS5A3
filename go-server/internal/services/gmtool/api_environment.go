package gmtool

// 本文件实现 GM 面板的「环境信息」只读路由：
//
//	GET /api/environment
//
// ---------------------------------------------------------------------------
// 契约与来源
// ---------------------------------------------------------------------------
// 面板里唯一提到 `/api/environment` 的是 `wwwroot/js/environment.js:217`，它发的是
// `post('/api/environment', { databasePath, pvfPath })` —— 那是 A21/86jp 的
// 「数据源」面板（SQLite inventory.db + PVF 路径热切换 + S4A12→S4A21 迁移预览）。
// 本仓**没有**那套语义：数据源在启动时由 `cmd/server/control` 生成的配置固定
// （库名来自 servergroup 路由 meta，PVF 来自 DNFBRIDGE_PVF_PATH），运行时既不能换
// 数据库也不能换 PVF；`/api/status` 也一直回报 `canChangeSource:false`
// （api_auth.go:40），而 `environment.js:83` 的 `showRuntimeSourcePanel` 在这个
// 前提下直接返回 —— 也就是说那个 POST 在 90CN 面板里**根本不可达**。
// 因此本路由按本仓能如实观测到的东西回答（GET，诊断信息），不去编造
// migrationRequired / preview / status 之类的 A21 迁移字段；POST /api/environment
// 继续落 `/api/` 兜底「该功能未移植」，行为与今天逐字节相同。
//
// 只报告**能观测**的值，且**绝不回显机密**：
//
//   - 版本/构建：gmtool.go 的 ToolVersion / BaselineID + runtime + debug.ReadBuildInfo；
//   - PVF：配置路径 + 启动期加载快照里的 sha256/大小/文件数（platform/pvf 的
//     ArchiveSnapshot 在启动读盘时就算好了，本路由**不会为了取哈希再读一次盘**）；
//   - 数据库：只报驱动/库名/主机与面板账号表名。**不回显 DSN、用户名与口令**
//     （DSN 解析失败也只给一句"无法解析"，不把原文塞回响应）；
//   - 开关：本包全部 DNFBRIDGE_GM_* 开关的环境原文与生效值（条目数随开关增加而变，
//     所以这里不写死数字），其中几个还回报宿主启动时注入的 Config 选项
//     （能一眼看出"环境改了但进程没重启"）。

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	// envReportVersion 是本报告自身的格式版本（面板/脚本可据此判断字段集合）。
	envReportVersion = 1
	// envProbeTimeout 限制数据库连通性探测，避免库不可达时把请求挂住。
	envProbeTimeout = 2 * time.Second
)

// envPVFSnapshotter 是"能给出启动期加载快照"的 PVF 来源。
// `platformpvf.Archive`（生产档：dnfbridge 的 initialEquipmentArchiveLocked）
// 实现它；测试里的内存来源不实现，于是哈希字段如实为空而不是被编出来。
type envPVFSnapshotter interface {
	Snapshot() platformpvf.ArchiveSnapshot
}

// envSwitch 是一个功能开关的报告项。
type envSwitch struct {
	Name string `json:"name"`
	// Value 是环境变量的原文。这些开关的取值只能是 on/off/true/1/yes 之类的字面量，
	// 不含任何机密（本包所有开关的解析函数见各自文件）。
	Value            string `json:"value"`
	Enabled          bool   `json:"enabled"`
	DefaultWhenUnset string `json:"defaultWhenUnset"`
	Effect           string `json:"effect"`
	// WiredOption 只在"宿主启动时把开关读进 Config、handler 读 Config"的开关上出现，
	// 用来暴露"环境变量改了但进程没重启"这种不一致。
	WiredOption *bool `json:"wiredOption,omitempty"`
}

// envHandleInfo 实现 GET /api/environment。
//
// 恒 HTTP 200 + success:true（本仓契约：只有登录门禁是 401）；取不到的值一律
// 如实报 null / false / error 文本，**不用默认值假装成功**。
func (s *Server) envHandleInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	writeOK(w, map[string]any{
		"reportVersion": envReportVersion,
		"generatedAt":   time.Now().Format(time.RFC3339),
		"panel":         s.envPanelReport(),
		"go":            envGoReport(),
		"build":         envBuildReport(),
		"pvf":           s.envPVFReport(),
		"database":      s.envDatabaseReport(ctx),
		"repositories":  s.envRepositoryReport(),
		"itemIndex":     s.envItemIndexReport(),
		"switches":      s.envSwitchReport(),
		"backup":        envBackupReport(),
		"limitations":   envLimitations(),
	})
}

func (s *Server) envPanelReport() map[string]any {
	return map[string]any{
		"toolVersion":       ToolVersion,
		"baselineId":        BaselineID,
		"listen":            s.cfg.Listen,
		"accountsTable":     s.cfg.Table,
		"wwwRootOverride":   strings.TrimSpace(s.cfg.WWWRoot) != "",
		"pvfPathConfigured": strings.TrimSpace(s.cfg.PVFPath) != "",
		"sessionTTLSeconds": int(sessionTTL / time.Second),
	}
}

func envGoReport() map[string]any {
	return map[string]any{
		"version":    runtime.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"cpus":       runtime.NumCPU(),
		"gomaxprocs": runtime.GOMAXPROCS(0),
	}
}

// envBuildReport 报告构建信息（debug.ReadBuildInfo）。
//
// 只回显**白名单**里的编译设置：`-ldflags` 之类的设置里可能被塞进令牌或密钥，
// 一律不回显（本仓 DNF90Server.exe 用 -buildvcs=false 构建，vcs.* 通常缺失，
// 因此这些字段为空是正常的，不是错误）。
func envBuildReport() map[string]any {
	out := map[string]any{
		"available":      false,
		"modulePath":     "",
		"moduleVersion":  "",
		"moduleSum":      "",
		"vcsRevision":    "",
		"vcsTime":        "",
		"vcsModified":    nil,
		"buildSettings":  map[string]string{},
		"settingCount":   0,
		"settingsHidden": 0,
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return out
	}
	out["available"] = true
	out["modulePath"] = info.Main.Path
	out["moduleVersion"] = info.Main.Version
	out["moduleSum"] = info.Main.Sum
	settings := map[string]string{}
	hidden := 0
	allowed := map[string]struct{}{
		"GOOS": {}, "GOARCH": {}, "GOAMD64": {}, "GOARM": {}, "GO386": {},
		"CGO_ENABLED": {}, "-buildmode": {}, "-compiler": {}, "-trimpath": {},
		"-race": {}, "-tags": {}, "-asmflags": {}, "-gcflags": {},
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			out["vcsRevision"] = setting.Value
		case "vcs.time":
			out["vcsTime"] = setting.Value
		case "vcs.modified":
			modified := strings.EqualFold(setting.Value, "true")
			out["vcsModified"] = modified
		default:
			if _, ok := allowed[setting.Key]; ok {
				settings[setting.Key] = setting.Value
				continue
			}
			hidden++
		}
	}
	out["buildSettings"] = settings
	out["settingCount"] = len(info.Settings)
	out["settingsHidden"] = hidden
	return out
}

// envPVFReport 报告 PVF 路径与（廉价的）哈希。
//
// 哈希来自启动期加载时算好的快照（platform/pvf/pvf.go:72 的 sha256），
// 本路由不会为了它去读一次 500 MB 的盘。
func (s *Server) envPVFReport() map[string]any {
	out := map[string]any{
		"configuredPath": s.cfg.PVFPath,
		"loaded":         false,
		"error":          nil,
		"loadedPath":     nil,
		"sizeBytes":      nil,
		"checksumSha256": nil,
		"fileCount":      nil,
		"format":         nil,
		"loadedAt":       nil,
		"checksumNote":   "当前 PVF 来源不提供启动期快照，未计算哈希（本路由不会为了哈希读整份 PVF）",
	}
	if s.deps.PVF == nil {
		out["error"] = "PVF 提供器未注入（Deps.PVF 为 nil）"
		return out
	}
	source, err := s.source()
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["loaded"] = true
	if snapshotter, ok := source.(envPVFSnapshotter); ok {
		snapshot := snapshotter.Snapshot()
		out["loadedPath"] = snapshot.Path
		out["sizeBytes"] = snapshot.Size
		out["checksumSha256"] = snapshot.Checksum
		out["fileCount"] = snapshot.FileCount
		out["format"] = string(snapshot.Format)
		out["loadedAt"] = snapshot.LoadedAt
		out["checksumNote"] = "sha256 来自启动期加载的 PVF 快照，本路由不重新读盘"
		if snapshot.Path == "" {
			out["loadedPath"] = s.cfg.PVFPath
		}
	}
	return out
}

// envDatabaseReport 报告数据库标识。
//
// **不回显 DSN、用户名与口令**：只给驱动、库名、主机:端口与面板账号表名。
// DSN 解析失败时也只回一句固定文案，绝不把连接串原文带进响应。
func (s *Server) envDatabaseReport(ctx context.Context) map[string]any {
	out := map[string]any{
		"driver":     "mysql",
		"label":      s.cfg.DatabaseLabel,
		"name":       nil,
		"host":       nil,
		"network":    nil,
		"configured": strings.TrimSpace(s.cfg.DSN) != "",
		"reachable":  nil,
		"error":      nil,
		"note":       "只报告库名/主机；DSN、用户名与口令一律不回显",
	}
	if strings.TrimSpace(s.cfg.DSN) == "" {
		out["error"] = "未配置数据库 DSN"
		return out
	}
	parsed, err := mysql.ParseDSN(s.cfg.DSN)
	if err != nil {
		// 解析失败就到此为止：不覆盖成别的文案，也不回显原文。
		out["error"] = "DSN 无法解析（为避免泄露连接串，不显示原文）"
		return out
	}
	out["name"] = parsed.DBName
	out["host"] = parsed.Addr
	out["network"] = parsed.Net
	if s.db == nil {
		out["error"] = backupDatabaseMissingMessage
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, envProbeTimeout)
	defer cancel()
	if err := s.db.PingContext(probeCtx); err != nil {
		out["reachable"] = false
		out["error"] = err.Error()
		return out
	}
	out["reachable"] = true
	return out
}

// envRepositoryReport 报告 DNF 仓储装配状态（只读探测，不写库）。
func (s *Server) envRepositoryReport() map[string]any {
	out := map[string]any{
		"ready":   false,
		"error":   nil,
		"missing": []string{},
	}
	if s.deps.Repos == nil {
		out["error"] = "仓储提供器未注入（Deps.Repos 为 nil）"
		return out
	}
	group, ok := s.deps.Repos()
	if !ok {
		out["error"] = errRepositoriesUnavailable.Error()
		return out
	}
	missing := make([]string, 0, 4)
	for _, entry := range []struct {
		name  string
		value any
	}{
		{"Account", group.Account},
		{"AccountInventory", group.AccountInventory},
		{"Character", group.Character},
		{"Inventory", group.Inventory},
		{"Equipment", group.Equipment},
		{"Pet", group.Pet},
		{"Quest", group.Quest},
		{"Skill", group.Skill},
		{"DungeonPermission", group.DungeonPermission},
		{"Mailbox", group.Mailbox},
		{"LegacyUserInfo", group.LegacyUserInfo},
		{"LegacyInventory", group.LegacyInventory},
	} {
		if entry.value == nil {
			missing = append(missing, entry.name)
		}
	}
	out["missing"] = missing
	if _, err := s.repositories(); err != nil {
		out["error"] = err.Error()
		return out
	}
	out["ready"] = true
	return out
}

// envItemIndexReport 报告 PVF 物品索引的构建状态（api_auth.go:15-19 用的同一对探针）。
func (s *Server) envItemIndexReport() map[string]any {
	out := map[string]any{"ready": false, "error": nil, "cachePath": s.cfg.ItemIndexCachePath}
	if s.items == nil {
		out["error"] = "物品索引未装配"
		return out
	}
	out["ready"] = s.items.isReady()
	if err := s.items.failure(); err != nil {
		out["error"] = err.Error()
	}
	return out
}

// envSwitchReport 报告本包全部 DNFBRIDGE_GM_* 功能开关。
//
// enabled 一律调用**该开关自己的解析函数**（不在这里重写一套判断），
// 这样报告与真实行为不可能分叉。
func (s *Server) envSwitchReport() []envSwitch {
	requireOffline := RequireOfflineFromEnvironment()
	removeInventory := RequireItemRemoveFromEnvironment()
	equipSlotQuestSync := EquipSlotQuestSyncFromEnv(os.Getenv(equipSlotQuestSyncEnv))
	return []envSwitch{
		{
			Name: requireOfflineEnv, Value: os.Getenv(requireOfflineEnv), Enabled: requireOffline,
			DefaultWhenUnset: "off", WiredOption: &s.cfg.RequireOffline,
			Effect: "为 on 时，P2 起新增的写路由会拒绝仍在对局会话里的角色（online_guard.go）",
		},
		{
			Name: itemRemoveEnv, Value: os.Getenv(itemRemoveEnv), Enabled: removeInventory,
			DefaultWhenUnset: "off", WiredOption: &s.cfg.RemoveInventory,
			Effect: "为 on 时才注册「直接写背包」的两条路由（items/delete、items/clear）；默认档它们根本不存在",
		},
		{
			Name: equipSlotQuestSyncEnv, Value: os.Getenv(equipSlotQuestSyncEnv), Enabled: equipSlotQuestSync,
			DefaultWhenUnset: "off", WiredOption: &s.cfg.EquipSlotQuestSync,
			Effect: "legacy 档下为 on 时「开启特殊装备槽」额外把授权任务标 completed（equipment_slots.go）；" +
				"默认档（" + equipSlotQuestChainEnv + " 未设）下任务写入由下面的链路开关决定，本开关不再起作用",
		},
		{
			Name: equipSlotQuestChainEnv, Value: os.Getenv(equipSlotQuestChainEnv),
			Enabled:          EquipSlotQuestChainFromEnvironment(),
			DefaultWhenUnset: "on",
			Effect: "空 = on：开启特殊装备槽时把 PVF 里授权该槽位的任务**连同它的 [pre required quest] 前置链**" +
				"一起标 completed（耳环槽 = 2636 + 2635「穿戴耳环 (1/2)」），修掉现场" +
				"「gm工具开启耳环槽后任务依然存在」；只有显式 " + equipSlotQuestChainLegacy +
				"（或 off/0/false/no/disable/disabled/none）才逐字节回到改动前的" +
				"「只写位 + 只写授权任务本身」",
		},
		{
			Name: gmMailOptionsEnv, Value: os.Getenv(gmMailOptionsEnv), Enabled: GMMailOptionsFromEnvironment(),
			DefaultWhenUnset: "off",
			Effect:           "为 on 时发放路由才解析 options（expire_days/reinforce/amplify_type/equipment_quality/forging_level）",
		},
		{
			Name: gmMailGrantDialogEnv, Value: os.Getenv(gmMailGrantDialogEnv),
			Enabled:          GMMailGrantDialogFromEnvironment(),
			DefaultWhenUnset: "on",
			Effect: "发放列表每一行的按钮先弹**发放对话框**（模式 " + gmMailGrantDialogModeDialog + "）；空 = on。" +
				"为 off/direct 等显式字面量时 give.js 与加这条开关之前逐字节相同（" +
				gmMailGrantDialogModeDirect + " 档：行内按钮直接发那封邮件，没有对话框）。" +
				"对话框里 强化/红字/锻造 三个框由 GET …/items/{templateId}/grant-options 的能力位决定，" +
				"而能力位只在 " + gmMailOptionsEnv + "=on（空 = off）时才如实上报；" +
				"发送仍走既有的 POST /api/characters/{id}/items（api_items_grant_dialog.go）",
		},
		{
			Name: gmAccountSearchEnv, Value: os.Getenv(gmAccountSearchEnv), Enabled: GMAccountSearchFromEnvironment(),
			DefaultWhenUnset: "off",
			Effect:           "为 on 时 GET /api/accounts 才接受 q/limit 跨账号检索",
		},
		{
			Name: questMutateEnv, Value: os.Getenv(questMutateEnv), Enabled: s.questMutateEnabled(),
			DefaultWhenUnset: "on",
			Effect:           "任务写面总开关；空 = on。为 off 时 6 条任务写路由恒失败且零写入",
		},
		{
			Name: questProfessionWriteEnv, Value: os.Getenv(questProfessionWriteEnv),
			Enabled:          QuestProfessionWriteFromEnv(os.Getenv(questProfessionWriteEnv)),
			DefaultWhenUnset: "off",
			Effect:           "为 on 时才允许写职业任务",
		},
		{
			Name: renameModeEnv, Value: os.Getenv(renameModeEnv), Enabled: s.characterRenameEnabled(),
			DefaultWhenUnset: "on",
			Effect: "角色改名路由 POST /api/characters/{id}/rename 的开关；空 = on。" +
				"为 off/legacy 等显式字面量时该路由**根本不注册**，请求落 /api/ 兜底（api_character_rename.go）",
		},
		{
			Name: questModeEnv, Value: os.Getenv(questModeEnv), Enabled: s.questModeEnabled(),
			DefaultWhenUnset: "on",
			Effect: "任务完成页「批量完成已接任务」路由 POST /api/characters/{id}/quests/active/complete-batch 的开关；空 = on。" +
				"为 off/legacy 等显式字面量时该路由**根本不注册**（落 /api/ 兜底），" +
				"且 GET /api/characters/{id}/quests 不再回 completionBatch 字段（前端不渲染逐条/批量完成控件）" +
				"—— 面板与加这条路由之前逐字节相同（api_quest_active_batch.go）",
		},
		{
			Name: questChainResetEnv, Value: os.Getenv(questChainResetEnv),
			Enabled:          QuestChainResetFromEnv(os.Getenv(questChainResetEnv)),
			DefaultWhenUnset: "off",
			Effect:           "为 on 时 uncomplete 撞到 main quest > 0 的任务才允许整族链路重置",
		},
		{
			// 只读广告（本包唯一的 dnfbridge 条目）：这个开关的**行为真源**不在本包 ——
			// 它在 internal/services/dnfbridge/game_enter_select_dungeon.go 的
			// currentTownTransportEnterSelectWindow。gmtool 不能 import dnfbridge（依赖方向
			// 相反），所以这里按 online_guard.go / api_items_grant_dialog.go 的既有惯例
			// 自读环境，**只做展示**，不改任何行为。
			Name: townTransportEnterSelectWindowEnv, Value: os.Getenv(townTransportEnterSelectWindowEnv),
			Enabled:          townTransportEnterSelectWindowBounded(),
			DefaultWhenUnset: "500",
			Effect: "城镇 transport（世界地图/传送站、传送阵）之后那条场景收尾 op15 的抑制窗口（毫秒）：" +
				"默认 500（实测提交->op15 最小 2054.7 ms，余量 4 倍以上），窗口外到达的 op15 照常应答（op15 成功体 + 疲劳 + op27 选择页上下文），" +
				"修复「副本门前撞在地图边上不进去、回头再进才弹选图界面」；" +
				"legacy/off/false/no = 逐字节回到无时限恒吞（enabled=false）",
		},
	}
}

// townTransportEnterSelectWindowEnv 是 dnfbridge 那条窗口开关的环境名（只读广告，
// 见 envSwitchReport 里对应条目的注释）。
const townTransportEnterSelectWindowEnv = "DNFBRIDGE_TOWN_TRANSPORT_ENTER_SELECT_WINDOW_MS"

// townTransportEnterSelectWindowBounded 回答"窗口档是否生效"（= 不是回退档）。
// 口径与 dnfbridge 的 currentTownTransportEnterSelectWindow 的档位分支逐条一致；
// 空/非数字/非正数与正整数都是修复档（默认 500 ms 或该毫秒数）。
func townTransportEnterSelectWindowBounded() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(townTransportEnterSelectWindowEnv))) {
	case "legacy", "off", "false", "no":
		return false
	}
	return true
}

// envBackupReport 报告本面板账号备份/恢复的格式与范围（本文件的事实声明，不是猜测）。
func envBackupReport() map[string]any {
	specs := backupCoreTableSpecs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.name)
	}
	return map[string]any{
		"formatVersion":         backupFormatVersion,
		"restoreAcceptsVersion": []int{backupFormatVersion},
		"store":                 "JSON 文件（面板导出下载 / 恢复时上传），服务端不留存",
		"restoreBodyLimitBytes": backupRestoreBodyLimit,
		"legacyTablePrefix":     backupLegacyTablePrefix,
		"coreTables":            names,
		"maxInChunk":            backupInChunkSize,
	}
}

// envLimitations 是这份报告的"边界声明"：面板能用它解释"为什么某个值不在"。
func envLimitations() []string {
	return []string{
		"数据源在启动时固定（库名来自 servergroup 路由 meta、PVF 来自 DNFBRIDGE_PVF_PATH），运行时不能像 A21 那样切换数据库/PVF。",
		"POST /api/environment（A21 的数据源面板）在本仓不可达：/api/status 恒回报 canChangeSource=false，该请求继续落 /api/ 兜底。",
		"不回显 DSN、数据库用户名与口令；DSN 解析失败也不显示原文。",
		"PVF 哈希来自启动期加载快照；不为此重新读盘，来源不支持快照时该字段为空。",
		"构建信息里的编译设置只回显白名单项，避免把 -ldflags 里的机密带出去。",
	}
}
