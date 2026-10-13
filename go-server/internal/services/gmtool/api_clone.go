package gmtool

// 本文件是面板「复制角色」功能的**唯一**实现（A21 `GmService.CharacterClone*` 的 Go 移植）。
//
// 前端契约（wwwroot/js/character.js，逐字段对齐）：
//
//	GET  /api/characters/{id}/clone-plan      character.js:217 -> renderCharacterClonePlan（:229）
//	POST /api/characters/{id}/clone           character.js:361 -> {targetAccountId,newName,options}
//	POST /api/accounts/create-for-clone       character.js:398 -> {accountName,password,confirmPassword}
//	GET  /api/characters/name-available?name= character.js:334 -> {available,reason}
//
// 参考实现（A21 C#，只读）与移植点：
//
//	GmService.CharacterClone.cs
//	  :18-45     CharacterCloneOptions —— 选项键/显示名/默认勾选
//	  :64-111    GetCharacterClonePlan —— 账号列表字段（accountId/name/characterCount/
//	             slotLimit/canAcceptCharacter/isCurrent）+ options 三元组
//	  :113-134   CheckCharacterNameAvailable —— 先校验名字再查重
//	  :136-168   CreateAccountForClone —— 账号名/密码校验 + 建号
//	  :170-263   CloneCharacter —— 参数校验 → 安全校验 → 单事务复制 → 具名回报
//	  :265-274   NormalizeCloneOptions —— nil=默认勾选集合；**强制加 basic**
//	  :808-854   CharacterExists / CharacterNameExists 判据（delete_flag = 0）
//	  :856-889   ResolveFreeCharacterSlotIndex —— 最小空闲槽
//	  :891-900   CountCharactersByAccount —— 槽位上限
//	  :923-931   ValidateCharacterName —— 2..18 字节
//	GmService.CharacterCloneSafety.cs
//	  :46-75     ValidateCloneTableSafety —— 每张被复制的表必须有可安全改写的归属列
//	  :104-147   ValidateClonedCharacter —— 复制后行/槽位唯一性的回读校验
//
// 与 A21 的**有意差异**（都写在这里，不藏在代码里）：
//
//	D1 选项集合只保留 90CN 仓储层真的能整表复制的那部分。A21 的 titlebook / daily /
//	   quickSlots / wallet / avatars / locks / misc / audit 在 90CN 的
//	   `dnf_*` 表里**没有对应表**（标题簿/成就、每日周常、物品审计在本仓不存在），
//	   所以它们**不出现在 plan 里**；请求里带上这些键会得到具名错误，绝不静默丢弃
//	   （A21 :271 是静默丢弃）。
//	D2 不按"背包分类"切分 list 0。A21 用 A21InventorySlotPolicy 的槽位区间把主背包
//	   拆成装备/消耗品/材料/任务品/副职业/徽章（:642-683）；90CN 服务端没有这张区间表
//	   （唯一存在的区间是账号共享格 354..365，见 repository/account_inventory_repository.go:11-17），
//	   照抄会变成"发明槽位范围"。这里改成按仓储记录切：`inventory` = 角色背包记录
//	   （dnf_inventories.Slots）、`personalCargo` = 个人仓库（Warehouse）。
//	D3 账号共享格（list 0 的 354..365）**不复制**：它们属于账号不属于角色
//	   （IsAccountSharedInventorySlot，account_inventory_repository.go:41-46），
//	   复制进新角色只会造出一份 MergeAccountSharedInventory 会立即删掉的影子行
//	   （同文件 :55-70）。跳过的条数如实回报在 skippedAccountSharedSlots。
//	D4 宠物/装扮不需要 UID 重映射。A21 的 creature_key / avatar uid 是库级序列，
//	   必须重新分配（:498-529）；90CN 的 dnf_pet_entries / dnf_pet_clear_tokens /
//	   dnf_pet_artifacts 主键都是 (character_id, …)，条目键是角色内字符串，逐字节
//	   复制即正确，因此这里**不发明**任何新 id。
//	D5 不剥离"职业限制的穿戴物"。A21 的 StripJobRestrictedEquippedItems 存在的前提是
//	   复制可以改职业；本实现的 job/grow_type 与源角色逐字节相同，没有可剥离的对象。
//	D6 取消时**不写任何行**：所有写入都在 CharacterCreate 事务里（见下），任何一步
//	   失败都整体回滚，源角色一个字节都不动（A21 :251-258 同口径）。
//
// 数据面（表名/归属列来自 repository/mysql 的 schema，前缀取默认 `dnf`，
// mysql/schema.go:15）：写入一律经仓储层（repository.Group），GM 面板不直接拼 SQL
// 碰 DNF 库 —— 只有"启动器账号表"（cfg.Table，dnf90_web_accounts）走 s.db 原生 SQL，
// 与 api_accounts.go/deps.go 的既有做法一致。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskillcmd "longheng.io/server/internal/modules/dnf/skillcmd"
)

const (
	// cloneAccountBaseAccountID / cloneAccountMinPassword / cloneAccountMaxPassword
	// 与 dnfbridge/account_api.go:42-45 同源：面板建出来的账号就是启动器账号表里的
	// 一行，所以号段与密码长度必须逐字一致，否则同一个库里会出现两套规则
	// （启动器自助注册出来的账号与面板建出来的账号行为不同）。
	cloneAccountBaseAccountID = 10000
	cloneAccountMinPassword   = 6
	cloneAccountMaxPassword   = 64

	// cloneCharacterNameMinBytes / MaxBytes 逐字移植 A21 CharacterClone.cs:923-931
	// 的 ValidateCharacterName（UTF-8 字节数 2..18）。
	cloneCharacterNameMinBytes = 2
	cloneCharacterNameMaxBytes = 18

	// cloneDefaultSlotLimit 取仓储层的角色槽上限（repository.DefaultCharacterSlotLimit，
	// character_repository.go:13-16）。A21 用的是它自己 get_userinfo_template 里的 17
	// （CharacterClone.cs:15），90CN 的权威只有这一个常量，不照抄 17。
	cloneDefaultSlotLimit = dnfrepo.DefaultCharacterSlotLimit
)

// cloneAccountNamePattern 与 dnfbridge/account_api.go:54 的账号名规则逐字节相同。
var cloneAccountNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// cloneMutationGate 串行化本进程内的复制请求，等价于 A21
// CharacterClone.cs:16 的 CharacterCloneMutationGate（:183/:261 的 Wait/Release）。
//
// 为什么必须有：新角色号来自 Character.NextNumericID（mysql_char.go:643-659，
// `MAX(character_id)+1`），这个分配**不在事务里**。两个并发复制会算出同一个号，
// 后一个在 INSERT 主键冲突时回滚（数据不会坏，但这个失败本可避免）。串行化把竞争
// 收敛成排队；游戏自身的建号路径不受影响（那时冲突由主键兜住并如实报错）。
var cloneMutationGate sync.Mutex

