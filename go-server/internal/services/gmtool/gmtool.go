// Package gmtool 实现 90CN 服务端的 Web GM 面板。
//
// 前端沿用 86jp_DfoGmTool (A21) 的 wwwroot，因此这里的 JSON 契约必须与
// A21 的 Program.cs/GmService 保持一致：成功一律 HTTP 200，业务失败用
// {"success":false,"error":"..."}，唯一的非 200 是登录门禁的 401 +
// {"loginRequired":true}。数据层换成 90CN 自己的 MySQL 仓储。
package gmtool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

//go:embed wwwroot
var embeddedWWW embed.FS

const (
	// CookieName 与 A21 前台保持一致，前端登录面板只依赖 /api/status。
	CookieName = "dfo_gm_session"
	// ToolVersion 显示在状态栏。
	ToolVersion = "90cn-gm-v1"
	// BaselineID 显示在状态栏。
	BaselineID = "90cn-mariadb-v1"

	sessionTTL         = 12 * time.Hour
	maxBodyBytes       = 1 << 20
	readHeaderTimeout  = 10 * time.Second
	readTimeout        = 30 * time.Second
	writeTimeout       = 60 * time.Second
	idleTimeout        = 120 * time.Second
	shutdownGrace      = 5 * time.Second
	loginFailuresPerIP = 8
	loginFailureWindow = 5 * time.Minute

	// unportedFeatureMessage 是 `/api/` 兜底的唯一文案。抽成常量是为了让
	// "关闭档下的新能力"能返回**逐字相同**的兜底文案（P4 的
	// api_items_delete.go 在 cfg.RemoveInventory=false 时就是这么做的），
	// 这样"关闭 = 今天的字节"可以被测试直接断言，而不是靠人眼比对两份字面量。
	unportedFeatureMessage = "该功能未移植到 90CN 面板（当前只提供 账号/角色/属性/发放/钱包）"
)

// Config 是 GM 面板的启动配置。
type Config struct {
	Listen   string
	Password string
	// Table 是启动器注册账号表（A21 里的 accounts）。
	Table string
	// DSN 指向 90CN 使用的 MySQL/MariaDB。
	DSN string
	// WWWRoot 非空时优先从磁盘读取前端文件，便于调试。
	WWWRoot string
	// ItemIndexCachePath 非空时把 PVF 物品索引缓存到该文件。
	ItemIndexCachePath string
	// PVFPath / DatabaseLabel 只用于状态栏展示。
	PVFPath       string
	DatabaseLabel string
	// EquipSlotQuestSync 为真时，「开启特殊装备槽」在写 ex_equip_slot_stat 的位之外，
	// **额外**把运行时 PVF 里授权该槽位的任务标成 completed（任务手册与位一致，
	// 但会跳过那些任务本身的收集流程）。默认 false = 只写位，与 A21 C# 工具、
	// 上游 0.1.8 的 dnfbridge/gm.go、本仓既有的 cmd/scratch/grantlevel90loadout
	// 三个参考实现完全同口径。开关取值见 equipment_slots.go。
	EquipSlotQuestSync bool
	// RequireOffline 为真时，**P2 起新增的写路由**在目标角色仍在对局会话里时拒绝
	// （在线判据见 Deps.Online）。默认 false = 不检查在线 = 逐字节今天的行为；
	// 取值见 online_guard.go 的 DNFBRIDGE_GM_REQUIRE_OFFLINE。**既有路由不受它影响。**
	RequireOffline bool
	// RemoveInventory 为真时才注册 P4 的两条"直接写背包"路由
	// （`POST /api/characters/{id}/items/delete` 与 `.../items/clear`，J6）。
	//
	// **默认 false = 这两条路由根本不注册**：请求继续落到 `/api/` 兜底
	// 「该功能未移植到 90CN 面板…」，与改动前逐字节相同。
	// 取值见 api_items_delete.go 的 DNFBRIDGE_GM_ITEM_REMOVE（**只有显式 `on`
	// 才为真**）。为什么必须默认关：本仓刻意拒绝直接写背包
	// （`api_items.go:327-329` 的 deliveryMode:"inventory" 拒绝即该立场的代码化，
	// 计划书 §5.2 红线 C3），开启等于操作者显式推翻它。
	RemoveInventory bool
	Logger          func(string, ...any)
}

