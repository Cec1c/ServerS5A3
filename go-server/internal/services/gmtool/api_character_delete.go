package gmtool

// C：删除角色（POST /api/characters/{id}/delete）。
//
// # 前端契约（wwwroot/js/character.js:141-177，逐字段对齐）
//
//	body     {"confirmText":"删除角色"}（前端在 prompt 里要求逐字输入"删除角色"）
//	响应     r.name（toast 里回显的角色名）与 r.characterId
//	前端在调用前自己弹过两次确认，并提醒操作者"先在客户端切到别的角色"。
//
// # 删除模式：默认**硬删除（物理 DELETE，单事务）**，开关可退回软删除
//
// 开关：`DNFBRIDGE_GM_CHARACTER_DELETE_MODE`
//
//	未设置 / 空 / `hard` / 任何其它值 = **硬删除**（默认）
//	`soft` / `legacy`                  = 旧行为（软删除：delete_flag=1 + 让出槽位）
//
// 硬删除为什么必须自己拼 SQL：仓储层**没有任何删除路径** —— 最小的 `db.Store[T]`
// 只有 Load/Save（internal/platform/db/store.go:27-30），`CharacterRepository`
// （repository/character_repository.go:27-32）只有 Save/ListByAccount/FindIDByName/NextNumericID。
// 表与表之间也**没有外键、没有级联**（全仓 mysql 包 grep 不到 `FOREIGN KEY`/`ON DELETE`，
// 只有 legacy_schema.go:217 在解析旧库 DDL 时跳过它们），所以"删干净"只能逐表删。
//
// 表清单**不写死前缀、也不凭记忆**：本文件在事务里读 information_schema，
// 按**列名**发现该角色在每张表里的落点（`character_id` / `sender_character_id` /
// `recipient_character_id` / `scope='character:<id>[:...]'` / legacy 明细表的
// `item_uid`、`audit_id` 子查询），再逐表 DELETE + 逐表 `SELECT COUNT(*)` 复核。
// 依据（与 DDL 逐表核对过）：
//
//	dnf_characters(PK character_id) / dnf_character_stats / dnf_character_locations /
//	dnf_character_rosters / dnf_character_roster_equipment / dnf_character_roster_lists /
//	dnf_inventories / dnf_inventory_items / dnf_inventory_item_extra /
//	dnf_equipments / dnf_equipment_entries / dnf_equipment_entry_extra /
//	dnf_pets / dnf_pet_entries / dnf_pet_entry_extra / dnf_pet_clear_tokens /
//	dnf_pet_artifacts / dnf_pet_artifact_extra / dnf_quests / dnf_quest_states /
//	dnf_quest_state_extra / dnf_skills / dnf_skill_states / dnf_skill_layouts /
//	dnf_skill_cooldowns / dnf_dungeon_permissions / dnf_mailboxes / dnf_mails /
//	dnf_mail_metadata / dnf_mail_attachments / dnf_mail_attachment_extra /
//	dnf_legacy_character_items
//	  （以上全部来自 repository/mysql/schema.go:223-296 与 schema_relational.go:39-161，
//	   除 dnf_characters 自己外**都带 character_id**）
//	dnf_settings / dnf_setting_values  → 按 scope 前缀删除（见下）
//	dnf_legacy_character_item_extra    → 只有 item_uid，用 dnf_legacy_character_items 子查询
//	dnf_legacy_item_audit_payload_values → 只有 audit_id，用 dnf_legacy_item_audit_log 子查询
//	dnf_mails 的 sender_character_id / recipient_character_id → 防悬空引用，一并清理
//
// **角色状态也可能挂在 settings 的 scope 键上**，所以按前缀整体清：
// `character:<id>:container_state`（repository/container_state.go:28-30）、
// `:skill_commands`（skillcmd/commands.go:13,31）、`:character_option` / `:hotkey_config`
// （dnfbridge/game_option_settings.go:116,120）、`:init_bodies` / `:hotkeys`
// （dnfbridge/initial_defaults.go:185,205）、`:skill_switch_inventory`
// （dnfbridge/skill_switch_inventory.go:68，Buff 强化登记）、以及
// skillswitch/store.go:51 的 `character:<id>` + 任意 scopeSuffix。
// 谓词是 `scope = 'character:<id>' OR scope LIKE 'character:<id>:%'`；角色号是十进制
// （loadCharacter 就是按十进制查的，deps.go:357-370），因此 LIKE 里不会出现
// `%`/`_` 通配符污染。
//
// 事务语义（全部在**同一个 `sql.Tx`** 里）：
//
//	1. plan   ：information_schema 发现 + 逐表构造 (表, 谓词, 参数)；
//	2. delete ：逐表 DELETE，记录真实条数；
//	3. verify ：逐表 `SELECT COUNT(*)`，任何一张表还有残留 ⇒ **整体回滚** + 具名报出表名；
//	4. commit ：全部干净才提交。
//	额外一条 fail-closed：`dnf_characters` 自己那一行必须**真的删掉 1 行**，否则说明
//	GM 的 DSN 指向的库里没有这个角色（表都在别的库）⇒ 回滚并具名报错 ——
//	绝不出现"删了 0 行却报告成功"的假成功。
//
// 前端那句"已彻底删除"的文案在 wwwroot/js/character.js:162（本批不许改前端），
// 现在它**是实话**：响应里的 deleteMode="hard" 与 tables[] 就是逐表条数。
//
// # 与上游的逐段对拍（vendor Services/GmService.Characters.cs:652-845）
//
//	:652-655  confirmText 必须逐字等于"删除角色"（本仓同，且零写入）
//	:689-716  删除前置门禁：进行中的佣兵出战、未投递的佣兵奖励邮件 —— **本仓没有对应系统**：
//	          90CN 里没有任何 mercenary/outbox 持久化表（全仓只有 dnfbridge 的报文级
//	          "不支持"拒绝，scene_fixtures.go:706），邮件是 `AppendSystemMail` 同步写进
//	          dnf_mail 的（repository/mail_system.go:30），不存在"未送达"状态。
//	          这一条**没有移植**，因为没有可查的数据（绝不凭空拦人或凭空放行）。
//	:718-819  逐表物理删除 + get_userinfo_template 的种子角色改写 —— 本仓没有那些表
//	          （那是 A21 自己的 sqlite schema）；表的枚举改为读 information_schema。
//	:817-821  删 0 行 = 明确失败（本仓同：dnf_characters 那一行必须真删掉 1 行）
//	:847-875  ResolveReplacementSeedCharacterId：替"种子角色"找替补 —— 本仓没有
//	          get_userinfo_template 这种表，不需要。
//
// # 软删除档（开关 = soft 时）保留的既有语义
//
//	delete_flag=1（schema 的 uk_dnf_characters_account_slot_active 就是这么设计的）
//	+ 墓碑行让出选人槽位（见 chardelTombstoneSlot）。所有读面都过滤 delete_flag：
//	ListByAccount（mysql_char.go:490、memory_char.go:127-129）、FindIDByName
//	（mysql_char.go:627）、槽位交换（mysql_char.go:558）、面板自己的列表（deps.go:344-347），
//	选角链路只从 ListByAccount 选人（dnfbridge/game_select_resolve.go:29-34）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// chardelConfirmText 是必须逐字给出的确认文本
	// （vendor GmService.Characters.cs:654、wwwroot/js/character.js:148-152）。
	chardelConfirmText = "删除角色"

	// chardelDeletedFlag 是"已删除"标记的值。**恒为 1**：所有读面用的都是
	// `delete_flag = 0`（见文件头第 3 条），所以 1 既符合 schema 语义，
	// 也不会被任何"等于 0 才算活着"的读法误判。
	chardelDeletedFlag = 1
)