// cloneOptionMeta 是一个复制选项的三元组（A21 CharacterCloneOptions，
// CharacterClone.cs:18-45 同结构）。
type cloneOptionMeta struct {
	Key            string
	Label          string
	DefaultChecked bool
}

// cloneOptions 是面板可选的复制范围，顺序即面板显示顺序。
//
// 键沿用 A21 :18-45；**只**列出 90CN 真能整表复制的那部分（差异 D1）。
// 90CN 侧对应的表见 cloneOptionOwnedTables。
//
// 注意 `quests` 的 defaultChecked 是 true（与 A21 一致），但前端
// character.js:182-198 的 CLONE_OPTIONS_DEFAULT_OFF / :268-280 的标签判据会把它
// 强制取消勾选 —— 那是前端的既定行为，服务端不重复实现。
var cloneOptions = []cloneOptionMeta{
	{Key: "basic", Label: "角色基础信息（含数值/位置/角色卡）", DefaultChecked: true},
	{Key: "skills", Label: "技能/技能点/技能栏", DefaultChecked: true},
	{Key: "quests", Label: "任务与已完成记录", DefaultChecked: true},
	{Key: "dungeon", Label: "地图难度/副本状态", DefaultChecked: true},
	{Key: "inventory", Label: "背包物品（含装扮/勋章容器）", DefaultChecked: true},
	{Key: "personalCargo", Label: "个人仓库", DefaultChecked: true},
	{Key: "containerState", Label: "背包/仓库扩容状态", DefaultChecked: true},
	{Key: "equipment", Label: "身上装备", DefaultChecked: true},
	{Key: "pets", Label: "宠物", DefaultChecked: true},
}

// cloneOwnedTable 是一张会被复制写入的表，以及它的**角色归属列**。
//
// 这是 A21 GmService.CharacterCloneSafety.cs:46-75 `ValidateCloneTableSafety` 的移植：
// 每条被复制的表都必须有可安全改写的归属列（否则"把源角色的行插进新角色"这件事本身
// 就不成立）。A21 在 SQLite 上用 PRAGMA 现查（:11-33、:54-74）；90CN 的物理表名与
// 归属列由 repository/mysql 决定，GM 面板拿不到 DDL，所以这里把归属关系**声明**出来，
// 由 api_clone_test.go 钉死两件事：
//
//  1. cloneOptions 里每个选项都在 cloneOptionOwnedTables 里有条目；
//  2. 归属列只能是 character_id / account_id / scope（scope 见下方 settings 的说明）。
type cloneOwnedTable struct {
	Table       string
	OwnerColumn string
}

// cloneOptionOwnedTables 是"选项 → 真正写入的表"的权威清单。
//
// 表名取 MySQL 仓储的默认前缀 `dnf`（schema.go:15 defaultTablePrefix，
// 物理名由 mysqlTable() 拼出）。归属列全部来自 schema：
//
//	dnf_characters                 PK(character_id)              schema.go:228-241
//	dnf_character_stats            PK(character_id,stat_key)     schema_relational.go:42-47
//	dnf_character_locations        PK(character_id)              schema_relational.go:48-55
//	dnf_character_rosters          PK(character_id)              schema_relational.go:267-312
//	dnf_character_roster_equipment PK(character_id,ordinal)      schema_relational.go:57-68
//	dnf_character_roster_lists     PK(character_id,list_name,..) schema_relational.go:69-75
//	dnf_inventories                PK(character_id)              schema.go:242-246
//	dnf_inventory_items            PK(character_id,col,entry)    schema_relational.go:173-191
//	dnf_inventory_item_extra       PK(character_id,col,entry,k)  schema_relational.go:193-208
//	dnf_equipments                 PK(character_id)              schema.go:247-251
//	dnf_equipment_entries          PK(character_id,entry_key)    schema_relational.go:78-87
//	dnf_equipment_entry_extra      PK(character_id,entry_key,k)  schema_relational.go:88
//	dnf_pets                       PK(character_id)              schema.go:252-258
//	dnf_pet_entries                PK(character_id,pet_key)      schema_relational.go:210-231
//	dnf_pet_entry_extra            PK(character_id,entry_key,k)  schema_relational.go:90
//	dnf_pet_clear_tokens           PK(character_id,pet_key,tok)  schema_relational.go:91-99
//	dnf_pet_artifacts              PK(character_id,col,entry)    schema_relational.go:100
//	dnf_pet_artifact_extra         PK(character_id,col,entry,k)  schema_relational.go:101
//	dnf_quests                     PK(character_id)              schema.go:259-263
//	dnf_quest_states               PK(character_id,group,quest)  schema_relational.go:102-113
//	dnf_quest_state_extra          PK(character_id,group,q,k)    schema_relational.go:114-121
//	dnf_skills                     PK(character_id)              schema.go:264-273
//	dnf_skill_states               PK(character_id,skill_id)     schema_relational.go:122-128
//	dnf_skill_layouts              PK(character_id,tree,slot)    schema_relational.go:129-135
//	dnf_skill_cooldowns            PK(character_id,skill_id)     schema_relational.go:136-141
//	dnf_dungeon_permissions        PK(character_id,dungeon_id)   schema.go:274-282
//	dnf_settings                   PK(scope)                    schema.go:296-300
//	dnf_setting_values             PK(scope,entry_key)          schema_relational.go:164-171
//
// settings 家族的归属列是 `scope`，不是 character_id：本实现写入的两个 scope 都由
// 角色号派生（repository.CharacterContainerStateScope、
// skillcmd.SkillCommandScope），所以新行的主键必然与源行不同 —— 同一条安全性判据
// （"唯一键里必须含归属信息"，A21 Safety:67-72）在 scope 形态下依然成立。
// 表中每一条都由 api_clone_test.go 的 TestCloneOptionTablesDeclareOwnership 静态钉死。
var cloneOptionOwnedTables = map[string][]cloneOwnedTable{
	"basic": {
		{Table: "dnf_characters", OwnerColumn: "character_id"},
		{Table: "dnf_character_stats", OwnerColumn: "character_id"},
		{Table: "dnf_character_locations", OwnerColumn: "character_id"},
		{Table: "dnf_character_rosters", OwnerColumn: "character_id"},
		{Table: "dnf_character_roster_equipment", OwnerColumn: "character_id"},
		{Table: "dnf_character_roster_lists", OwnerColumn: "character_id"},
	},
	"skills": {
		{Table: "dnf_skills", OwnerColumn: "character_id"},
		{Table: "dnf_skill_states", OwnerColumn: "character_id"},
		{Table: "dnf_skill_layouts", OwnerColumn: "character_id"},
		{Table: "dnf_skill_cooldowns", OwnerColumn: "character_id"},
		// 技能指令自定义（op331）与技能栏同属一份状态，scope 由角色号派生。
		{Table: "dnf_settings", OwnerColumn: "scope"},
		{Table: "dnf_setting_values", OwnerColumn: "scope"},
	},
	"quests": {
		{Table: "dnf_quests", OwnerColumn: "character_id"},
		{Table: "dnf_quest_states", OwnerColumn: "character_id"},
		{Table: "dnf_quest_state_extra", OwnerColumn: "character_id"},
	},
	"dungeon": {
		{Table: "dnf_dungeon_permissions", OwnerColumn: "character_id"},
	},
	"inventory": {
		{Table: "dnf_inventories", OwnerColumn: "character_id"},
		{Table: "dnf_inventory_items", OwnerColumn: "character_id"},
		{Table: "dnf_inventory_item_extra", OwnerColumn: "character_id"},
	},
	"personalCargo": {
		{Table: "dnf_inventories", OwnerColumn: "character_id"},
		{Table: "dnf_inventory_items", OwnerColumn: "character_id"},
		{Table: "dnf_inventory_item_extra", OwnerColumn: "character_id"},
	},
	"containerState": {
		{Table: "dnf_settings", OwnerColumn: "scope"},
		{Table: "dnf_setting_values", OwnerColumn: "scope"},
	},
	"equipment": {
		{Table: "dnf_equipments", OwnerColumn: "character_id"},
		{Table: "dnf_equipment_entries", OwnerColumn: "character_id"},
		{Table: "dnf_equipment_entry_extra", OwnerColumn: "character_id"},
	},
	"pets": {
		{Table: "dnf_pets", OwnerColumn: "character_id"},
		{Table: "dnf_pet_entries", OwnerColumn: "character_id"},
		{Table: "dnf_pet_entry_extra", OwnerColumn: "character_id"},
		{Table: "dnf_pet_clear_tokens", OwnerColumn: "character_id"},
		{Table: "dnf_pet_artifacts", OwnerColumn: "character_id"},
		{Table: "dnf_pet_artifact_extra", OwnerColumn: "character_id"},
	},
}