// Deps 是 gmtool 需要宿主注入的运行时依赖。
type Deps struct {
	Activities *onlineevent.Controls
	// Repos 返回 DNF 仓储组；未就绪时返回 false。
	Repos func() (dnfrepo.Group, bool)
	// PVF 返回当前运行时 PVF。
	PVF func() (dnfpvf.Source, error)
	// PremiumTypes exposes only contract types registered in the runtime PVF.
	PremiumTypes func() ([]int64, error)
	// Online 是**可选**的在线会话探针（J0）：返回该角色是否仍在对局会话里。
	// 由 dnfbridge/gmtool_service.go 接到 Service.onlineGameSession 上。
	// 未注入（nil）时在线守卫安全降级为"未知/不在线"并记一条具名日志，
	// 绝不 panic、也不拒绝一切（见 online_guard.go）。
	Online func(characterID uint16) bool
	// CrystalCubeItemIDs 是**可选**的账号晶块商品表（J7）：返回 6 个原生槽位
	// （黑/白/红/蓝/透明/金）的物品 id，顺序即 selection 0..5。
	// 由 dnfbridge/gmtool_service.go 接到本进程唯一的解析结果
	// （Service.currentPremiumCatalog().crystalCubeIDs，premium_pvf.go:126-153）。
	// 未注入或返回 0/负数时，晶块读/写都**明确失败**，绝不填 0（上游 gm_extended.go:407）。
	CrystalCubeItemIDs func() ([6]int64, error)
	// EncodeStackRow 是**可选**的物品行编码器（J7）：把 (listType, slot, stack)
	// 编码成当前客户端 0x77 行字节。由 dnfbridge/gmtool_service.go 包住本进程
	// 唯一的编码器 currentItemListEntryFromStack（current_item_list_stack_projection.go:27）。
	// 未注入时晶块写入仍会落 ItemID/Count/Extra，只是不写 RawEntry —— 见
	// api_account_cubes.go 的 D4，不会凭空造字节。
	EncodeStackRow func(listType byte, slot int16, stack dnfrepo.ItemStack) []byte
}

// Server 是 GM 面板的 HTTP 服务。
type Server struct {
	cfg  Config
	deps Deps

	db  *sql.DB
	www http.Handler

	http *http.Server
	ln   net.Listener

	mu       sync.Mutex
	sessions map[string]time.Time
	failures map[string][]time.Time
	grants   map[string]grantRecord

	items *itemIndex
	pvf   *pvfCache

	stopOnce sync.Once
}

// New 构造 GM 面板；未配置监听地址或密码时返回 nil, nil。
func New(cfg Config, deps Deps) (*Server, error) {
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	if cfg.Listen == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return nil, errors.New("gm tool requires DNFBRIDGE_GM_PASSWORD")
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("gm tool requires DNFBRIDGE_GM_DSN (or the account API DSN)")
	}
	if strings.TrimSpace(cfg.Table) == "" {
		cfg.Table = "dnf90_web_accounts"
	}
	if deps.Repos == nil || deps.PVF == nil {
		return nil, errors.New("gm tool requires repository and PVF providers")
	}
	if cfg.Logger == nil {
		cfg.Logger = func(string, ...any) {}
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("gm tool open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	www, err := newStaticHandler(cfg.WWWRoot)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	server := &Server{
		cfg:      cfg,
		deps:     deps,
		db:       db,
		www:      www,
		sessions: make(map[string]time.Time),
		failures: make(map[string][]time.Time),
		grants:   make(map[string]grantRecord),
		pvf:      newPVFCache(),
	}
	// 开关开着却没有探针 = 守卫全程降级（放行 + 每次记 gm-online-guard-probe-missing）。
	// 启动时说清楚，避免操作者以为"已经拦住在线角色"。
	if cfg.RequireOffline && deps.Online == nil {
		cfg.Logger("gm-online-guard-probe-missing", "action", "startup", "reason", "probe_not_injected")
	}
	server.items = newItemIndex(cfg.ItemIndexCachePath, server, cfg.Logger)
	return server, nil
}