var (
	chardelErrConfirmText = errors.New("确认文本不正确，请输入：删除角色")
	// chardelErrCharacterMissing 是"角色不存在"这条失败在错误分类表里的条目。
	// 它由共用的 characterRequest（api_characters.go:1028-1048 → deps.go:357-370）
	// 在解析阶段就写出响应，所以本文件不产生它 —— 留着是为了让分类表覆盖
	// 上游 DeleteCharacterPermanently 的全部失败模式（与 api_items_delete.go:126-131
	// 保留 errItemRemoveCharacterMissing 的同一理由）。
	chardelErrCharacterMissing         = errors.New("角色不存在")
	chardelErrAlreadyDeleted           = errors.New("该角色已经是删除状态（delete_flag != 0），无需重复删除")
	chardelErrOnline                   = errors.New("角色正在游戏内在线，拒绝删除")
	chardelErrCharacterRepo            = errors.New("角色仓储未就绪")
	chardelErrAccountOnCharacter       = errors.New("角色记录缺少归属账号，拒绝删除（无法确认它属于哪个账号）")
	chardelErrTombstoneSlotUnavailable = errors.New("面板角色号非正数，无法为墓碑行腾出槽位")
)

// chardelRequest 是请求体：只有 confirmText 一个字段
// （vendor DeleteCharacterRequest / wwwroot/js/character.js:161）。
type chardelRequest struct {
	ConfirmText string `json:"confirmText"`
}