// cloneRequest 是 `POST /api/characters/{id}/clone` 的请求体。
// 字段名与 character.js:361 的 `{targetAccountId, newName, options}` 逐字一致。
type cloneRequest struct {
	TargetAccountID int64    `json:"targetAccountId"`
	NewName         string   `json:"newName"`
	Options         []string `json:"options"`
}

// cloneAccountRequest 是 `POST /api/accounts/create-for-clone` 的请求体。
// 字段名与 character.js:398-402 逐字一致。
type cloneAccountRequest struct {
	AccountName     string `json:"accountName"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
}

// ---------------------------------------------------------------------------
// GET /api/characters/{id}/clone-plan
// ---------------------------------------------------------------------------

// handleCharacterClonePlan 输出"复制这个角色会发生什么"：可选目标账号 + 选项清单。
//
// A21 对照：GetCharacterClonePlan（CharacterClone.cs:64-111）。响应字段与前端
// renderCharacterClonePlan（character.js:229-260）/ updateCloneAccountLimit（:321-327）
// 读取的名字逐字一致：
//
//	sourceCharacterId  number     面板角色号
//	sourceAccountId    number     源角色所在**面板账号**号（前端 select.value = String(...)）
//	slotLimit          number     角色槽上限
//	accounts[]         {accountId, name, characterCount, slotLimit, canAcceptCharacter, isCurrent}
//	options[]          {key, label, defaultChecked}
func (s *Server) handleCharacterClonePlan(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeFailure(w, "启动器账号表不可用（面板数据库未连接）。")
		return
	}
	accounts, err := s.webAccounts(ctx)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, s.clonePlanPayload(ctx, group, character, panelID, accounts))
}

// clonePlanPayload 是 clone-plan 的载荷构建（不碰 s.db，单测直接调用它）。
// 与 api_accounts.go:41-67 的 accountListPayload 同一取舍：把依赖数据库的那半段
// 留在 handler 里，业务/契约部分单独可测。
func (s *Server) clonePlanPayload(
	ctx context.Context,
	group dnfrepo.Group,
	source dnfrepo.CharacterRecord,
	sourcePanelID int,
	accounts []webAccount,
) map[string]any {
	sourceAccountPanelID := s.accountPanelID(source.AccountID)
	rows := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		characters, err := s.charactersForAccount(ctx, group, account.AccountID)
		if err != nil {
			// 与 accountListPayload 同一口径：单个账号读不出来就按 0 个角色展示，
			// 不让整个 plan 失败（否则玩家会看到"复制功能坏了"）。
			characters = nil
		}
		count := len(characters)
		rows = append(rows, map[string]any{
			"accountId":          account.PanelID,
			"name":               account.Account,
			"characterCount":     count,
			"slotLimit":          cloneDefaultSlotLimit,
			"canAcceptCharacter": count < cloneDefaultSlotLimit,
			"isCurrent":          account.PanelID == int64(sourceAccountPanelID),
		})
	}
	options := make([]map[string]any, 0, len(cloneOptions))
	for _, option := range cloneOptions {
		options = append(options, map[string]any{
			"key":            option.Key,
			"label":          option.Label,
			"defaultChecked": option.DefaultChecked,
		})
	}
	return map[string]any{
		"sourceCharacterId": sourcePanelID,
		"sourceAccountId":   sourceAccountPanelID,
		"slotLimit":         cloneDefaultSlotLimit,
		"accounts":          rows,
		"options":           options,
	}
}

// ---------------------------------------------------------------------------
// GET /api/characters/name-available?name=
// ---------------------------------------------------------------------------

// handleCharacterNameAvailable 报告候选角色名是否可用。
//
// A21 对照：CheckCharacterNameAvailable（CharacterClone.cs:113-134）。响应字段与
// character.js:334-337 读取的名字一致：`available`(bool) + `reason`(string)。
// 名字不合法时同样是 HTTP 200 + available:false + 具名 reason（A21 :117-118 同口径），
// 前端把它当"不可用 + 原因"显示，不是异常。
//
// 查重口径：A21 用 `name = @name OR name = @nameBytes OR name_bytes = @nameBytes`
// （:124-131）。90CN 的 dnf_characters 只有 `name` 一列（schema.go:232），且仓储层
// 已提供与建号同源的查重（Character.FindIDByName，mysql_char.go:616-639，
// `WHERE name = ? AND delete_flag = 0`），这里直接用它，不另写一套 SQL。
func (s *Server) handleCharacterNameAvailable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if message := cloneValidateCharacterName(name); message != "" {
		writeOK(w, map[string]any{"available": false, "reason": message})
		return
	}
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	existing, found, lookupErr := group.Character.FindIDByName(ctx, name)
	if lookupErr != nil {
		writeFailure(w, "检查角色名失败: "+lookupErr.Error())
		return
	}
	if found {
		s.logEvent("gm-character-clone-name-taken", "name", name, "character_id", existing)
		writeOK(w, map[string]any{"available": false, "reason": "角色名已存在"})
		return
	}
	writeOK(w, map[string]any{"available": true, "reason": ""})
}

// ---------------------------------------------------------------------------
// POST /api/characters/{id}/clone
// ---------------------------------------------------------------------------

// handleCharacterClone 把源角色复制成一个新角色。
//
// A21 对照：CloneCharacter（CharacterClone.cs:170-263）。前半段（角色/请求体/账号解析）
// 在这里，写库部分在 applyCharacterClone —— 与 api_accounts.go:201-206 对
// applyAccountCurrency 的取舍相同：单测没有面板数据库，把不依赖 s.db 的那一段单独放，
// 才能对每条分支做逐字节断言。
func (s *Server) handleCharacterClone(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil || panelID <= 0 {
		writeFailure(w, "角色编号无效")
		return
	}
	body, ok := decodeBody[cloneRequest](w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	source, loadErr := s.loadCharacter(ctx, group, panelID)
	if loadErr != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	// 参数本身不合法时不必碰启动器账号表（A21 :174-181 也是先校验请求）。
	if body.TargetAccountID <= 0 {
		writeFailure(w, "请选择目标账号")
		return
	}
	if message := cloneValidateCharacterName(body.NewName); message != "" {
		writeFailure(w, message)
		return
	}
	if _, optionErr := cloneNormalizeOptions(body.Options); optionErr != nil {
		writeFailure(w, optionErr.Error())
		return
	}
	if s.db == nil {
		writeFailure(w, "启动器账号表不可用（面板数据库未连接）。")
		return
	}
	target, targetErr := s.webAccountByPanelID(ctx, body.TargetAccountID)
	if targetErr != nil {
		writeFailure(w, fmt.Sprintf("目标账号不存在: %d", body.TargetAccountID))
		return
	}
	s.applyCharacterClone(w, ctx, group, source, panelID, target, body)
}

// applyCharacterClone 是 clone 路由在**源角色与目标账号都已解析**之后的全部行为：
// 校验 → 快照 → 单事务写入 → 事务内校验 → 如实回报。单测直接调用它。
func (s *Server) applyCharacterClone(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	source dnfrepo.CharacterRecord,
	sourcePanelID int,
	target webAccount,
	body cloneRequest,
) {
	// ---- 参数校验（A21 :172-181）----
	if body.TargetAccountID <= 0 {
		writeFailure(w, "请选择目标账号")
		return
	}
	if message := cloneValidateCharacterName(body.NewName); message != "" {
		writeFailure(w, message)
		return
	}
	newName := strings.TrimSpace(body.NewName)
	selected, optionErr := cloneNormalizeOptions(body.Options)
	if optionErr != nil {
		writeFailure(w, optionErr.Error())
		return
	}

	// ---- 安全校验（A21 :193-210）----
	if statInt(source, "delete_flag") != 0 {
		// A21 的 CharacterExists 带 `AND delete_flag = 0`（:814）：软删角色不是复制源。
		writeFailure(w, fmt.Sprintf("源角色 %d 已被删除，无法复制。", sourcePanelID))
		return
	}
	targetAccountID := strings.TrimSpace(target.AccountID)
	if targetAccountID == "" {
		writeFailure(w, "目标账号不存在。")
		return
	}
	if group.Character == nil || group.CharacterCreate == nil {
		writeFailure(w, "角色创建事务不可用（"+dnfrepo.ErrCharacterCreationTransactionUnavailable.Error()+"）。")
		return
	}

	// ---- 在线守卫（P2 起新增写路由的统一配方，见 online_guard.go:36-46）----
	// 默认档（cfg.RequireOffline=false）直接放行、不探针、不写日志。
	if !s.requireOffline(w, "character-clone", sourcePanelID) {
		return
	}

	// 从这里开始写库：先串行化（A21 :183 的 mutation gate），再算槽位/名字。
	cloneMutationGate.Lock()
	defer cloneMutationGate.Unlock()

	records, listErr := group.Character.ListByAccount(ctx, targetAccountID, cloneDefaultSlotLimit)
	if listErr != nil {
		writeFailure(w, "读取目标账号角色列表失败: "+listErr.Error())
		return
	}
	visible := make([]dnfrepo.CharacterRecord, 0, len(records))
	for _, record := range records {
		if statInt(record, "delete_flag") != 0 {
			continue
		}
		visible = append(visible, record)
	}
	if len(visible) >= cloneDefaultSlotLimit {
		writeFailure(w, fmt.Sprintf("目标账号角色数量已达上限 %d", cloneDefaultSlotLimit))
		return
	}
	slot, slotOK := cloneFreeSlot(visible, cloneDefaultSlotLimit)
	if !slotOK {
		writeFailure(w, "目标账号没有可用角色槽位")
		return
	}
	existingName, nameFound, nameErr := group.Character.FindIDByName(ctx, newName)
	if nameErr != nil {
		writeFailure(w, "检查角色名失败: "+nameErr.Error())
		return
	}
	if nameFound {
		s.logEvent("gm-character-clone-name-taken", "name", newName, "character_id", existingName,
			"result", "rejected")
		writeFailure(w, "角色名已存在: "+newName)
		return
	}

	// ---- 事务外快照源角色 ----
	// 为什么在事务外读：写事务只覆盖**新角色**（见下），源角色一个字节都不写
	// （红线：复制永不破坏源角色）。A21 在同一个 SQLite 事务里读源行（:209-230），
	// 90CN 的建号事务是"新角色作用域"的（mysql_transaction.go:70-96 的
	// newMySQLGroupFromRouter，内存实现的 memoryCharacterCreationStore 只认新角色号），
	// 所以快照必须先取好。取快照与提交之间源角色若被改动，本次复制仍然是
	// "复制那一刻读到的真实行"，不会凭空造数据。
	snapshot, snapshotErr := s.cloneCharacterSnapshot(ctx, group, source, selected)
	if snapshotErr != nil {
		writeFailure(w, snapshotErr.Error())
		return
	}

	newPanelID, idErr := group.Character.NextNumericID(ctx)
	if idErr != nil {
		writeFailure(w, "分配新角色编号失败: "+idErr.Error())
		return
	}
	if newPanelID <= 0 {
		writeFailure(w, "分配新角色编号失败: 仓储返回了无效编号")
		return
	}
	newCharacterID := strconv.Itoa(newPanelID)
	now := time.Now().UTC()

	// ---- 单事务写入（A21 :191-248 同口径：任何一步失败都整体回滚）----
	cloneErr := group.CharacterCreate.WithinCharacterCreation(ctx, newCharacterID, group, func(tx dnfrepo.Group) error {
		return cloneWriteCharacter(ctx, tx, source, targetAccountID, newName, slot, newPanelID, selected, snapshot, now)
	})
	if cloneErr != nil {
		s.logEvent("gm-character-clone-failed",
			"source_character_id", sourcePanelID, "target_account_id", target.PanelID,
			"target_account", targetAccountID, "name", newName, "slot", slot,
			"error", cloneErr.Error(), "result", "rolled_back")
		writeFailure(w, "复制失败，事务已回滚: "+cloneErr.Error())
		return
	}

	// ---- 提交后回读：把"确实落库了"作为最后一道事实核对（A21 Safety:104-147 的
	// 校验在事务内已经做过，这里再确认提交本身没有丢东西）----
	stored, found, verifyErr := group.Character.Load(ctx, newCharacterID)
	if verifyErr != nil || !found {
		detail := "回读不到该行"
		if verifyErr != nil {
			detail = verifyErr.Error()
		}
		s.logEvent("gm-character-clone-verify-failed", "source_character_id", sourcePanelID,
			"character_id", newPanelID, "error", detail, "result", "committed_unverified")
		writeFailure(w, fmt.Sprintf(
			"复制已提交，但提交后回读失败（新角色 #%d: %s）；请以客户端角色列表为准。", newPanelID, detail))
		return
	}
	if strings.TrimSpace(stored.AccountID) != targetAccountID || stored.Slot != slot || stored.Name != newName {
		s.logEvent("gm-character-clone-verify-failed", "source_character_id", sourcePanelID,
			"character_id", newPanelID, "account_id", stored.AccountID, "slot", stored.Slot,
			"name", stored.Name, "result", "committed_mismatch")
		writeFailure(w, fmt.Sprintf(
			"复制已提交，但回读的角色行与请求不一致（新角色 #%d）；请以客户端角色列表为准。", newPanelID))
		return
	}

	copied := cloneSelectedKeys(selected)
	s.logEvent("gm-character-clone",
		"source_character_id", sourcePanelID, "character_id", newPanelID,
		"source_account_id", panelIDFromAccountID(source.AccountID), "target_account_id", target.PanelID,
		"target_account", targetAccountID, "name", newName, "slot", slot,
		"options", strings.Join(copied, ","), "skipped_account_shared_slots", snapshot.accountSharedSkipped,
		"result", "applied")
	writeOK(w, map[string]any{
		"sourceCharacterId":         sourcePanelID,
		"sourceAccountId":           panelIDFromAccountID(source.AccountID),
		"characterId":               newPanelID,
		"targetAccountId":           target.PanelID,
		"name":                      newName,
		"slotIndex":                 slot,
		"copiedOptions":             copied,
		"skippedAccountSharedSlots": snapshot.accountSharedSkipped,
	})
}

// ---------------------------------------------------------------------------
// POST /api/accounts/create-for-clone
// ---------------------------------------------------------------------------

// handleAccountCreateForClone 建一个可以拿来做复制目标的账号。
//
// A21 对照：CreateAccountForClone（CharacterClone.cs:136-168）。响应字段与
// character.js:398-410 读取的名字一致：`accountId`(面板账号号) + `name`。
//
// 写入范围：**只有启动器账号表一行**（cfg.Table，默认 dnf90_web_accounts），
// 列与 dnfbridge/account_api.go:213-224 建表语句一致：
// account / password_hash(bcrypt) / account_id / created_at。
// account_id 的算法逐字移植 account_api.go:363（`dnf:%d`，基号 10000），
// 于是面板建出来的账号与启动器自助注册出来的账号完全同构。
//
// 为什么不顺手建 DNF 侧 dnf_accounts 行：那条行由游戏会话在首次进入场景时按
// 既定流程补建（dnfbridge/account_record_bootstrap.go:100-153 的
// ensureAccountLevelRecords，带 source/state/元数据），而且账号级容器也在那里一起
// 补齐。这里另建一份就得自己发明 State/Metadata，宁可留给那条唯一实现。
func (s *Server) handleAccountCreateForClone(w http.ResponseWriter, r *http.Request) {
	if s.usesLocalLoginAccounts() {
		writeFailure(w, "本地账号请通过本地登录器注册，再刷新面板选择该账号。")
		return
	}
	body, ok := decodeBody[cloneAccountRequest](w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := strings.TrimSpace(body.AccountName)
	if message := cloneValidateAccountRequest(name, body.Password, body.ConfirmPassword); message != "" {
		writeFailure(w, message)
		return
	}
	if s.db == nil {
		writeFailure(w, "启动器账号表不可用（面板数据库未连接）。")
		return
	}

	// 账号名唯一（A21 :151-152；与 account_api.go:321-330 同口径）。
	var existing string
	query := "SELECT account FROM " + quoteIdentifier(s.cfg.Table) + " WHERE account = ?"
	switch err := s.db.QueryRowContext(ctx, query, name).Scan(&existing); {
	case err == nil:
		writeFailure(w, fmt.Sprintf("账号名已存在: %s", name))
		return
	case !errors.Is(err, sql.ErrNoRows):
		writeFailure(w, "查询账号失败: "+err.Error())
		return
	}

	hash, hashErr := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if hashErr != nil {
		writeFailure(w, "密码处理失败: "+hashErr.Error())
		return
	}

	tx, txErr := s.db.BeginTx(ctx, nil)
	if txErr != nil {
		writeFailure(w, "创建账号失败: "+txErr.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	// account_id 在拿到自增主键之前不可知，所以先写占位再回填 —— 与
	// account_api.go:345-368 完全同一手法（唯一键 uniq_account_id 也就能兜住并发）。
	placeholder := fmt.Sprintf("pending:%d", time.Now().UnixNano())
	insert := "INSERT INTO " + quoteIdentifier(s.cfg.Table) +
		" (account, password_hash, account_id, created_at) VALUES (?, ?, ?, ?)"
	result, insertErr := tx.ExecContext(ctx, insert, name, string(hash), placeholder, time.Now())
	if insertErr != nil {
		if strings.Contains(strings.ToLower(insertErr.Error()), "duplicate") {
			writeFailure(w, fmt.Sprintf("账号名已存在: %s", name))
			return
		}
		writeFailure(w, "创建账号失败: "+insertErr.Error())
		return
	}
	rowID, idErr := result.LastInsertId()
	if idErr != nil {
		writeFailure(w, "创建账号失败: "+idErr.Error())
		return
	}
	accountID := cloneLauncherAccountID(rowID)
	update := "UPDATE " + quoteIdentifier(s.cfg.Table) + " SET account_id = ? WHERE id = ?"
	if _, updateErr := tx.ExecContext(ctx, update, accountID, rowID); updateErr != nil {
		writeFailure(w, "创建账号失败: "+updateErr.Error())
		return
	}
	if commitErr := tx.Commit(); commitErr != nil {
		writeFailure(w, "创建账号失败: "+commitErr.Error())
		return
	}
	s.logEvent("gm-account-create-for-clone", "account", name, "account_id", accountID,
		"panel_id", rowID, "result", "created")
	writeOK(w, map[string]any{
		"accountId": rowID,
		"name":      name,
		// 额外事实：DNF 侧账号号。前端不读它，操作者拿它去库里核对时不用再算一遍。
		"gameAccountId": accountID,
	})
}

// cloneLauncherAccountID 逐字移植 dnfbridge/account_api.go:45 + :363 的账号号算法。
func cloneLauncherAccountID(rowID int64) string {
	return fmt.Sprintf("dnf:%d", cloneAccountBaseAccountID+rowID)
}

// ---------------------------------------------------------------------------
// 校验 / 选项 / 槽位
// ---------------------------------------------------------------------------

// cloneValidateCharacterName 逐字移植 A21 CharacterClone.cs:923-931。
// 返回空串 = 合法；否则返回给操作者看的中文原因。
func cloneValidateCharacterName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "角色名不能为空"
	}
	size := len([]byte(trimmed))
	if size < cloneCharacterNameMinBytes || size > cloneCharacterNameMaxBytes {
		return "角色名长度需要为 2-18 字节"
	}
	return ""
}

// cloneValidateAccountRequest 合并 A21 CreateAccountForClone 的前四道校验
// （CharacterClone.cs:138-144）与 dnfbridge 账号 API 的账号名/密码长度规则
// （account_api.go:54、:469-480）。两条来源都不放宽：面板建出来的账号必须能
// 用同一套启动器登录，所以规则必须一致。
func cloneValidateAccountRequest(name, password, confirmPassword string) string {
	if name == "" {
		return "账号名不能为空"
	}
	if !cloneAccountNamePattern.MatchString(name) {
		return "账号名只能是 3-20 位字母、数字或下划线"
	}
	if password == "" {
		return "密码不能为空"
	}
	if password != confirmPassword {
		return "两次输入的密码不一致"
	}
	if len(password) < cloneAccountMinPassword {
		return fmt.Sprintf("密码至少 %d 位", cloneAccountMinPassword)
	}
	if len(password) > cloneAccountMaxPassword {
		return fmt.Sprintf("密码不能超过 %d 位", cloneAccountMaxPassword)
	}
	return ""
}

// cloneNormalizeOptions 把请求里的选项键规范化成集合。
//
// A21 对照：NormalizeCloneOptions（CharacterClone.cs:265-274）。
// 语义相同的两条：options 为 nil = 取 defaultChecked 集合；**basic 永远在集合里**
// （:272，对应前端把 basic 复选框设成 checked+disabled，character.js:251-254）。
//
// 有意差异 D1：A21 用 `.Where(o => known.Contains(o))` 静默丢弃未知键；这里改成
// 具名错误 —— 面板报"复制成功"但少复制了东西，比报错危险得多（本仓红线：
// 不能默默成功）。空串元素同样被忽略（C# 也会丢弃它，且前端不会产生）。
func cloneNormalizeOptions(options []string) (map[string]bool, error) {
	// 键匹配大小写不敏感（A21 :267-270 用 StringComparer.OrdinalIgnoreCase），
	// 所以比较的是 lower(输入) 与 lower(选项键)，命中的是**规范键**。
	known := make(map[string]string, len(cloneOptions))
	for _, option := range cloneOptions {
		known[strings.ToLower(option.Key)] = option.Key
	}
	selected := map[string]bool{"basic": true}
	if options == nil {
		for _, option := range cloneOptions {
			if option.DefaultChecked {
				selected[option.Key] = true
			}
		}
		return selected, nil
	}
	for _, raw := range options {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		canonical, ok := known[key]
		if !ok {
			return nil, fmt.Errorf("不支持的复制选项 %q（本面板支持: %s）",
				strings.TrimSpace(raw), strings.Join(cloneOptionKeys(), ", "))
		}
		selected[canonical] = true
	}
	return selected, nil
}

// cloneOptionKeys 返回全部选项键（按 cloneOptions 顺序，便于错误文案稳定）。
func cloneOptionKeys() []string {
	keys := make([]string, 0, len(cloneOptions))
	for _, option := range cloneOptions {
		keys = append(keys, option.Key)
	}
	return keys
}

// cloneSelectedKeys 返回已被选中的键（排序，与 A21 :246 的 OrderBy 一致）。
func cloneSelectedKeys(selected map[string]bool) []string {
	keys := make([]string, 0, len(selected))
	for key, on := range selected {
		if on {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// cloneFreeSlot 是最小空闲槽分配，逐字移植 A21 CharacterClone.cs:856-889 的
// ResolveFreeCharacterSlotIndex（与 dnfbridge/character_repository.go:45-56 的
// nextCharacterSlot 同口径）。records 里只应包含可见（delete_flag=0）角色。
func cloneFreeSlot(records []dnfrepo.CharacterRecord, limit int) (int, bool) {
	if limit <= 0 {
		return 0, false
	}
	used := make(map[int]struct{}, len(records))
	for _, record := range records {
		used[record.Slot] = struct{}{}
	}
	for slot := 0; slot < limit; slot++ {
		if _, taken := used[slot]; !taken {
			return slot, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// 快照 / 写入
// ---------------------------------------------------------------------------

// cloneCharacterSnapshot 是源角色在"复制那一刻"的真实子记录。
//
// has* 字段区分"源角色没有这条记录"与"记录是空的"：没有的就不写（绝不为了凑齐
// 而造一条空行，A21 :289 的 tables.RemoveWhere(table => !TableExists(...)) 同一立场）。
type cloneCharacterSnapshot struct {
	inventory            dnfrepo.InventoryRecord
	hasInventory         bool
	equipment            dnfrepo.EquipmentRecord
	hasEquipment         bool
	skill                dnfrepo.SkillRecord
	hasSkill             bool
	skillCommands        dnfrepo.SettingsRecord
	hasSkillCommands     bool
	quest                dnfrepo.QuestRecord
	hasQuest             bool
	pet                  dnfrepo.PetRecord
	hasPet               bool
	dungeon              dnfrepo.DungeonPermissionRecord
	hasDungeon           bool
	containerState       dnfrepo.SettingsRecord
	hasContainerState    bool
	accountSharedSkipped int
}

// cloneCharacterSnapshot 读齐本次选中范围的源记录。任何读取失败都直接失败
// （绝不把"读不到"当成"没有"：那会复制出一个静默残缺的角色）。
func (s *Server) cloneCharacterSnapshot(
	ctx context.Context,
	group dnfrepo.Group,
	source dnfrepo.CharacterRecord,
	selected map[string]bool,
) (cloneCharacterSnapshot, error) {
	var snapshot cloneCharacterSnapshot
	sourceID := strings.TrimSpace(source.CharacterID)
	if sourceID == "" {
		return snapshot, errors.New("源角色编号为空，无法复制")
	}

	if selected["inventory"] || selected["personalCargo"] {
		if group.Inventory == nil {
			return snapshot, errors.New("背包仓储未就绪，无法复制背包")
		}
		record, found, err := group.Inventory.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色背包失败: %w", err)
		}
		if found {
			snapshot.inventory = dnfrepo.CloneInventory(record)
			snapshot.hasInventory = true
		}
	}
	if selected["inventory"] {
		// 差异 D3：账号共享格（list 0 的 354..365）属于账号，不进新角色。
		filtered := make(map[string]dnfrepo.ItemStack, len(snapshot.inventory.Slots))
		for key, stack := range snapshot.inventory.Slots {
			if cloneSlotIsAccountShared(key) {
				snapshot.accountSharedSkipped++
				continue
			}
			filtered[key] = stack
		}
		snapshot.inventory.Slots = filtered
	} else {
		snapshot.inventory.Slots = nil
	}
	if !selected["personalCargo"] {
		snapshot.inventory.Warehouse = nil
	}

	if selected["equipment"] {
		if group.Equipment == nil {
			return snapshot, errors.New("穿戴装备仓储未就绪，无法复制装备")
		}
		record, found, err := group.Equipment.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色穿戴装备失败: %w", err)
		}
		if found {
			snapshot.equipment = dnfrepo.CloneEquipment(record)
			snapshot.hasEquipment = true
		}
	}

	if selected["skills"] {
		if group.Skill == nil {
			return snapshot, errors.New("技能仓储未就绪，无法复制技能")
		}
		record, found, err := group.Skill.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色技能失败: %w", err)
		}
		if found {
			snapshot.skill = dnfrepo.CloneSkill(record)
			snapshot.hasSkill = true
		}
		// 技能指令自定义（op331）：scope 由角色号派生（skillcmd/commands.go:29-32）。
		if group.Settings == nil {
			return snapshot, errors.New("设置仓储未就绪，无法复制技能指令")
		}
		commands, commandsFound, commandsErr := group.Settings.Load(ctx, dnfskillcmd.SkillCommandScope(sourceID))
		if commandsErr != nil {
			return snapshot, fmt.Errorf("读取源角色技能指令失败: %w", commandsErr)
		}
		if commandsFound {
			snapshot.skillCommands = dnfrepo.CloneSettings(commands)
			snapshot.hasSkillCommands = true
		}
	}

	if selected["quests"] {
		if group.Quest == nil {
			return snapshot, errors.New("任务仓储未就绪，无法复制任务")
		}
		record, found, err := group.Quest.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色任务失败: %w", err)
		}
		if found {
			snapshot.quest = dnfrepo.CloneQuest(record)
			snapshot.hasQuest = true
		}
	}

	if selected["pets"] {
		if group.Pet == nil {
			return snapshot, errors.New("宠物仓储未就绪，无法复制宠物")
		}
		record, found, err := group.Pet.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色宠物失败: %w", err)
		}
		if found {
			snapshot.pet = dnfrepo.ClonePet(record)
			snapshot.hasPet = true
		}
	}

	if selected["dungeon"] {
		if group.DungeonPermission == nil {
			return snapshot, errors.New("副本权限仓储未就绪，无法复制地图难度")
		}
		record, found, err := group.DungeonPermission.Load(ctx, sourceID)
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色副本状态失败: %w", err)
		}
		if found {
			snapshot.dungeon = dnfrepo.CloneDungeonPermission(record)
			snapshot.hasDungeon = true
		}
	}

	if selected["containerState"] {
		if group.Settings == nil {
			return snapshot, errors.New("设置仓储未就绪，无法复制背包扩容状态")
		}
		record, found, err := group.Settings.Load(ctx, dnfrepo.CharacterContainerStateScope(sourceID))
		if err != nil {
			return snapshot, fmt.Errorf("读取源角色容器状态失败: %w", err)
		}
		if found {
			snapshot.containerState = dnfrepo.CloneSettings(record)
			snapshot.hasContainerState = true
		}
	}
	return snapshot, nil
}

// cloneSlotIsAccountShared 判断一条背包槽键是否属于**账号共享格**（list 0 的 354..365）。
//
// 键格式 `list:slot` 来自 repository.AccountSharedInventorySlotKey
// （account_inventory_repository.go:48-50）与 inventory/owner.go:2899-2901 的 slotKey；
// 判定本身调用仓储层的 IsAccountSharedInventorySlot（同文件 :41-46），不自己写范围。
func cloneSlotIsAccountShared(key string) bool {
	listRaw, slotRaw, ok := strings.Cut(strings.TrimSpace(key), ":")
	if !ok {
		return false
	}
	listValue, err := strconv.Atoi(strings.TrimSpace(listRaw))
	if err != nil || listValue < 0 || listValue > 255 {
		return false
	}
	slotValue, err := strconv.Atoi(strings.TrimSpace(slotRaw))
	if err != nil || slotValue < -32768 || slotValue > 32767 {
		return false
	}
	return dnfrepo.IsAccountSharedInventorySlot(byte(listValue), int16(slotValue))
}

// cloneNewCharacterRecord 由源记录派生新角色的基础行。
//
// A21 对照：CloneCharacterRow（CharacterClone.cs:298-333）。重写的字段与理由：
//
//	character_id    新号（A21 :305 去掉该列由数据库分配）
//	account_id      目标账号（A21 :315-316）
//	name            新名字（A21 :317-318）
//	slot            最小空闲槽（A21 :323-324）
//	created_at/updated_at  now（A21 :327-328）
//	stats[delete_flag] = 0  新角色必须可见（A21 :321-322）
//
// 90CN 特有的两条（A21 没有角色卡表）：
//
//	stats["roster_object_id"] 与 roster.Entry.ObjectID 都改写成**新角色号**。
//	依据：dnfbridge/character_created_defaults.go:29-37 的 rosterObjectID 在
//	roster_object_id 缺省时回落到数字角色号，说明两者同义；:5-19 的
//	defaultNoPackCharacterRoster(objectID) 也把 ObjectID 当角色号用。不改写会让
//	新角色卡顶着源角色的对象号。
//
// 其余字段（等级/职业/数值/成长段/角色卡其余字段/时间戳以外的元数据）一律逐字节复制
// —— 这正是"复制的定义"，A21 :309-330 也是"整行照抄 + 少数列改写"。
func cloneNewCharacterRecord(
	source dnfrepo.CharacterRecord,
	targetAccountID string,
	newName string,
	slot int,
	newPanelID int,
	now time.Time,
) dnfrepo.CharacterRecord {
	// 深拷贝：Stats/Roster 若与源记录共享，写新角色就会污染源角色（或反之）。
	record := dnfrepo.CloneCharacter(source)
	record.CharacterID = strconv.Itoa(newPanelID)
	record.AccountID = targetAccountID
	record.Name = newName
	record.Slot = slot
	record.CreatedAt = now
	record.UpdatedAt = now
	if record.Stats == nil {
		record.Stats = make(map[string]int64)
	}
	record.Stats["delete_flag"] = 0
	record.Stats["roster_object_id"] = int64(newPanelID)
	record.Roster.Entry.ObjectID = int64(newPanelID)
	return record
}

// cloneWriteCharacter 在**一个事务**里写完新角色的全部选定范围。
//
// 事务由 CharacterCreate 提供（repository.CharacterCreationUnitOfWork，
// transaction.go:32-34）：MySQL 实现 newMySQLGroupFromRouter（mysql_transaction.go:70-96）
// 给出一组共用同一个 *sql.Tx 的仓储，与 dnfbridge 建号/修补走的是同一条边界
// （characterdata/creation.go:49-141）。任何一步返回错误，整笔回滚，源角色不受影响。
func cloneWriteCharacter(
	ctx context.Context,
	tx dnfrepo.Group,
	source dnfrepo.CharacterRecord,
	targetAccountID string,
	newName string,
	slot int,
	newPanelID int,
	selected map[string]bool,
	snapshot cloneCharacterSnapshot,
	now time.Time,
) error {
	if tx.Character == nil {
		return errors.New("事务作用域内没有角色仓储")
	}
	newCharacterID := strconv.Itoa(newPanelID)
	if err := dnfrepo.CreateCharacter(ctx, tx.Character, cloneNewCharacterRecord(
		source, targetAccountID, newName, slot, newPanelID, now,
	)); err != nil {
		return fmt.Errorf("写入新角色基础行失败: %w", err)
	}

	if selected["inventory"] || selected["personalCargo"] {
		if snapshot.hasInventory {
			if tx.Inventory == nil {
				return errors.New("事务作用域内没有背包仓储")
			}
			record := dnfrepo.CloneInventory(snapshot.inventory)
			record.CharacterID = newCharacterID
			record.UpdatedAt = now
			fields := make([]dnfrepo.InventoryField, 0, 2)
			if selected["inventory"] {
				fields = append(fields, dnfrepo.InventoryFieldSlots)
			}
			if selected["personalCargo"] {
				fields = append(fields, dnfrepo.InventoryFieldWarehouse)
			}
			if len(fields) > 0 {
				if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, record, fields...); err != nil {
					return fmt.Errorf("复制背包失败: %w", err)
				}
			}
		}
	}

	if selected["equipment"] && snapshot.hasEquipment {
		if tx.Equipment == nil {
			return errors.New("事务作用域内没有穿戴装备仓储")
		}
		record := dnfrepo.CloneEquipment(snapshot.equipment)
		record.CharacterID = newCharacterID
		record.UpdatedAt = now
		if err := tx.Equipment.Save(ctx, record); err != nil {
			return fmt.Errorf("复制穿戴装备失败: %w", err)
		}
	}

	if selected["skills"] {
		if snapshot.hasSkill {
			if tx.Skill == nil {
				return errors.New("事务作用域内没有技能仓储")
			}
			record := dnfrepo.CloneSkill(snapshot.skill)
			record.CharacterID = newCharacterID
			record.UpdatedAt = now
			if err := tx.Skill.Save(ctx, record); err != nil {
				return fmt.Errorf("复制技能失败: %w", err)
			}
		}
		if snapshot.hasSkillCommands {
			if tx.Settings == nil {
				return errors.New("事务作用域内没有设置仓储")
			}
			record := dnfrepo.CloneSettings(snapshot.skillCommands)
			record.Scope = dnfskillcmd.SkillCommandScope(newCharacterID)
			record.UpdatedAt = now
			if err := tx.Settings.Save(ctx, record); err != nil {
				return fmt.Errorf("复制技能指令失败: %w", err)
			}
		}
	}

	if selected["quests"] && snapshot.hasQuest {
		if tx.Quest == nil {
			return errors.New("事务作用域内没有任务仓储")
		}
		record := dnfrepo.CloneQuest(snapshot.quest)
		record.CharacterID = newCharacterID
		record.UpdatedAt = now
		if err := tx.Quest.Save(ctx, record); err != nil {
			return fmt.Errorf("复制任务失败: %w", err)
		}
	}

	if selected["pets"] && snapshot.hasPet {
		if tx.Pet == nil {
			return errors.New("事务作用域内没有宠物仓储")
		}
		// 差异 D4：宠物条目键是角色内字符串（dnf_pet_entries PK=(character_id,pet_key)），
		// 逐字节复制即正确，不需要（也不允许）另造 UID。
		record := dnfrepo.ClonePet(snapshot.pet)
		record.CharacterID = newCharacterID
		record.UpdatedAt = now
		if err := tx.Pet.Save(ctx, record); err != nil {
			return fmt.Errorf("复制宠物失败: %w", err)
		}
	}

	if selected["containerState"] && snapshot.hasContainerState {
		if tx.Settings == nil {
			return errors.New("事务作用域内没有设置仓储")
		}
		record := dnfrepo.CloneSettings(snapshot.containerState)
		record.Scope = dnfrepo.CharacterContainerStateScope(newCharacterID)
		record.UpdatedAt = now
		if err := tx.Settings.Save(ctx, record); err != nil {
			return fmt.Errorf("复制背包/仓库扩容状态失败: %w", err)
		}
	}

	if selected["dungeon"] && snapshot.hasDungeon && len(snapshot.dungeon.Entries) > 0 {
		// 副本权限仓储只有 UpsertMax（dungeon_permission_repository.go:22-25），
		// 没有整记录 Save，所以按条写；写入顺序保持 Load 的 sort_order 顺序
		// （mysql_dungeon_permission.go:31-33 的 ORDER BY sort_order, dungeon_id）。
		if tx.DungeonPermission == nil {
			// 内存实现的事务作用域里没有这个仓储（memory_transaction.go:220-253），
			// MySQL 实现里有（mysql_group.go:113）。宁可整体失败，也不做
			// "事务外补写"——那会让复制在失败时留下半个角色。
			return errors.New("复制地图难度/副本状态需要事务作用域内的副本权限仓储（当前仓储组未提供，本次未做任何写入）")
		}
		for _, entry := range snapshot.dungeon.Entries {
			if entry.DungeonID == 0 || entry.ClearState == 0 {
				continue
			}
			if _, _, err := tx.DungeonPermission.UpsertMax(ctx, newCharacterID, entry.DungeonID, entry.ClearState); err != nil {
				return fmt.Errorf("复制副本状态 dungeon=%d 失败: %w", entry.DungeonID, err)
			}
		}
	}

	// ---- 事务内回读校验（A21 GmService.CharacterCloneSafety.cs:104-147）----
	// 1) 新角色基础行必须存在且 account_id/slot/name 与请求一致（A21 :109-118）；
	// 2) 目标账号该槽位上必须**恰好一个**可见角色（A21 :120-131 的唯一性冲突检查）。
	stored, found, loadErr := tx.Character.Load(ctx, newCharacterID)
	if loadErr != nil {
		return fmt.Errorf("复制后的角色基础行校验失败: %w", loadErr)
	}
	if !found {
		return errors.New("复制后的角色基础行校验失败: 回读不到新角色")
	}
	if strings.TrimSpace(stored.AccountID) != targetAccountID {
		return fmt.Errorf("复制后的角色基础行校验失败: account_id=%q", stored.AccountID)
	}
	if stored.Slot != slot {
		return fmt.Errorf("复制后的角色基础行校验失败: slot=%d（期望 %d）", stored.Slot, slot)
	}
	if stored.Name != newName {
		return fmt.Errorf("复制后的角色基础行校验失败: name=%q（期望 %q）", stored.Name, newName)
	}
	accountRecords, listErr := tx.Character.ListByAccount(ctx, targetAccountID, cloneDefaultSlotLimit)
	if listErr != nil {
		return fmt.Errorf("复制后的目标账号槽位校验失败: %w", listErr)
	}
	occupants := 0
	for _, record := range accountRecords {
		if record.Slot == slot && statInt(record, "delete_flag") == 0 {
			occupants++
		}
	}
	if occupants != 1 {
		return fmt.Errorf("复制后的目标账号槽位校验失败: slot=%d 上有 %d 个可见角色", slot, occupants)
	}
	return nil
}