// Start 启动监听并异步构建物品索引。
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("gm tool ping database: %w", err)
	}
	mux := s.routes()
	s.http = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("gm tool listen %s: %w", s.cfg.Listen, err)
	}
	s.ln = ln
	go s.items.start(context.Background())
	go func() {
		s.cfg.Logger("dnf90 gm tool listening", "addr", s.cfg.Listen)
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.Logger("dnf90 gm tool stopped unexpectedly", "error", err)
		}
	}()
	return nil
}

// Stop 关闭 GM 面板。
func (s *Server) Stop() error {
	if s == nil {
		return nil
	}
	var err error
	s.stopOnce.Do(func() {
		if s.http != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			err = s.http.Shutdown(ctx)
		}
		if s.db != nil {
			err = errors.Join(err, s.db.Close())
		}
	})
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// 公开端点（A21 同样豁免）。
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// 账号。
	mux.HandleFunc("GET /api/accounts", s.handleAccountList)
	mux.HandleFunc("GET /api/accounts/{id}/detail", s.handleAccountDetail)
	mux.HandleFunc("POST /api/accounts/{id}/currency", s.handleAccountCurrency)
	mux.HandleFunc("POST /api/accounts/{id}/honor-level", s.handleAccountHonorLevel)
	mux.HandleFunc("POST /api/accounts/{id}/honor-level/max", s.handleAccountHonorLevelMax)
	mux.HandleFunc("POST /api/accounts/{id}/growth-capsule", s.handleAccountGrowthCapsule)
	mux.HandleFunc("POST /api/accounts/{id}/growth-capsule/max", s.handleAccountGrowthCapsule)
	// 账号备份/还原（api_account_backup.go）：备份是面板下载/回传的那份 JSON 文件本身，
	// 服务端不建表、不落目录；恢复先全校验再单事务“删该账号 → 按备份重建”。
	mux.HandleFunc("POST /api/accounts/{id}/backup", s.backupHandleAccountExport)
	mux.HandleFunc("POST /api/accounts/restore", s.backupHandleAccountRestore)
	// 环境信息（api_environment.go）：只报能观测到的值，绝不回显 DSN/口令/令牌。
	mux.HandleFunc("GET /api/environment", s.envHandleInfo)
	mux.HandleFunc("GET /api/activities", s.handleActivities)
	mux.HandleFunc("POST /api/activities", s.handleSaveActivities)
	// 账号金库（api_account_cargo.go）：扩容 / 清空 / 删除单条，都走既有金库 owner 写入路径。
	mux.HandleFunc("POST /api/accounts/{id}/cargo/max", s.handleAccountCargoMax)
	mux.HandleFunc("POST /api/accounts/{id}/cargo/clear", s.handleAccountCargoClear)
	mux.HandleFunc("POST /api/accounts/{id}/cargo/delete", s.handleAccountCargoDelete)

	// 角色。
	mux.HandleFunc("GET /api/characters", s.handleCharacterList)
	mux.HandleFunc("GET /api/characters/{id}", s.handleCharacterDetail)
	mux.HandleFunc("GET /api/characters/{id}/stats", s.handleCharacterStats)
	mux.HandleFunc("GET /api/characters/{id}/premium", s.handlePremiumStatus)
	mux.HandleFunc("POST /api/characters/{id}/premium", s.handlePremiumGrant)
	mux.HandleFunc("GET /api/characters/{id}/sptp", s.handleSpTp)
	mux.HandleFunc("POST /api/characters/{id}/sp", s.handleAdjustSpTp)
	mux.HandleFunc("POST /api/characters/{id}/sp/zero-remaining", s.handleZeroRemainingSpTp)
	mux.HandleFunc("POST /api/characters/{id}/level", s.handleSetLevel)
	mux.HandleFunc("GET /api/characters/{id}/growoptions", s.handleGrowOptions)
	mux.HandleFunc("POST /api/characters/{id}/growtype", s.handleSetGrowType)
	mux.HandleFunc("GET /api/characters/{id}/gold-limit", s.handleGoldLimit)
	mux.HandleFunc("POST /api/characters/{id}/gold-limit/max", s.handleGoldLimitMax)
	mux.HandleFunc("POST /api/characters/{id}/inventory-limit/max", s.handleInventoryLimitMax)
	mux.HandleFunc("POST /api/characters/{id}/inventory-limit/restore", s.handleInventoryLimitRestore)
	mux.HandleFunc("POST /api/characters/{id}/personal-cargo/max", s.handlePersonalCargoMax)
	mux.HandleFunc("POST /api/characters/{id}/avatar-inventory/max", s.handleAvatarInventoryMax)
	mux.HandleFunc("POST /api/characters/{id}/equipment-slots/unlock", s.handleUnlockEquipmentSlots)
	mux.HandleFunc("POST /api/characters/{id}/dungeon-permissions/unlock", s.handleUnlockDungeonPermissions)
	// 删除角色（api_character_delete.go）：软删除（delete_flag=1）并让出选人槽位，
	// 关联数据全部保留（仓储层没有 Delete、各表也没有级联，硬删才是"孤儿行"的来源）。
	mux.HandleFunc("POST /api/characters/{id}/delete", s.handleCharacterDelete)

	// 角色改名（api_character_rename.go）——**条件注册**。
	//
	// 这条路由是上游 A21 GM 工具 `POST /api/characters/{id}/rename` 的移植
	// （Services/GmService.CharacterRename.cs），用来解掉操作者批次 3 项 ①：
	// 选角界面上被标成违规的角色点【修改】后改名不生效（游戏信道 op135 在本
	// 服务端与厂商参考实现里都没有 handler，回包形态未取证）。
	//
	// `DNFBRIDGE_GM_CHARACTER_RENAME_MODE`（api_character_rename.go 的
	// CharacterRenameFromEnv）未设/空/未知值 = **启用**（默认）；显式
	// `off`/`0`/`false`/`no`/`disable`/`disabled`/`none`/`legacy` 时下面这一行
	// **不执行**：请求继续落 `/api/` 兜底「该功能未移植到 90CN 面板…」，
	// 与加这条路由之前的字节完全相同（这就是文档里那条回退档）。
	if s.characterRenameEnabled() {
		mux.HandleFunc("POST /api/characters/{id}/rename", s.handleCharacterRename)
	}

	// 复制角色：克隆预览 / 执行克隆 / 建目标账号 / 角色名查重（api_clone.go）。
	mux.HandleFunc("GET /api/characters/{id}/clone-plan", s.handleCharacterClonePlan)
	mux.HandleFunc("POST /api/characters/{id}/clone", s.handleCharacterClone)
	mux.HandleFunc("POST /api/accounts/create-for-clone", s.handleAccountCreateForClone)
	mux.HandleFunc("GET /api/characters/name-available", s.handleCharacterNameAvailable)

	// 物品与邮件。
	mux.HandleFunc("GET /api/items/categories", s.handleItemCategories)
	mux.HandleFunc("GET /api/items/browse", s.handleItemBrowse)
	mux.HandleFunc("GET /api/items/search", s.handleItemSearch)
	mux.HandleFunc("GET /api/characters/{id}/items", s.handleItemList)
	mux.HandleFunc("POST /api/characters/{id}/items", s.handleItemGrant)
	mux.HandleFunc("GET /api/characters/{id}/items/{templateId}/grant-options", s.handleGrantOptions)
	// 已穿/在包物品的再配置（强化/增幅/锻造等，api_items_configure.go）：先解析该物品真实能力再落库。
	mux.HandleFunc("GET /api/characters/{id}/items/config-options", s.handleItemConfigOptions)
	mux.HandleFunc("POST /api/characters/{id}/items/configure", s.handleItemConfigure)
	mux.HandleFunc("POST /api/characters/{id}/wallet", s.handleWallet)
	mux.HandleFunc("POST /api/characters/{id}/mailbox/clear", s.handleMailboxClear)

	// 只读查询面（P1，全部 GET，零写库、零开关）：
	//   - 物品详情：/api/items/{itemId}/detail（纯 PVF，上游 GMItemDetail）
	//   - 邮箱快照：/api/characters/{id}/mailbox（上游 GMMailbox）
	//   - 任务浏览：/api/characters/{id}/quests[/search|/{questId}/detail]
	//     （上游 GMQuestOverview / GMSearchQuests / GMQuestDetail）
	// 这些路由只新增，不改变上面任何一条既有路由的响应。
	mux.HandleFunc("GET /api/items/{itemId}/detail", s.handleItemDetail)
	mux.HandleFunc("GET /api/characters/{id}/mailbox", s.handleMailboxSnapshot)
	mux.HandleFunc("GET /api/characters/{id}/quests", s.handleQuestOverview)
	mux.HandleFunc("GET /api/characters/{id}/quests/search", s.handleQuestSearch)
	mux.HandleFunc("GET /api/characters/{id}/quests/{questId}/detail", s.handleQuestDetail)

	// P2 写路由（J5 邮件单封删除；J0 在线守卫见 online_guard.go）：
	// 整个面板里**只有这一条**路由会读 cfg.RequireOffline —— 默认档（开关未设）
	// 与今天逐字节相同，只是多了一条前端可以主动调用的新路由。
	mux.HandleFunc("POST /api/characters/{id}/mailbox/delete", s.handleMailboxDelete)

	// P3 写路由（J8 SP/TP 绝对值、J7 账号晶块）：
	//   - POST /api/characters/{id}/sp/set            绝对值设点，**不改**既有
	//     POST /api/characters/{id}/sp 的增量语义（计划书 §5.2 C7）。
	//   - GET/POST /api/characters/{id}/account-cubes 账号共享槽 354..359 的 6 格读写。
	// 三条都是新增路由；既有 41 条路由的注册行与 handler 一行未动。
	mux.HandleFunc("POST /api/characters/{id}/sp/set", s.handleSetSpAbsolute)
	mux.HandleFunc("GET /api/characters/{id}/account-cubes", s.handleAccountCubes)
	mux.HandleFunc("POST /api/characters/{id}/account-cubes", s.handleSetAccountCube)

	// P5 写路由（J9/J10/J11 任务写面；守卫与开关见 api_quest_mutations.go）：
	//   - POST .../quests/{questId}/accept|give-up  复用 dnfquest.Owner（唯一一套接取/放弃实现）
	//   - POST .../quests/{questId}/ready|complete|uncomplete
	//   - POST .../quests/complete-batch            ≤200 条、排除职业任务、单事务、逐条回报
	//   - POST .../quests/active/complete-batch     批次 3 项 ⑥：以「角色当前已接任务」
	//     为输入（缺省 questIds = 全部已接），条件注册，见下面那一段与 api_quest_active_batch.go
	// 全部是新增路由；上面既有路由的注册行与 handler 一行未动。
	// 总开关 DNFBRIDGE_GM_QUEST_MUTATE（空 = on）；职业任务写入另由
	// DNFBRIDGE_GM_QUEST_PROFESSION_WRITE（空 = off）单独把关。
	mux.HandleFunc("POST /api/characters/{id}/quests/complete-batch", s.handleQuestCompleteBatch)
	// 批次 3 项 ⑥（api_quest_active_batch.go）——**条件注册**，默认启用：
	//   POST .../quests/active/complete-batch   以「角色当前已接任务」为输入的批量完成
	//   （上游 Program.cs:350 的 `<范围>/complete-batch` 命名法；语义对照
	//    Services/GmService.Quests.cs:1297-1344 的 CompleteVisibleQuestBatch）。
	//
	// `DNFBRIDGE_GM_QUEST_MODE`（QuestModeFromEnv）未设/空/未知值 = **启用**（默认）；
	// 显式 off/0/false/no/disable/disabled/none/legacy 时下面这一行**不执行**：
	// 请求继续落 `/api/` 兜底「该功能未移植到 90CN 面板…」，且 GET …/quests 不再回
	// completionBatch 字段（前端据此不渲染新控件）—— 与加这条路由之前逐字节相同。
	if s.questModeEnabled() {
		mux.HandleFunc("POST /api/characters/{id}/quests/active/complete-batch", s.handleQuestActiveCompleteBatch)
	}
	mux.HandleFunc("POST /api/characters/{id}/quests/{questId}/accept", s.handleQuestAccept)
	mux.HandleFunc("POST /api/characters/{id}/quests/{questId}/give-up", s.handleQuestGiveUp)
	mux.HandleFunc("POST /api/characters/{id}/quests/{questId}/ready", s.handleQuestMarkReady)
	mux.HandleFunc("POST /api/characters/{id}/quests/{questId}/complete", s.handleQuestForceComplete)
	mux.HandleFunc("POST /api/characters/{id}/quests/{questId}/uncomplete", s.handleQuestUncomplete)

	// P4 写路由（J6 背包单格删除 / 整容器清空）——**条件注册，默认两条都不存在**。
	//
	// 这是全计划里立场冲突最尖锐的一批：本仓刻意拒绝直接写背包
	// （`api_items.go:327-329`），而这两条路由就是那条拒绝的反面。因此开关
	// `DNFBRIDGE_GM_ITEM_REMOVE`（api_items_delete.go 的 RequireItemRemoveFromEnv）
	// 未设/空/非 `on` 时，下面两行**一行都不会执行**：
	//
	//   - `POST /api/characters/{id}/items/delete`、`.../items/clear`
	//     继续落 `/api/` 兜底「该功能未移植到 90CN 面板…」= 逐字节今天的行为
	//     （既有用例 `api_quest_mutations_test.go:914` 正是在钉这一条）；
	//   - 不探针、不写日志、不碰仓储。
	//
	// 前端接线（P4b，2026-09-27）：`wwwroot/js/inventory.js` 的删除按钮与
	// "清空当前分类"现在打的就是上面这两条新路径（`container` 固定 `inventory`，
	// 依据见该文件顶部注释）。旧的 `…/items/batch-delete` 与 `…/items/delete-at`
	// 两条同族路径**从未注册过**、继续保持落 `/api/` 兜底 —— 所以关闭档下
	// 改动前后用户看到的都是同一条兜底文案（前端不探测开关、不隐藏按钮）。
	if s.cfg.RemoveInventory {
		mux.HandleFunc("POST /api/characters/{id}/items/delete", s.handleItemRemove)
		mux.HandleFunc("POST /api/characters/{id}/items/clear", s.handleItemClear)
	}

	// 未移植功能：前端仍可能调用，统一给出明确的中文失败，避免静默当成成功。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeFailure(w, unportedFeatureMessage)
	})

	// 静态前端（与 A21 一样在门禁之前提供，登录面板由 /api/status 驱动）。
	mux.Handle("/", noCache(s.www))

	return s.authenticate(mux)
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// authenticate 实现 A21 的门禁语义：静态资源与 status/login/logout 公开，
// 其余 /api/* 需要会话 Cookie，否则 401 + loginRequired。
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") ||
			path == "/api/status" ||
			path == "/api/auth/login" ||
			path == "/api/auth/logout" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authenticated(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":       false,
				"error":         "请先登录。",
				"loginRequired": true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	if !expires.After(now) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *Server) startSession() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	for existing, expires := range s.sessions {
		if !expires.After(time.Now()) {
			delete(s.sessions, existing)
		}
	}
	s.sessions[token] = time.Now().Add(sessionTTL)
	return token, nil
}