// chardelServiceUnavailable 在角色仓储缺失时统一出口（"能力没接上"是一种明确失败，
// 绝不静默成功 —— 与 repository/account_repository.go:36-40 的 ErrAccountListUnavailable 同立场）。
func chardelServiceUnavailable(w http.ResponseWriter) {
	writeFailure(w, chardelErrCharacterRepo.Error())
}

func (s *Server) handleCharacterDelete(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[chardelRequest](w, r)
	if !ok {
		return
	}
	// 顺序与上游 :652-655 一致：先验确认文本，再做任何其它事（零写入）。
	if strings.TrimSpace(body.ConfirmText) != chardelConfirmText {
		s.logEvent("gm-character-delete-rejected",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", character.AccountID,
			"reason", "confirm_text_mismatch",
			"result", "rejected")
		writeFailure(w, chardelErrConfirmText.Error()+"（未做任何写入）。")
		return
	}
	s.chardelApply(w, ctx, group, panelID, character)
}

// chardelApply 是"角色已解析、确认文本已校验"之后的全部行为，单独抽出来是为了让单测
// 能绕开 s.db（与 api_accounts.go:201-213 的 applyAccountCurrency 同一拆分理由）。
func (s *Server) chardelApply(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	panelID int,
	character dnfrepo.CharacterRecord,
) {
	if group.Character == nil {
		s.logEvent("gm-character-delete-failed",
			"panel_id", panelID, "character_id", character.CharacterID,
			"reason", "character_repository_missing", "result", "failed")
		chardelServiceUnavailable(w)
		return
	}
	accountID := strings.TrimSpace(character.AccountID)
	if accountID == "" {
		// 没有归属账号的行一旦隐藏就再也说不清属于谁，拒绝比删掉安全。
		s.logEvent("gm-character-delete-rejected",
			"panel_id", panelID, "character_id", character.CharacterID,
			"reason", "character_account_missing", "result", "rejected")
		writeFailure(w, chardelErrAccountOnCharacter.Error()+"（未做任何写入）。")
		return
	}
	// 在线守卫：**这里不看 cfg.RequireOffline**。理由不是风格而是数据安全：
	// 在线会话把整条 CharacterRecord 放在内存里，任何一次存档（
	// SaveCharacterFields(..., CharacterFieldBase|CharacterFieldStats)）都会把
	// 它内存里的记录写回来 —— 软删除档下等于把刚删掉的角色原地复活（甚至与
	// "新角色占了同一槽位"的行撞唯一键），硬删除档下会**重建**一行只带 base/stats 的
	// 残缺角色行。删除是不可逆操作，探针说在线就必须拒绝。
	// 探针没注入时安全降级（放行 + 具名日志），与 online_guard.go:27-29 的既定降级口径一致。
	if status := s.characterOnline(chardelProbeID(panelID)); status.known {
		if status.online {
			s.logEvent("gm-character-delete-rejected",
				"panel_id", panelID, "character_id", character.CharacterID, "account_id", accountID,
				"reason", "character_online", "result", "rejected")
			writeFailure(w, fmt.Sprintf(
				"%s：请先在客户端切到别的角色（退出该角色/回到选人界面）再删除（未做任何写入）。",
				chardelErrOnline.Error()))
			return
		}
	} else {
		s.logEvent("gm-character-delete-probe-missing",
			"panel_id", panelID, "character_id", character.CharacterID,
			"reason", "probe_not_injected_or_out_of_range")
	}

	if s.chardelHardMode() {
		s.chardelApplyHard(w, ctx, panelID, character)
		return
	}
	s.chardelApplySoft(w, ctx, group, panelID, character)
}

// ---------------------------------------------------------------------------
// 软删除档（开关 = soft）
// ---------------------------------------------------------------------------