func (s *Server) endSession(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// allowLogin 限制单个来源 IP 的密码爆破。
func (s *Server) allowLogin(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-loginFailureWindow)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.failures[ip][:0]
	for _, hit := range s.failures[ip] {
		if hit.After(cutoff) {
			kept = append(kept, hit)
		}
	}
	s.failures[ip] = kept
	return len(kept) < loginFailuresPerIP
}

func (s *Server) recordLoginFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[ip] = append(s.failures[ip], time.Now())
}

func (s *Server) clearLoginFailures(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, ip)
}

// ---------------------------------------------------------------------------
// HTTP 辅助
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOK(w http.ResponseWriter, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["success"] = true
	writeJSON(w, payload)
}

func writeFailure(w http.ResponseWriter, message string) {
	writeJSON(w, map[string]any{"success": false, "error": message})
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var payload T
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		writeFailure(w, "请求读取失败")
		return payload, false
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return payload, true
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeFailure(w, "请求格式不正确")
		return payload, false
	}
	return payload, true
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := parseInt(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty")
	}
	negative := false
	if strings.HasPrefix(value, "-") {
		negative = true
		value = value[1:]
	}
	if value == "" {
		return 0, errors.New("empty")
	}
	var out int
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, errors.New("not a number")
		}
		out = out*10 + int(ch-'0')
	}
	if negative {
		out = -out
	}
	return out, nil
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newStaticHandler(root string) (http.Handler, error) {
	if strings.TrimSpace(root) != "" {
		return http.FileServer(http.Dir(root)), nil
	}
	sub, err := fs.Sub(embeddedWWW, "wwwroot")
	if err != nil {
		return nil, fmt.Errorf("gm tool embedded web root: %w", err)
	}
	return http.FileServer(http.FS(sub)), nil
}

func sha256Sum(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

func equalHash(a [32]byte, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