func (s *Server) chardelApplySoft(
	w http.ResponseWriter,
	ctx context.Context,
	group dnfrepo.Group,
	panelID int,
	character dnfrepo.CharacterRecord,
) {
	accountID := strings.TrimSpace(character.AccountID)
	if statInt(character, "delete_flag") != 0 {
		s.logEvent("gm-character-delete-rejected",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", accountID,
			"reason", "already_deleted", "result", "rejected")
		writeFailure(w, chardelErrAlreadyDeleted.Error()+"。")
		return
	}
	tombstoneSlot, slotErr := chardelTombstoneSlot(panelID)
	if slotErr != nil {
		s.logEvent("gm-character-delete-rejected",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", accountID,
			"reason", "tombstone_slot_unavailable", "error", slotErr.Error(), "result", "rejected")
		writeFailure(w, slotErr.Error()+"（未做任何写入）。")
		return
	}

	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64, 1)
	}
	previousSlot := updated.Slot
	updated.Stats["delete_flag"] = chardelDeletedFlag
	// 见 chardelTombstoneSlot：把墓碑行移出选人槽位（槽位号用负数，理由写在函数上）。
	updated.Slot = tombstoneSlot
	updated.UpdatedAt = time.Now().UTC()
	// 与既有写路径同一组字段（deps.go:384-393 的 saveCharacterStats 用的就是
	// CharacterFieldBase + CharacterFieldStats）：一次写回即完成软删除。
	if saveErr := dnfrepo.SaveCharacterFields(ctx, group.Character, updated,
		dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); saveErr != nil {
		s.logEvent("gm-character-delete-failed",
			"panel_id", panelID, "character_id", character.CharacterID, "account_id", accountID,
			"reason", "write_failed", "error", saveErr.Error(), "result", "failed")
		writeFailure(w, "写入角色删除标记失败: "+saveErr.Error())
		return
	}

	// 复核：写完立刻读回来确认它真的不在选人列表里（读面走 ListByAccount，
	// 与游戏选角用的是同一个方法）。读不回来不算成功 —— 绝不"看起来删了"。
	remaining, listErr := s.charactersForAccount(ctx, group, accountID)
	visible := false
	if listErr == nil {
		for _, record := range remaining {
			if strings.TrimSpace(record.CharacterID) == strings.TrimSpace(character.CharacterID) {
				visible = true
				break
			}
		}
	}

	s.logEvent("gm-character-delete",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", accountID,
		"name", character.Name,
		"delete_mode", "soft",
		"delete_flag", chardelDeletedFlag,
		"previous_slot", previousSlot,
		"tombstone_slot", tombstoneSlot,
		"rows_deleted", 0,
		"visible_after", visible,
		"roster_check", chardelRosterCheckLabel(listErr),
		"result", "applied")
	writeOK(w, map[string]any{
		"characterId":     panelIDOfCharacter(character),
		"accountId":       panelIDFromAccountID(accountID),
		"accountName":     accountID,
		"name":            character.Name,
		"deleteMode":      "soft",
		"deleteFlag":      chardelDeletedFlag,
		"previousSlot":    previousSlot,
		"tombstoneSlot":   tombstoneSlot,
		"releasedSlot":    previousSlot,
		"rowsDeleted":     0,
		"dataRetained":    true,
		"visibleInRoster": visible,
		"rosterCheck":     chardelRosterCheckLabel(listErr),
		"notification":    "character_hidden_from_roster",
		"hint": "90CN 的 dnf_characters 只有 delete_flag 软删除路径（仓储层没有物理删除方法，" +
			"各表之间也没有外键级联）：角色已从选人列表与角色名索引中移除、角色名可立即被新角色复用，" +
			"而背包/任务/邮件等关联行**全部保留**（同一角色号若将来要恢复，只需把 delete_flag 置回 0 并回填槽位）。",
	})
}

// ---------------------------------------------------------------------------
// 硬删除（默认档）：一个事务里逐表删干净 + 逐表残留复核
// ---------------------------------------------------------------------------

// chardelModeEnv 是删除模式的开关名。
//
//	未设置 / 空 / `hard` / 其它任何值 = 硬删除（默认）
//	`soft` / `legacy`                  = 软删除（delete_flag=1 + 让出槽位）
const chardelModeEnv = "DNFBRIDGE_GM_CHARACTER_DELETE_MODE"

// chardelHardModeFromEnv 解析开关。白名单只有两个字面量用于**退回**旧行为；
// 其余一切（含空）都走硬删除 —— 与"操作者明确要求硬删除"这个前提一致。
func chardelHardModeFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "soft", "legacy":
		return false
	default:
		return true
	}
}

// chardelHardMode 读进程环境里的同一个开关（与 online_guard.go:65-73 同一取舍：
// 开关自读环境，不动改动最密集的 dnfbridge/options.go）。
func (s *Server) chardelHardMode() bool {
	return chardelHardModeFromEnv(os.Getenv(chardelModeEnv))
}

var (
	chardelErrDatabaseUnavailable = errors.New("GM 数据库句柄未就绪，无法执行硬删除")
	chardelErrSchemaUnreadable    = errors.New("读取 information_schema 失败，无法确定要清理哪些表")
	chardelErrCharacterRowMissing = errors.New("目标库里没有这个角色的主行，已整体回滚（硬删除拒绝假装成功）")
	chardelErrResidualRows        = errors.New("删除后仍有残留行，已整体回滚")
	chardelErrExecFailed          = errors.New("删除语句执行失败，已整体回滚")
)

// chardelHardTx 是硬删除需要的最小事务面（*sql.Tx 天然满足；测试用假实现注入）。
type chardelHardTx interface {
	Names(ctx context.Context, query string, args ...any) ([][2]string, error)
	Exec(ctx context.Context, query string, args ...any) (int64, error)
	Count(ctx context.Context, query string, args ...any) (int64, error)
	Commit() error
	Rollback() error
}

// chardelHardDB 开一个事务（*sql.DB 天然满足）。
type chardelHardDB interface {
	Begin(ctx context.Context) (chardelHardTx, error)
}

// chardelSQLDB / chardelSQLTx 是生产实现：把 *sql.DB / *sql.Tx 包成本文件的最小面。
type chardelSQLDB struct{ db *sql.DB }

func (d chardelSQLDB) Begin(ctx context.Context) (chardelHardTx, error) {
	if d.db == nil {
		return nil, chardelErrDatabaseUnavailable
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return chardelSQLTx{tx: tx}, nil
}

type chardelSQLTx struct{ tx *sql.Tx }

func (t chardelSQLTx) Names(ctx context.Context, query string, args ...any) ([][2]string, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([][2]string, 0, 64)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		out = append(out, [2]string{table, column})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (t chardelSQLTx) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

func (t chardelSQLTx) Count(ctx context.Context, query string, args ...any) (int64, error) {
	var count int64
	if err := t.tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (t chardelSQLTx) Commit() error   { return t.tx.Commit() }
func (t chardelSQLTx) Rollback() error { return t.tx.Rollback() }

// chardelIsCharacterTable 判断一张表是不是"角色主行"表（承载 (account_id, slot, delete_flag)
// 唯一键的那张）。只认 `characters` 或 `<前缀>_characters` 两种形状：
// `dnf_character_stats`（单数）与 `dnf_characters_backup`（多后缀）都**不算** ——
// 否则"主行必须真删到 1 行"这条 fail-closed 检查会被一张只有别的行的表蒙混过去。
func chardelIsCharacterTable(table string) bool {
	lower := strings.ToLower(strings.TrimSpace(table))
	return lower == "characters" || strings.HasSuffix(lower, "_characters")
}

// chardelHardPredicate 是一条"把这个角色在 t 表里的行删掉"的谓词。
// 同一条谓词同时用于 DELETE 与残留 COUNT，避免两处写法漂移。
type chardelHardPredicate struct {
	Table     string
	Key       string // 审计用的逻辑键：character_id / scope / sender_character_id / legacy_item_uid …
	Predicate string
	Args      []any
}

// chardelHardResult 是一次硬删除的如实回报。
type chardelHardResult struct {
	Deletes []chardelHardPredicate
	Counts  []int64
}

func (r chardelHardResult) total() int64 {
	var total int64
	for _, count := range r.Counts {
		total += count
	}
	return total
}

// chardelDiscoverQuery 一次性把本文件关心的所有列读出来（表名 + 列名）。
// 不写死库前缀：表名直接用 information_schema 里的事实值。
const chardelDiscoverQuery = "SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS " +
	"WHERE TABLE_SCHEMA = DATABASE() AND COLUMN_NAME IN " +
	"('character_id','scope','sender_character_id','recipient_character_id','item_uid','audit_id') " +
	"ORDER BY TABLE_NAME, COLUMN_NAME"

// chardelBuildPlan 从 information_schema 的 (表, 列) 事实集构造删除计划。
//
// 顺序要紧：
//  1. legacy 明细表（只有 item_uid / audit_id）必须**先**删 —— 它们的子查询依赖父表还在；
//  2. scope 键（settings/setting_values）与邮件发件人/收件人引用；
//  3. 其它带 character_id 的表；
//  4. `*_characters` 主行最后删（它承载 (account_id, slot, delete_flag) 唯一键）。
func chardelBuildPlan(columns [][2]string, characterID string) []chardelHardPredicate {
	hasColumn := make(map[string]map[string]bool, len(columns))
	for _, entry := range columns {
		table, column := entry[0], entry[1]
		if hasColumn[table] == nil {
			hasColumn[table] = make(map[string]bool, 4)
		}
		hasColumn[table][column] = true
	}
	tables := make([]string, 0, len(hasColumn))
	for table := range hasColumn {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	plan := make([]chardelHardPredicate, 0, len(tables)+4)
	byCharacterID := make([]string, 0, len(tables))
	characterRows := make([]string, 0, 1)
	// 1) legacy 明细表：按父表子查询（先于父表删除）。
	for _, table := range tables {
		columnsOfTable := hasColumn[table]
		if columnsOfTable["character_id"] {
			continue
		}
		for _, link := range []struct{ child, parentColumn string }{
			{child: "item_uid", parentColumn: "item_uid"},
			{child: "audit_id", parentColumn: "audit_id"},
		} {
			if !columnsOfTable[link.child] {
				continue
			}
			parents := make([]string, 0, 1)
			for _, candidate := range tables {
				if candidate == table {
					continue
				}
				if hasColumn[candidate]["character_id"] && hasColumn[candidate][link.parentColumn] {
					parents = append(parents, candidate)
				}
			}
			for _, parent := range parents {
				plan = append(plan, chardelHardPredicate{
					Table: table,
					Key:   "character_" + link.child + "_via_" + parent,
					Predicate: quoteIdentifier(link.child) + " IN (SELECT " + quoteIdentifier(link.child) +
						" FROM " + quoteIdentifier(parent) + " WHERE " + quoteIdentifier("character_id") + " = ?)",
					Args: []any{characterID},
				})
			}
		}
	}
	// 2) scope 键：settings / setting_values（以及任何名字里带 setting 的 scope 表）。
	scopePrefix := "character:" + characterID
	for _, table := range tables {
		if !hasColumn[table]["scope"] || !strings.Contains(strings.ToLower(table), "setting") {
			continue
		}
		plan = append(plan, chardelHardPredicate{
			Table:     table,
			Key:       "scope",
			Predicate: quoteIdentifier("scope") + " = ? OR " + quoteIdentifier("scope") + " LIKE ?",
			Args:      []any{scopePrefix, scopePrefix + ":%"},
		})
	}
	// 3) 邮件里的发件人/收件人引用（防悬空引用）+ 其它 character_id 表。
	for _, table := range tables {
		columnsOfTable := hasColumn[table]
		if columnsOfTable["sender_character_id"] {
			plan = append(plan, chardelHardPredicate{
				Table:     table,
				Key:       "sender_character_id",
				Predicate: quoteIdentifier("sender_character_id") + " = ?",
				Args:      []any{characterID},
			})
		}
		if columnsOfTable["recipient_character_id"] {
			plan = append(plan, chardelHardPredicate{
				Table:     table,
				Key:       "recipient_character_id",
				Predicate: quoteIdentifier("recipient_character_id") + " = ?",
				Args:      []any{characterID},
			})
		}
		if !columnsOfTable["character_id"] {
			continue
		}
		if chardelIsCharacterTable(table) {
			characterRows = append(characterRows, table)
			continue
		}
		byCharacterID = append(byCharacterID, table)
	}
	for _, table := range byCharacterID {
		plan = append(plan, chardelHardPredicate{
			Table:     table,
			Key:       "character_id",
			Predicate: quoteIdentifier("character_id") + " = ?",
			Args:      []any{characterID},
		})
	}
	for _, table := range characterRows {
		plan = append(plan, chardelHardPredicate{
			Table:     table,
			Key:       "character_id",
			Predicate: quoteIdentifier("character_id") + " = ?",
			Args:      []any{characterID},
		})
	}
	return plan
}

// chardelApplyHard 是硬删除的唯一实现：单事务、逐表删、逐表复核、任何异常整体回滚。
func (s *Server) chardelApplyHard(
	w http.ResponseWriter,
	ctx context.Context,
	panelID int,
	character dnfrepo.CharacterRecord,
) {
	s.chardelApplyHardWith(w, ctx, panelID, character, chardelSQLDB{db: s.db})
}

// chardelApplyHardWith 把数据库面作为参数传入，好让单测用假事务走完**整条响应路径**
// （生产调用点就是上面那一行，注入的永远是 s.db）。
func (s *Server) chardelApplyHardWith(
	w http.ResponseWriter,
	ctx context.Context,
	panelID int,
	character dnfrepo.CharacterRecord,
	database chardelHardDB,
) {
	result, err := s.chardelHardDelete(ctx, database, character.CharacterID)
	if err != nil {
		reason := chardelHardReasonFromError(err)
		s.logEvent("gm-character-delete-failed",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", character.AccountID,
			"delete_mode", "hard",
			"reason", reason,
			"error", err.Error(),
			"result", "failed")
		writeFailure(w, "删除角色失败（已整体回滚，库里没有任何变化）: "+err.Error())
		return
	}
	tables := make([]map[string]any, 0, len(result.Deletes))
	for index, item := range result.Deletes {
		tables = append(tables, map[string]any{
			"table":   item.Table,
			"key":     item.Key,
			"deleted": result.Counts[index],
		})
	}
	s.logEvent("gm-character-delete",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"name", character.Name,
		"delete_mode", "hard",
		"tables", len(result.Deletes),
		"rows_deleted", result.total(),
		"verified", true,
		"result", "applied")
	writeOK(w, map[string]any{
		"characterId":   panelIDOfCharacter(character),
		"accountId":     panelIDFromAccountID(character.AccountID),
		"accountName":   character.AccountID,
		"name":          character.Name,
		"deleteMode":    "hard",
		"tables":        tables,
		"tablesScanned": len(result.Deletes),
		"rowsDeleted":   result.total(),
		"verified":      true,
		"notification":  "character_deleted",
		"hint": "已在**单个事务**里逐表删除该角色的全部关联行，并对每张表做了残留复核（任何一张表还有残留就整体回滚）。" +
			"表清单来自 information_schema（按列名发现），不依赖写死的表名前缀。",
	})
}

// chardelHardDelete 是本文件的硬删除内核：可注入 chardelHardDB（测试用假事务）。
//
// 事务顺序：发现 → 逐表 DELETE → 逐表 COUNT 复核 → dnf_characters 主行必须真删到 1 行 → 提交。
func (s *Server) chardelHardDelete(
	ctx context.Context,
	database chardelHardDB,
	characterID string,
) (chardelHardResult, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return chardelHardResult{}, chardelErrCharacterMissing
	}
	if database == nil {
		return chardelHardResult{}, chardelErrDatabaseUnavailable
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return chardelHardResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	columns, err := tx.Names(ctx, chardelDiscoverQuery)
	if err != nil {
		return chardelHardResult{}, fmt.Errorf("%w: %v", chardelErrSchemaUnreadable, err)
	}
	plan := chardelBuildPlan(columns, characterID)
	if len(plan) == 0 {
		return chardelHardResult{}, fmt.Errorf("%w: information_schema 里没有一张带 character_id 的表", chardelErrSchemaUnreadable)
	}
	result := chardelHardResult{Deletes: plan, Counts: make([]int64, len(plan))}

	characterRowsDeleted := int64(0)
	for index, item := range plan {
		query := "DELETE FROM " + quoteIdentifier(item.Table) + " WHERE " + item.Predicate
		affected, execErr := tx.Exec(ctx, query, item.Args...)
		if execErr != nil {
			return chardelHardResult{}, fmt.Errorf("%w: 表 %s: %v", chardelErrExecFailed, item.Table, execErr)
		}
		result.Counts[index] = affected
		if item.Key == "character_id" && chardelIsCharacterTable(item.Table) {
			characterRowsDeleted += affected
		}
	}
	// fail-closed：主行必须**正好**删到 1 行（角色号是主键）。0 行 = 这个库里没有该角色
	// （GM 的 DSN 指到别的库去了）；>1 行 = 出现了多张"角色主行"表这种本文件不认识的
	// 结构。两种都整体回滚，绝不报告"删了 0 行"的成功。
	if characterRowsDeleted != 1 {
		return chardelHardResult{}, fmt.Errorf("%w: 删除角色主行共影响 %d 行",
			chardelErrCharacterRowMissing, characterRowsDeleted)
	}

	// 残留复核：逐表 SELECT COUNT(*)，任何一张表不为 0 ⇒ 回滚 + 具名。
	residual := make([]string, 0, 4)
	for _, item := range plan {
		query := "SELECT COUNT(*) FROM " + quoteIdentifier(item.Table) + " WHERE " + item.Predicate
		count, countErr := tx.Count(ctx, query, item.Args...)
		if countErr != nil {
			return chardelHardResult{}, fmt.Errorf("%w: 复核表 %s: %v", chardelErrResidualRows, item.Table, countErr)
		}
		if count != 0 {
			residual = append(residual, fmt.Sprintf("%s(%s)=%d", item.Table, item.Key, count))
		}
	}
	if len(residual) > 0 {
		return chardelHardResult{}, fmt.Errorf("%w：%s", chardelErrResidualRows, strings.Join(residual, ", "))
	}
	if err := tx.Commit(); err != nil {
		return chardelHardResult{}, err
	}
	committed = true
	return result, nil
}

// chardelHardReasonFromError 把硬删除失败归类成稳定的 reason 串（日志聚合用）。
func chardelHardReasonFromError(err error) string {
	switch {
	case err == nil:
		return "failed"
	case errors.Is(err, chardelErrDatabaseUnavailable):
		return "database_unavailable"
	case errors.Is(err, chardelErrSchemaUnreadable):
		return "schema_unreadable"
	case errors.Is(err, chardelErrCharacterRowMissing):
		return "character_row_missing"
	case errors.Is(err, chardelErrResidualRows):
		return "residual_rows"
	case errors.Is(err, chardelErrExecFailed):
		return "exec_failed"
	default:
		return "failed"
	}
}

// chardelProbeID 把面板主键换算成对局会话表的 uint16 角色号；
func chardelProbeID(panelID int) uint16 {
	characterID, ok := probeCharacterID(panelID)
	if !ok {
		return 0
	}
	return characterID
}

// chardelTombstoneSlot 计算墓碑行该占的槽位号：**取角色号的负值**（-1 → 槽位 1）。
//
// 为什么必须让出槽位：schema 的唯一键是 `(account_id, slot, delete_flag)`
// （repository/mysql/schema.go:239），它刻意允许"同一槽位一个活人 + 一个墓碑"
// （所以删完能立刻在同槽位建新角色），但**不允许同一槽位有两个墓碑**：
// "删 A → 同槽位建 B → 删 B"会撞 1062 Duplicate entry。而面板最常见的用法
// （复制角色当靶子、清理复制目标，然后重来一次）正好就是这个序列。
//
// 负槽位安全吗？——安全，而且不是新发明的约定：
//   - 选人槽位区间是 [0, DefaultCharacterSlotLimit)（repository/character_repository.go:176-178
//     的 SwapCharacterSlots 就直接拒绝区间外的槽位）；
//   - 所有读面只遍历 `delete_flag = 0` 的行（见文件头第 3 条），墓碑行的槽位号
//     不会被任何列表/排序/选角逻辑看到；
//   - 用负号本身就是本仓既有手法：SwapCharacterSlots 交换时用的临时槽位就是
//     `-1 - slotA - slotB*32`（mysql_char.go:586-595）；
//   - 角色号是主键（唯一），所以 -角色号 天然唯一：删多少次都不会再撞唯一键。
//
// 角色号非正数时**拒绝**（而不是退回原槽位硬写）：那种行本来也无法被本面板寻址
// （loadCharacter 就是按十进制角色号查的，见 deps.go:357-370），不如把话说明白。
func chardelTombstoneSlot(panelID int) (int, error) {
	if panelID <= 0 {
		return 0, chardelErrTombstoneSlotUnavailable
	}
	return -panelID, nil
}

// chardelRosterCheckLabel 把复核结果变成审计字段里稳定的一小段枚举值。
func chardelRosterCheckLabel(err error) string {
	if err != nil {
		return "unavailable"
	}
	return "checked"
}
