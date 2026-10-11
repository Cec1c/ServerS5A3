package gmtool

// 本文件实现 GM 面板「账号数据管理 → 账号备份」的导出与恢复两条路由：
//
//	POST /api/accounts/{id}/backup  导出（= A21 的 ExportAccountBackup）
//	POST /api/accounts/restore      恢复（= A21 的 RestoreAccountBackup）
//
// ---------------------------------------------------------------------------
// 参考实现（只读）与逐段对应关系
// ---------------------------------------------------------------------------
// A21 = `H:\86JP启动器\GM工具搭建包_含源码与发布版\源码-86jp_DfoGmTool-main\Services\GmService.AccountBackup.cs`
//
//	A21:14         AccountBackupVersion（本文件 backupFormatVersion）
//	A21:16-18      标识符白名单正则（本文件 backupIdentifierPattern）
//	A21:20-76      AccountBackupPreferredOrder（父表在前、子表在后；本文件 backupCoreTableSpecs）
//	A21:78-87      AccountBackupRestoreExcludedColumns（本仓**不适用**，理由见下方"代理键"）
//	A21:89-103     可选/忽略表集合（本文件用 dnf_legacy_ 前缀 + 目标库探测代替）
//	A21:105-152    ExportAccountBackup：按账号/角色归属逐表导出、空表跳过、accounts 恒留
//	A21:154-255    RestoreAccountBackup：先校验 -> 再删光该账号 -> 再按备份重建（单事务）
//	A21:257-272    ValidateAccountBackupFile（版本 / 账号 ID / 角色列表 / 重复表）
//	A21:274-348    目标结构归一化 + 必需列 + item_core 校验（本文件 backupValidateDumpForRestore）
//	A21:390-433    LoadAccountCharacterIds / NameValues（本文件 backupLoadCharacterIDs）
//	A21:512-573    BuildAccountBackupPredicate（account_id / character_id 归属谓词）
//	A21:575-641    AppendMailboxRelationDumps / AppendRelatedDump（父表 id -> 子表行）
//	A21:682-766    AccountHasAnyBackupData / ClearAccountBackupData / 删除谓词（子表先删）
//	A21:768-789    FindConflictingBackupCharacterIds（角色 ID 是否被别的账号占用）
//	A21:1023-1060  RestoreAccountBackupTable（逐行 INSERT）
//	A21:1074-1105  BuildInClause / EqualIntSets / 标识符校验与引用
//	A21:1153-1280  值与文件的 JSON 形状（type/integer/real/text/blob + version/exportedAt/accountID/characterIDs/tables）
//
// ---------------------------------------------------------------------------
// 与 A21 的三处**有意差异**（都不改变契约字段名，只改数据来源）
// ---------------------------------------------------------------------------
//  1. 表名与归属列来自本仓真实 schema，而不是"把 SQLite 里所有表都试一遍"：
//     A21 在 sqlite_master 里枚举全部表、按列名猜归属；本仓的表与归属列是已知且固定的
//     （`internal/modules/dnf/repository/mysql/schema.go` 与 `schema_relational.go` 的
//     CREATE TABLE：account_id / character_id 两族），因此核心表用**钉死的清单**
//     （backupCoreTableSpecs，逐张对照 schema_relational.go 的 DDL 写出），
//     `dnf_legacy_*` 镜像表（C# 导入产物、运行时只读，见 csharp_item_schema.sql）
//     才用 information_schema 现场枚举（表数 ~100，钉清单会随 schema 漂移而漏）。
//     绝不做"猜列名"的全库扫描：本库里有 `dnf90_web_accounts`（启动器登录凭据，
//     account_id 是 "dnf:10001" 这样的字符串）——任何泛化的 account_id 扫描都会
//     把它当成游戏账号表，删掉玩家的登录账号。
//  2. 值类型用 MySQL 的列类型（`database/sql` 的 ColumnType.DatabaseTypeName）判定
//     text/blob，并额外用 utf8 合法性兜底；A21 用 SQLite 的动态类型。
//  3. 没有任何代理键重映射：A21 的 character_inventory_items.item_uid /
//     mailbox_attachments.attachment_id 等自增代理键需要现场重排（A21:78-87、
//     A21:791-1009）；本仓被备份的表**全部是自然主键**（character_id/account_id +
//     entry_key/mail_id/...），导出的键原样插回即可，父子关系天然保持一致。
//     唯一的例外是 C# 导入镜像表里的两个自增键（dnf_legacy_character_items.item_uid、
//     dnf_legacy_item_audit_log.audit_id），它们也按导出值原样插回（见 backupInsertDump
//     注释），因此不需要 A21 的两套重映射。
//
// ---------------------------------------------------------------------------
// 备份物理存放在哪里（明确回答"文件/表/JSON blob"）
// ---------------------------------------------------------------------------
// **就是这份 JSON 文档本身，存在操作者的电脑上。** 面板侧的证据链是
// `wwwroot/js/sidebar.js:250-272`（导出：把本路由的响应 JSON 存成
// `账号备份-account_<id>-<时间>.json`）与 `:281-311`（恢复：把文件 POST 回
// `/api/accounts/restore`）。因此：
//
//   - 服务端**不新建表、不写文件、不引入备份目录**（可逆：删掉本文件与三行路由即回到今天）；
//   - 本路由的响应体 = 备份文件内容（多出的 `"success":true` 是本仓统一的成功标记，
//     wwwroot/js/core.js:19-24 要求它），恢复请求体 = 同一个文件原样回传；
//   - 因此"导出的 JSON 与恢复的输入"是同一个格式，往返可读（见测试
//     TestBackupRoundTripRestoresExactlyWhatWasExported）。
//
// 为什么不做服务端留存：A21 也是文件流（操作者自行保存），而"服务端留存"在本仓要
// 新建表或目录（会和 `mysql/schema.go` 的建表/迁移所有权冲突），且面板没有任何
// "列出历史备份"的入口 —— 加了也是取不到的死数据。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// backupFormatVersion 是本服务端产出的备份格式版本（A21:14 的 AccountBackupVersion）。
	// **不接受** A21/86jp 的 v1/v2/v3：那些文件的表名（accounts/characters/mailbox_*）
	// 与本仓 schema 完全不是一套，硬套只会静默丢数据，所以一律具名拒绝（见 backupValidateFile）。
	backupFormatVersion = 1
	// backupRestoreBodyLimit 只作用于 `/api/accounts/restore`：gmtool.go 的全局
	// maxBodyBytes（1 MiB）对备份文件太小（一个大号账号的背包 + 邮件附件轻松超过），
	// 所以这条路由自己读请求体（decodeBody 的 1 MiB 会先失败，故不能复用它）。
	backupRestoreBodyLimit = 64 << 20
	// backupLegacyTablePrefix 是 C# inventory.db 导入镜像表的前缀（schema.go:108
	// `legacy_` + 默认表前缀 `dnf_`）。
	backupLegacyTablePrefix = "dnf_legacy_"
	// backupInChunkSize 是 IN (?,?,...) 每批的占位符上限，避免超大账号把 SQL 撑爆。
	backupInChunkSize = 400
	// backupSQLTimeLayout 是 DATETIME(6) 的字面量形状：驱动 parseTime=true 时
	// DATETIME 会变成 time.Time，写回时必须给出同样的墙钟字符串（无时区转换）。
	backupSQLTimeLayout = "2006-01-02 15:04:05.000000"

	backupValueNull    = "null"
	backupValueInteger = "integer"
	backupValueReal    = "real"
	backupValueText    = "text"
	backupValueBlob    = "blob"

	backupDatabaseMissingMessage = "数据库连接未就绪，无法备份/恢复账号。"
)

// backupIdentifierPattern 复刻 A21:16-18 的标识符白名单：所有进 SQL 的表名/列名
// 都必须先过它（连同 backupQuoteIdent 的反引号转义），杜绝把备份文件里的字符串
// 直接拼进 SQL。
var backupIdentifierPattern = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)

// ---------------------------------------------------------------------------
// 值 / 表 / 文件 / 结果的 JSON 形状（A21:1153-1280 的逐字段移植）
// ---------------------------------------------------------------------------

// backupValue 是单元格的带类型表示（A21:1153-1211 AccountBackupValue）。
// 为什么要带类型而不是直接塞 JSON 标量：备份必须能**无损**还原 NULL、整数、浮点、
// 文本与二进制（背包 raw_entry / 邮件 body 都是 LONGBLOB），JSON 标量做不到。
type backupValue struct {
	Type    string   `json:"type"`
	Integer *int64   `json:"integer,omitempty"`
	Real    *float64 `json:"real,omitempty"`
	Text    *string  `json:"text,omitempty"`
	Blob    *string  `json:"blob,omitempty"` // base64（A21:1175 Convert.ToBase64String）
}

// backupTableDump 是一张表在该账号范围内的全部行（A21:1213-1223）。
type backupTableDump struct {
	Name    string          `json:"name"`
	Columns []string        `json:"columns"`
	Rows    [][]backupValue `json:"rows"`
}

// backupFile 是备份文件的完整形状（A21:1225-1241 AccountBackupFile）。
//
// accountKey 是**本仓新增**的字段：A21 的 accountID 就是 SQLite 的整数主键，
// 而本仓面板主键（dnf90_web_accounts.id）与仓储账号 ID（dnf_accounts.account_id，
// 形如 "dnf:10001"）是两个值，恢复时两者都必须能定位到账号，因此一起进文件。
// 前端只读 version / tables / characterIDs（sidebar.js:265/292/299），不受影响。
type backupFile struct {
	Version      int               `json:"version"`
	ExportedAt   string            `json:"exportedAt"`
	AccountID    int64             `json:"accountID"`
	AccountKey   string            `json:"accountKey"`
	CharacterIDs []string          `json:"characterIDs"`
	Tables       []backupTableDump `json:"tables"`
}

// backupRestoreResult 是恢复结果（A21:1243-1280 RestoreAccountBackupResult）。
// 前端实际读取的字段：accountID / restoredCharacterCount /
// deletedExistingCharacterCount / remappedMailboxMessageIdCount /
// remappedMailboxAuditIdCount（sidebar.js:295-299），其余为可读性与审计。
type backupRestoreResult struct {
	AccountID                     int64    `json:"accountID"`
	OverwroteExistingAccount      bool     `json:"overwroteExistingAccount"`
	DeletedExistingCharacterCount int      `json:"deletedExistingCharacterCount"`
	RestoredCharacterCount        int      `json:"restoredCharacterCount"`
	CharacterIDs                  []string `json:"characterIDs"`
	RemappedAvatarUIDCount        int      `json:"remappedAvatarUidCount"`
	RemappedCreatureUIDCount      int      `json:"remappedCreatureUidCount"`
	RemappedMailboxMessageIDCount int      `json:"remappedMailboxMessageIdCount"`
	RemappedMailboxAuditIDCount   int      `json:"remappedMailboxAuditIdCount"`
	SourceBackupVersion           int      `json:"sourceBackupVersion"`
	UpgradedFromVersion           int      `json:"upgradedFromVersion"`
	// 下面三个是本仓新增的"到底改了多少行"的如实回报（A21 只回报角色数）。
	DeletedRowCount  int64    `json:"deletedRowCount"`
	RestoredRowCount int64    `json:"restoredRowCount"`
	RestoredTables   []string `json:"restoredTables"`
	SkippedTables    []string `json:"skippedTables"`
}

// backupRejection 是"具名拒绝、零写入"的结构化原因：message 回给面板，
// reason 进具名日志（与 mailboxDeleteRejection / questMutationRejection 同形态）。
type backupRejection struct {
	reason  string
	message string
}

func (e *backupRejection) Error() string { return e.message }

func backupReject(reason, format string, args ...any) *backupRejection {
	return &backupRejection{reason: reason, message: fmt.Sprintf(format, args...)}
}

func backupReasonOf(err error) string {
	var rejection *backupRejection
	if errors.As(err, &rejection) {
		return rejection.reason
	}
	return "internal_error"
}

func backupMessageOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// 表清单与归属谓词
// ---------------------------------------------------------------------------

// backupLinkKind 说明一张表"怎么算属于这个账号"（A21:512-573 BuildAccountBackupPredicate
// 的四个分支在本仓的等价物）。
type backupLinkKind int

const (
	// backupLinkAccount：表有 account_id 列（账号级数据：点券元数据、账号金库…）。
	backupLinkAccount backupLinkKind = iota
	// backupLinkCharacter：表有 character_id 列（角色数据：背包/任务/技能/邮件…）。
	backupLinkCharacter
	// backupLinkCharacterRoster：dnf_characters 本身 —— 导出按
	// `account_id = ? AND delete_flag = 0`（A21:532-539），删除按 `account_id = ?`
	// （A21:711-714），因而"软删除的角色"不会被导出、但一定会被清除。
	backupLinkCharacterRoster
	// backupLinkRelated：子表没有账号/角色列，只能通过父表的键值定位
	// （A21:575-641 AppendRelatedDump）。本仓只有 C# 导入镜像表用到它。
	backupLinkRelated
)

// backupTableSpec 是一张参与备份的表的归属说明。
type backupTableSpec struct {
	name string
	link backupLinkKind
	// optional 为真时：目标库缺这张表只跳过并如实回报，不让整次恢复失败。
	// 核心表一律 false（缺表 = 备份不完整，必须具名失败）。
	optional bool
	// related 专用：父表名、父表键列、子表键列。
	parent    string
	parentKey string
	childKey  string
}

// backupCoreTableSpecs 是本仓**权威表**的备份清单，顺序即父子顺序
// （A21:20-76 的 AccountBackupPreferredOrder 在本仓的等价物；父表在前，
// 删除时反序执行 = 子表先删，见 backupDeleteAccountData）。
//
// 逐张对照：
//
//	schema.go:213-222    dnf_accounts
//	schema.go:223-227    dnf_account_inventories
//	schema.go:228-241    dnf_characters（含全部角色数值列与 delete_flag）
//	schema.go:242-246    dnf_inventories
//	schema.go:247-251    dnf_equipments
//	schema.go:252-258    dnf_pets
//	schema.go:259-263    dnf_quests
//	schema.go:264-273    dnf_skills
//	schema.go:274-282    dnf_dungeon_permissions
//	schema.go:283-287    dnf_mailboxes
//	schema_relational.go:39-41   dnf_account_metadata / dnf_account_inventory_items /
//	                             dnf_account_inventory_item_extra
//	schema_relational.go:42-47   dnf_character_stats
//	schema_relational.go:48-55   dnf_character_locations
//	schema_relational.go:56-75   dnf_character_rosters / dnf_character_roster_equipment /
//	                             dnf_character_roster_lists
//	schema_relational.go:76-77   dnf_inventory_items / dnf_inventory_item_extra
//	schema_relational.go:78-88   dnf_equipment_entries / dnf_equipment_entry_extra
//	schema_relational.go:89-101  dnf_pet_entries / dnf_pet_entry_extra /
//	                             dnf_pet_clear_tokens / dnf_pet_artifacts /
//	                             dnf_pet_artifact_extra
//	schema_relational.go:102-141 dnf_quest_states / dnf_quest_state_extra /
//	                             dnf_skill_states / dnf_skill_layouts / dnf_skill_cooldowns
//	schema_relational.go:142-158 dnf_mails / dnf_mail_metadata / dnf_mail_attachments /
//	                             dnf_mail_attachment_extra
//
// **有意不在清单里**的全局表（它们不属于任何账号，A21 同样导不出：
// BuildAccountBackupPredicate 对它们返回 null）：dnf_packet_templates、
// dnf_packet_template_metadata、dnf_settings、dnf_setting_values，
// 以及启动器侧的 dnf90_web_accounts / dnf90_web_login_tokens（登录凭据，永不进备份）。
func backupCoreTableSpecs() []backupTableSpec {
	return []backupTableSpec{
		{name: "dnf_accounts", link: backupLinkAccount},
		{name: "dnf_account_metadata", link: backupLinkAccount},
		{name: "dnf_account_inventories", link: backupLinkAccount},
		{name: "dnf_account_inventory_items", link: backupLinkAccount},
		{name: "dnf_account_inventory_item_extra", link: backupLinkAccount},
		{name: "dnf_characters", link: backupLinkCharacterRoster},
		{name: "dnf_character_stats", link: backupLinkCharacter},
		{name: "dnf_character_locations", link: backupLinkCharacter},
		{name: "dnf_character_rosters", link: backupLinkCharacter},
		{name: "dnf_character_roster_equipment", link: backupLinkCharacter},
		{name: "dnf_character_roster_lists", link: backupLinkCharacter},
		{name: "dnf_inventories", link: backupLinkCharacter},
		{name: "dnf_inventory_items", link: backupLinkCharacter},
		{name: "dnf_inventory_item_extra", link: backupLinkCharacter},
		{name: "dnf_equipments", link: backupLinkCharacter},
		{name: "dnf_equipment_entries", link: backupLinkCharacter},
		{name: "dnf_equipment_entry_extra", link: backupLinkCharacter},
		{name: "dnf_pets", link: backupLinkCharacter},
		{name: "dnf_pet_entries", link: backupLinkCharacter},
		{name: "dnf_pet_entry_extra", link: backupLinkCharacter},
		{name: "dnf_pet_clear_tokens", link: backupLinkCharacter},
		{name: "dnf_pet_artifacts", link: backupLinkCharacter},
		{name: "dnf_pet_artifact_extra", link: backupLinkCharacter},
		{name: "dnf_quests", link: backupLinkCharacter},
		{name: "dnf_quest_states", link: backupLinkCharacter},
		{name: "dnf_quest_state_extra", link: backupLinkCharacter},
		{name: "dnf_skills", link: backupLinkCharacter},
		{name: "dnf_skill_states", link: backupLinkCharacter},
		{name: "dnf_skill_layouts", link: backupLinkCharacter},
		{name: "dnf_skill_cooldowns", link: backupLinkCharacter},
		{name: "dnf_dungeon_permissions", link: backupLinkCharacter},
		{name: "dnf_mailboxes", link: backupLinkCharacter},
		{name: "dnf_mails", link: backupLinkCharacter},
		{name: "dnf_mail_metadata", link: backupLinkCharacter},
		{name: "dnf_mail_attachments", link: backupLinkCharacter},
		{name: "dnf_mail_attachment_extra", link: backupLinkCharacter},
	}
}

// backupLegacySchema 是目标库里 `dnf_legacy_*` 镜像表的现场探测结果
// （A21:435-465 LoadAccountBackupTableInfos 的 MySQL 版）。
type backupLegacySchema struct {
	columns map[string]map[string]struct{}
	names   []string
}

// backupLoadLegacySchema 从 information_schema 读出当前库的全部 dnf_legacy_* 表及其列。
//
// 为什么只探测 legacy 表：它们是 C# inventory.db 一次性导入的产物（legacy_schema.go:1-3
// "legacy 表只作为导入和协议证据镜像"），运行时**只读**（mysql_legacy_userinfo.go:34、
// mysql_legacy_inventory.go:39），表数约一百张且随 C# schema 漂移，钉清单必然漏。
// 权威表反过来：数量固定、语义已知，钉清单更安全（见文件头"差异 1"）。
//
// 用 `LEFT(table_name, 11) = ?` 而不是 `LIKE 'dnf_legacy_%'`：后者里的 `_` 是
// 单字符通配符，`dnf_legacy_` 还好，但同一个坑在 `dnf_%` 上会把
// `dnf90_web_accounts` 也匹配进来 —— 这类"看似精确其实更宽"的匹配一律不用。
func backupLoadLegacySchema(ctx context.Context, runner backupSQLRunner) (backupLegacySchema, error) {
	const query = "SELECT table_name, column_name FROM information_schema.columns" +
		" WHERE table_schema = DATABASE() AND LEFT(table_name, ?) = ?" +
		" ORDER BY table_name, ordinal_position"
	rows, err := runner.QueryContext(ctx, query, len(backupLegacyTablePrefix), backupLegacyTablePrefix)
	if err != nil {
		return backupLegacySchema{}, fmt.Errorf("读取 dnf_legacy_* 镜像表结构失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	schema := backupLegacySchema{columns: map[string]map[string]struct{}{}}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return backupLegacySchema{}, err
		}
		table = strings.TrimSpace(table)
		column = strings.TrimSpace(column)
		if table == "" || column == "" {
			continue
		}
		columns, ok := schema.columns[table]
		if !ok {
			columns = map[string]struct{}{}
			schema.columns[table] = columns
			schema.names = append(schema.names, table)
		}
		columns[column] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return backupLegacySchema{}, err
	}
	sort.Strings(schema.names)
	return schema, nil
}

func (s backupLegacySchema) exists(table string) bool {
	_, ok := s.columns[table]
	return ok
}

// specs 把探测结果变成备份清单：只收"能用 character_id 归属"的表，
// 再补上两张经由父表键值归属的子表（A21:575-609 的三条关系，本仓只有这两条
// 在 dnf_legacy_* 里成立：character_items.item_uid -> character_item_extra.item_uid、
// item_audit_log.audit_id -> item_audit_payload_values.audit_id）。
func (s backupLegacySchema) specs() []backupTableSpec {
	specs := make([]backupTableSpec, 0, len(s.names))
	linked := map[string]bool{}
	for _, name := range s.names {
		if _, ok := s.columns[name]["character_id"]; !ok {
			continue
		}
		// 有 character_id 的镜像表才可能属于某个角色；软删除/全局种子表不进备份。
		specs = append(specs, backupTableSpec{name: name, link: backupLinkCharacter, optional: true})
		linked[name] = true
	}
	appendRelated := func(parent, parentKey, child, childKey string) {
		if !linked[parent] || !s.exists(child) {
			return
		}
		if _, ok := s.columns[child][childKey]; !ok {
			return
		}
		specs = append(specs, backupTableSpec{
			name: child, link: backupLinkRelated, optional: true,
			parent: parent, parentKey: parentKey, childKey: childKey,
		})
	}
	appendRelated("dnf_legacy_character_items", "item_uid", "dnf_legacy_character_item_extra", "item_uid")
	appendRelated("dnf_legacy_item_audit_log", "audit_id", "dnf_legacy_item_audit_payload_values", "audit_id")
	return specs
}

// backupPredicate 是一段 WHERE 子句及其参数。
type backupPredicate struct {
	sql  string
	args []any
}

// backupInPredicate 生成 `col IN (?,?,...)`（A21:1074-1088 BuildInClause）。
func backupInPredicate(column string, values []string) (backupPredicate, bool, error) {
	quoted, err := backupQuoteIdent(column)
	if err != nil {
		return backupPredicate{}, false, err
	}
	if len(values) == 0 {
		return backupPredicate{}, false, nil
	}
	placeholders := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	return backupPredicate{sql: quoted + " IN (" + strings.Join(placeholders, ", ") + ")", args: args}, true, nil
}

// backupScopePredicate 生成一张表的归属谓词：`account_id = ?`（账号级）或
// `character_id IN (...)`（角色级）。dnf_characters 走 account_id 分支
// （A21:532-539 的导出与 A21:711-714 的删除都用它，删除时没有 delete_flag 条件，
// 因此软删除的角色也一定会被清掉）。
//
// 导出与删除**必须**用同一个范围，否则就会删到别的账号的数据。
func backupScopePredicate(
	spec backupTableSpec,
	accountKey string,
	characterIDs []string,
) (backupPredicate, bool, error) {
	switch spec.link {
	case backupLinkAccount:
		if strings.TrimSpace(accountKey) == "" {
			return backupPredicate{}, false, errors.New("账号 ID 为空")
		}
		column, err := backupQuoteIdent("account_id")
		if err != nil {
			return backupPredicate{}, false, err
		}
		return backupPredicate{sql: column + " = ?", args: []any{accountKey}}, true, nil
	case backupLinkCharacter:
		return backupInPredicate("character_id", characterIDs)
	case backupLinkCharacterRoster:
		column, err := backupQuoteIdent("account_id")
		if err != nil {
			return backupPredicate{}, false, err
		}
		return backupPredicate{sql: column + " = ?", args: []any{accountKey}}, true, nil
	default:
		return backupPredicate{}, false, fmt.Errorf("表 %s 没有直接的归属谓词", spec.name)
	}
}

// backupQuoteIdent 是唯一的标识符入 SQL 通道（A21:1100-1105 QuoteAccountBackupIdentifier）：
// 先过白名单正则，再加反引号。
func backupQuoteIdent(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !backupIdentifierPattern.MatchString(name) {
		return "", fmt.Errorf("无效的数据库标识符: %q", name)
	}
	return "`" + name + "`", nil
}

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

// backupSQLRunner 是本文件需要的最小 SQL 接口；*sql.DB 与 *sql.Tx 都满足它，
// 因此同一份逻辑既能跑在连接池上（导出、目标结构探测），也能跑在事务里（恢复）。
type backupSQLRunner interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// backupSQLDatabase 是连接级接口（事务 + 连通性探针）。
type backupSQLDatabase interface {
	backupSQLRunner
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	PingContext(ctx context.Context) error
}

// backupHandleAccountExport 实现 POST /api/accounts/{id}/backup。
//
// 请求体：前端发的是空对象 `{}`（sidebar.js:253 的 `post(path)`），本路由不解析它 ——
// 备份范围只由路径里的账号决定，没有可选参数（不发明开关）。
func (s *Server) backupHandleAccountExport(w http.ResponseWriter, r *http.Request) {
	panelID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || panelID <= 0 {
		writeFailure(w, "账号编号无效")
		return
	}
	ctx := r.Context()
	// 仓储就绪门禁（与面板其余路由同一道门，deps.go:138-147）。备份本身只读数据库，
	// 但"仓储没就绪"意味着服务端还没装配完，此时允许导出会让操作者拿到一份
	// 来路不明的快照，所以照样拒绝。
	if _, repoErr := s.repositories(); repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	if s.db == nil {
		writeFailure(w, backupDatabaseMissingMessage)
		return
	}
	account, err := s.webAccountByPanelID(ctx, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("账号不存在: %d", panelID))
		return
	}
	// 角色清单从**数据库**读（`dnf_characters` 里 delete_flag = 0 的全部行），
	// 而不是复用 deps.go:334-351 的 charactersForAccount：那一条按选人界面槽位
	// （DefaultCharacterSlotLimit = 32）取可见角色。备份的 dnf_characters 谓词是
	// 纯 SQL 的 `account_id = ? AND delete_flag = 0`，若两边的集合不一致，
	// 导出会产出一份"自己的恢复会拒绝"的备份（归属校验要求每一行的 character_id
	// 都在 characterIDs 里）。用同一套 SQL 口径读，两者必然逐行相同。
	characterIDs, err := backupLoadCharacterIDs(ctx, s.db, account.AccountID, false)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	file, err := backupExportAccount(ctx, s.db, account, characterIDs)
	if err != nil {
		s.logEvent("gm-account-backup-export-failed",
			"panel_id", account.PanelID, "account_id", account.AccountID,
			"reason", backupReasonOf(err), "error", backupMessageOf(err), "result", "failed")
		writeFailure(w, backupMessageOf(err))
		return
	}
	payload, err := backupFileAsPayload(file)
	if err != nil {
		writeFailure(w, "备份序列化失败: "+err.Error())
		return
	}
	s.logEvent("gm-account-backup-export",
		"panel_id", account.PanelID, "account_id", account.AccountID,
		"version", file.Version, "tables", len(file.Tables),
		"characters", len(file.CharacterIDs), "result", "exported")
	writeOK(w, payload)
}

// backupFileAsPayload 把备份结构体转成 writeOK 需要的 map。
//
// 绕一圈 JSON 是**有意的**：面板会把这条响应原样存成文件、再原样 POST 回恢复路由
// （sidebar.js:254-262 与 :293），所以"响应里的字段名"必须与"文件里的字段名"
// 逐字相同 —— 用同一次 Marshal 产生两者，就不可能出现两套名字。
func backupFileAsPayload(file backupFile) (map[string]any, error) {
	raw, err := json.Marshal(file)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// backupExportAccount 是导出的全部行为（A21:105-152）。
//
// 读事务由调用方（连接池）语义保证：这里只发 SELECT。
// 任何一张**核心表**读失败（缺表/缺列/权限）都会让整次导出失败 —— 宁可明确报错，
// 也绝不产出一份"少了几张表"的备份（A21:133 的"空表跳过"只针对没有行的表）。
func backupExportAccount(
	ctx context.Context,
	runner backupSQLRunner,
	account webAccount,
	characterIDs []string,
) (backupFile, error) {
	accountKey := strings.TrimSpace(account.AccountID)
	if accountKey == "" {
		return backupFile{}, backupReject("account_key_missing",
			"账号 #%d 在启动器账号表里没有对应的游戏账号 ID，无法定位其数据。", account.PanelID)
	}
	legacy, err := backupLoadLegacySchema(ctx, runner)
	if err != nil {
		return backupFile{}, err
	}
	specs := append(backupCoreTableSpecs(), legacy.specs()...)

	file := backupFile{
		Version:      backupFormatVersion,
		ExportedAt:   time.Now().Format(time.RFC3339Nano),
		AccountID:    account.PanelID,
		AccountKey:   accountKey,
		CharacterIDs: append([]string{}, characterIDs...),
		Tables:       make([]backupTableDump, 0, len(specs)),
	}
	dumps := map[string]backupTableDump{}
	for _, spec := range specs {
		var predicate backupPredicate
		var ok bool
		var err error
		if spec.link == backupLinkRelated {
			// related 子表没有自己的归属列：用父表 dump 的键值定位
			// （A21:611-641 AppendRelatedDump）。父表没有 dump（当时是空表）
			// 就说明这张子表也没有属于本账号的行。
			keys, keyErr := backupRelatedKeys(dumps[spec.parent], spec.parentKey)
			if keyErr != nil {
				return backupFile{}, keyErr
			}
			predicate, ok, err = backupInPredicate(spec.childKey, keys)
		} else {
			predicate, ok, err = backupScopePredicate(spec, accountKey, characterIDs)
			if err == nil && ok && spec.link == backupLinkCharacterRoster {
				// A21:532-539：characters 只导出未删除的角色。
				predicate = backupPredicate{sql: "(" + predicate.sql + " AND `delete_flag` = 0)", args: predicate.args}
			}
		}
		if err != nil {
			return backupFile{}, err
		}
		if !ok {
			// 没有角色 ⇒ 角色表没有可归属的行（A21:540 的 `characterIds.Count > 0`）。
			continue
		}
		dump, err := backupDumpTable(ctx, runner, spec, predicate)
		if err != nil {
			// 探测到存在的表读不出来（权限/引擎差异）也要**明确失败**：
			// 绝不产出一份"少了几张表"却看起来完整的备份。
			return backupFile{}, fmt.Errorf("导出表 %s 失败: %w", spec.name, err)
		}
		// A21:133：空表不进备份，只有 accounts 无论有没有行都保留
		// （它是"这个备份属于哪个账号"的证据）。
		if len(dump.Rows) == 0 && spec.name != "dnf_accounts" {
			continue
		}
		dumps[spec.name] = dump
		file.Tables = append(file.Tables, dump)
	}
	// 自洽性闸门（= A21:175-180 在恢复端做的 `EqualIntSets` 检查，这里在导出端就做）：
	// dnf_characters 的行必须与 characterIDs 完全一致，否则这份备份一恢复就会被
	// 归属校验拒绝 —— 与其让操作者拿到一份恢复不了的文件，不如现在就具名失败。
	if characters, ok := dumps["dnf_characters"]; ok {
		ids, err := characters.backupKeyValues("character_id")
		if err != nil {
			return backupFile{}, err
		}
		if !backupEqualKeySets(ids, characterIDs) {
			return backupFile{}, backupReject("character_set_mismatch",
				"账号 %s 的角色行（%d 个）与角色清单（%d 个）不一致，已拒绝导出（请检查 dnf_characters 里是否有 slot 超出选人界面容量的行）。",
				accountKey, len(ids), len(characterIDs))
		}
	}
	return file, nil
}

// backupDumpTable 读一张表在给定范围内的全部行（A21:643-680）。
func backupDumpTable(
	ctx context.Context,
	runner backupSQLRunner,
	spec backupTableSpec,
	predicate backupPredicate,
) (backupTableDump, error) {
	table, err := backupQuoteIdent(spec.name)
	if err != nil {
		return backupTableDump{}, err
	}
	query := "SELECT * FROM " + table + " WHERE " + predicate.sql
	rows, err := runner.QueryContext(ctx, query, predicate.args...)
	if err != nil {
		return backupTableDump{}, err
	}
	defer func() { _ = rows.Close() }()
	return backupReadDump(rows, spec.name)
}

// backupReadDump 把结果集读成带类型的行（A21:649-678 的列名 + 逐格 FromDbValue）。
func backupReadDump(rows *sql.Rows, table string) (backupTableDump, error) {
	columns, err := rows.Columns()
	if err != nil {
		return backupTableDump{}, err
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return backupTableDump{}, err
	}
	typeNames := make([]string, len(columnTypes))
	for idx, columnType := range columnTypes {
		typeNames[idx] = columnType.DatabaseTypeName()
	}
	dump := backupTableDump{Name: table, Columns: append([]string{}, columns...), Rows: [][]backupValue{}}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for idx := range values {
			destinations[idx] = &values[idx]
		}
		if err := rows.Scan(destinations...); err != nil {
			return backupTableDump{}, err
		}
		row := make([]backupValue, len(columns))
		for idx, value := range values {
			converted, err := backupValueFromDB(value, typeNames[idx])
			if err != nil {
				return backupTableDump{}, fmt.Errorf("表 %s 列 %s: %w", table, columns[idx], err)
			}
			row[idx] = converted
		}
		dump.Rows = append(dump.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return backupTableDump{}, err
	}
	return dump, nil
}

// backupValueFromDB 把一个驱动值转成带类型备份值。
//
// 文本 / 二进制的判定顺序（本仓特有，见文件头"差异 2"）：
//  1. 列类型名里含 BLOB/BINARY/BIT ⇒ 二进制（`raw_entry`、`body`、`name_raw` 都是 LONGBLOB）；
//  2. 否则若字节不是合法 UTF-8 ⇒ 二进制（类型名拿不到时的兜底，绝不把二进制塞进 JSON 字符串 ——
//     encoding/json 会把非法 UTF-8 换成 U+FFFD，那是**不可逆的数据损坏**）；
//  3. 其余按文本。
func backupValueFromDB(value any, columnType string) (backupValue, error) {
	switch typed := value.(type) {
	case nil:
		return backupValue{Type: backupValueNull}, nil
	case int64:
		number := typed
		return backupValue{Type: backupValueInteger, Integer: &number}, nil
	case float64:
		number := typed
		return backupValue{Type: backupValueReal, Real: &number}, nil
	case bool:
		number := int64(0)
		if typed {
			number = 1
		}
		return backupValue{Type: backupValueInteger, Integer: &number}, nil
	case time.Time:
		// parseTime=true 的 DSN 会把 DATETIME(6) 读成 time.Time；写回时用同样的墙钟字面量。
		text := typed.Format(backupSQLTimeLayout)
		return backupValue{Type: backupValueText, Text: &text}, nil
	case []byte:
		if backupColumnIsBinary(columnType) || !utf8.Valid(typed) {
			encoded := base64.StdEncoding.EncodeToString(typed)
			return backupValue{Type: backupValueBlob, Blob: &encoded}, nil
		}
		text := string(typed)
		return backupValue{Type: backupValueText, Text: &text}, nil
	case string:
		text := typed
		return backupValue{Type: backupValueText, Text: &text}, nil
	default:
		// 不认识的类型一律明确失败：绝不 fmt.Sprint 一个来路不明的值进备份。
		return backupValue{}, fmt.Errorf("无法表示的数据库值类型 %T（列类型 %q）", value, columnType)
	}
}

func backupColumnIsBinary(columnType string) bool {
	upper := strings.ToUpper(strings.TrimSpace(columnType))
	return strings.Contains(upper, "BLOB") || strings.Contains(upper, "BINARY") || upper == "BIT"
}

// toDBValue 是 backupValueFromDB 的逆（A21:1183-1204 AccountBackupValue.ToDbValue）。
func (v backupValue) toDBValue() (any, error) {
	switch v.Type {
	case backupValueNull:
		return nil, nil
	case backupValueInteger:
		if v.Integer == nil {
			return nil, errors.New("整数备份值缺少 integer 字段")
		}
		return *v.Integer, nil
	case backupValueReal:
		if v.Real == nil {
			return nil, errors.New("浮点备份值缺少 real 字段")
		}
		return *v.Real, nil
	case backupValueText:
		if v.Text == nil {
			return "", nil
		}
		return *v.Text, nil
	case backupValueBlob:
		raw := ""
		if v.Blob != nil {
			raw = *v.Blob
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("二进制备份值不是合法 base64: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("不支持的备份值类型: %q", v.Type)
	}
}

// backupColumnIndex 返回列下标（大小写不敏感，与 A21:1011-1021 同口径）。
func (dump backupTableDump) backupColumnIndex(column string) (int, bool) {
	for idx, name := range dump.Columns {
		if strings.EqualFold(strings.TrimSpace(name), column) {
			return idx, true
		}
	}
	return 0, false
}

// backupKeyValues 取一列里的非空键值（去重、排序）。
func (dump backupTableDump) backupKeyValues(column string) ([]string, error) {
	index, ok := dump.backupColumnIndex(column)
	if !ok {
		return nil, nil
	}
	seen := map[string]struct{}{}
	values := make([]string, 0, len(dump.Rows))
	for _, row := range dump.Rows {
		if index >= len(row) {
			return nil, fmt.Errorf("表 %s 的备份行列数不一致", dump.Name)
		}
		raw, err := row[index].toDBValue()
		if err != nil {
			return nil, err
		}
		text := backupKeyText(raw)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		values = append(values, text)
	}
	sort.Strings(values)
	return values, nil
}

// backupKeyText 把键值统一成字符串（数据库里 legacy 键是 BIGINT、权威表是 VARCHAR，
// 两边都要能比较/当参数用）。
func backupKeyText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "1"
		}
		return "0"
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// backupRelatedKeys 是一张 related 子表的父键值集合（A21:611-641 AppendRelatedDump
// 用的就是父 dump 的 id 列）。
func backupRelatedKeys(parent backupTableDump, keyColumn string) ([]string, error) {
	if strings.TrimSpace(parent.Name) == "" {
		return nil, nil
	}
	return parent.backupKeyValues(keyColumn)
}

func backupChunk(values []string, size int) [][]string {
	if size <= 0 || len(values) <= size {
		return [][]string{values}
	}
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

// ---------------------------------------------------------------------------
// 恢复
// ---------------------------------------------------------------------------

// backupHandleAccountRestore 实现 POST /api/accounts/restore。
//
// 请求体 = 导出路由的响应体（也就是操作者下载的那个 JSON 文件），字段名逐字相同。
// 顺序：解析/校验文件 → 定位并**核对归属**账号 → 在线守卫 → 事务内"先删后建"。
// 任何一步失败都在任何写入之前，或者整事务回滚（见 backupRestoreAccount）。
func (s *Server) backupHandleAccountRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, repoErr := s.repositories()
	if repoErr != nil {
		writeFailure(w, repoErr.Error())
		return
	}
	if s.db == nil {
		writeFailure(w, backupDatabaseMissingMessage)
		return
	}
	file, err := backupDecodeRestoreBody(w, r)
	if err != nil {
		s.logEvent("gm-account-backup-restore-rejected",
			"reason", backupReasonOf(err), "error", backupMessageOf(err), "result", "rejected")
		writeFailure(w, backupMessageOf(err)+"本次未做任何写入。")
		return
	}
	account, err := s.webAccountByPanelID(ctx, file.AccountID)
	if err != nil {
		message := fmt.Sprintf("账号不存在: %d（恢复要覆盖该账号的现有数据，所以目标账号必须先存在）。", file.AccountID)
		s.logEvent("gm-account-backup-restore-rejected",
			"panel_id", file.AccountID, "reason", "target_account_missing", "result", "rejected")
		writeFailure(w, message+"本次未做任何写入。")
		return
	}
	// 归属核对（硬性要求）：备份里的游戏账号 ID 必须与目标面板账号现在指向的账号一致。
	// 否则就是"A 账号的备份往 B 账号恢复"，一律拒绝、零写入。
	if !backupSameKey(account.AccountID, file.AccountKey) {
		message := fmt.Sprintf("备份属于游戏账号 %s，而面板账号 #%d 当前对应 %s：拒绝跨账号恢复。",
			file.AccountKey, account.PanelID, account.AccountID)
		s.logEvent("gm-account-backup-restore-rejected",
			"panel_id", account.PanelID, "account_id", account.AccountID,
			"backup_account_id", file.AccountKey, "reason", "account_mismatch", "result", "rejected")
		writeFailure(w, message+"本次未做任何写入。")
		return
	}
	// 在线守卫：账号内仍有角色在对局会话里时拒绝（默认档不检查，见 online_guard.go）。
	if !s.requireAccountOffline(ctx, w, "account-backup-restore", group, account.AccountID, int(account.PanelID)) {
		return
	}

	result, err := backupRestoreAccount(ctx, s.db, account, file)
	if err != nil {
		s.logEvent("gm-account-backup-restore-failed",
			"panel_id", account.PanelID, "account_id", account.AccountID,
			"reason", backupReasonOf(err), "error", backupMessageOf(err), "result", "failed")
		writeFailure(w, backupMessageOf(err))
		return
	}
	s.logEvent("gm-account-backup-restore",
		"panel_id", account.PanelID, "account_id", account.AccountID,
		"version", result.SourceBackupVersion, "characters", result.RestoredCharacterCount,
		"deleted_characters", result.DeletedExistingCharacterCount,
		"deleted_rows", result.DeletedRowCount, "restored_rows", result.RestoredRowCount,
		"result", "restored")
	writeOK(w, map[string]any{
		"accountID":                     result.AccountID,
		"overwroteExistingAccount":      result.OverwroteExistingAccount,
		"deletedExistingCharacterCount": result.DeletedExistingCharacterCount,
		"restoredCharacterCount":        result.RestoredCharacterCount,
		"characterIDs":                  result.CharacterIDs,
		"remappedAvatarUidCount":        result.RemappedAvatarUIDCount,
		"remappedCreatureUidCount":      result.RemappedCreatureUIDCount,
		"remappedMailboxMessageIdCount": result.RemappedMailboxMessageIDCount,
		"remappedMailboxAuditIdCount":   result.RemappedMailboxAuditIDCount,
		"sourceBackupVersion":           result.SourceBackupVersion,
		"upgradedFromVersion":           result.UpgradedFromVersion,
		"deletedRowCount":               result.DeletedRowCount,
		"restoredRowCount":              result.RestoredRowCount,
		"restoredTables":                result.RestoredTables,
		"skippedTables":                 result.SkippedTables,
		"hint":                          "恢复直接写数据库；若该账号的角色正在游戏内，请让其退回选人界面并重新进入，否则客户端仍显示旧数据。",
	})
}

// backupRestoreAccount 是恢复的全部行为（A21:154-255）。
//
// 阶段划分（**先全部校验、再破坏性写入**，与 A21 的顺序一致）：
//
//	① backupValidateFile            文件自洽性（版本/账号/角色列表/行列一致）
//	② 目标结构探测                   dnf_legacy_* 现场枚举 + 逐表 `WHERE 1 = 0` 试读
//	③ 归属校验                       每张表的每一行都必须落在本账号/本角色范围内
//	④ 现状快照                       现有角色 ID、账号是否已有数据
//	⑤ BEGIN … 删除该账号全部数据（子表先删）… 按备份重建 … COMMIT
//
// ⑤ 里的任何失败都会回滚整个事务：绝不半应用（硬性要求 6）。
func backupRestoreAccount(
	ctx context.Context,
	db backupSQLDatabase,
	account webAccount,
	file backupFile,
) (backupRestoreResult, error) {
	if err := backupValidateFile(file); err != nil {
		return backupRestoreResult{}, err
	}
	accountKey := strings.TrimSpace(account.AccountID)

	legacy, err := backupLoadLegacySchema(ctx, db)
	if err != nil {
		return backupRestoreResult{}, err
	}
	coreSpecs := backupCoreTableSpecs()
	coreByName := make(map[string]backupTableSpec, len(coreSpecs))
	for _, spec := range coreSpecs {
		coreByName[spec.name] = spec
	}
	legacyByName := map[string]backupTableSpec{}
	for _, spec := range legacy.specs() {
		legacyByName[spec.name] = spec
	}

	// 备份里的每一张表都必须能对应到本服务端认识的表；否则具名拒绝。
	// 这是恢复路径上最重要的一道闸：它保证恢复**永远不会**碰到清单以外的表，
	// 例如启动器登录凭据表 dnf90_web_accounts。
	restorable := make(map[string]backupTableSpec, len(file.Tables))
	skipped := make([]string, 0, 4)
	for _, dump := range file.Tables {
		if spec, ok := coreByName[dump.Name]; ok {
			restorable[dump.Name] = spec
			continue
		}
		if strings.HasPrefix(dump.Name, backupLegacyTablePrefix) {
			if spec, ok := legacyByName[dump.Name]; ok {
				restorable[dump.Name] = spec
				continue
			}
			// 镜像表在目标库里已经不存在（或不再有 character_id）：跳过并如实回报。
			skipped = append(skipped, dump.Name)
			continue
		}
		return backupRestoreResult{}, backupReject("unknown_table",
			"备份包含本服务端不认识的表 %q：拒绝恢复（本面板只恢复 90CN 账号备份里的权威表与 dnf_legacy_* 镜像表）。",
			dump.Name)
	}
	// A21:170-173：accounts 是"这份备份属于哪个账号"的证据，缺了就不许恢复；
	// 有角色就必须有 characters 表数据。两条都在任何写入之前。
	if _, ok := restorable["dnf_accounts"]; !ok {
		return backupRestoreResult{}, backupReject("missing_accounts_table", "备份文件缺少 dnf_accounts 表数据。")
	}
	if len(file.CharacterIDs) > 0 {
		if _, ok := restorable["dnf_characters"]; !ok {
			return backupRestoreResult{}, backupReject("missing_characters_table", "备份文件缺少 dnf_characters 表数据。")
		}
	}

	// ② 目标结构探测：`WHERE 1 = 0` 试读一次，缺表/缺列在**任何写入之前**暴露。
	for _, dump := range file.Tables {
		spec, ok := restorable[dump.Name]
		if !ok {
			continue
		}
		if err := backupProbeTable(ctx, db, dump); err != nil {
			if spec.optional {
				delete(restorable, dump.Name)
				skipped = append(skipped, dump.Name)
				continue
			}
			return backupRestoreResult{}, backupReject("target_schema_mismatch",
				"目标数据库无法恢复表 %s: %v", dump.Name, err)
		}
	}

	// ③ 归属校验：每一行都必须属于这个账号（A21:169-210 的结构校验 + 本仓的键范围校验）。
	if err := backupValidateDumpScope(file, restorable, accountKey); err != nil {
		return backupRestoreResult{}, err
	}

	sort.Strings(skipped)

	// ④ 现状快照（A21:212-214）。
	existingCharacterIDs, err := backupLoadCharacterIDs(ctx, db, accountKey, true)
	if err != nil {
		return backupRestoreResult{}, err
	}
	hasData, err := backupAccountHasData(ctx, db, accountKey)
	if err != nil {
		return backupRestoreResult{}, err
	}

	result := backupRestoreResult{
		AccountID:                     account.PanelID,
		OverwroteExistingAccount:      hasData,
		DeletedExistingCharacterCount: len(existingCharacterIDs),
		RestoredCharacterCount:        len(file.CharacterIDs),
		CharacterIDs:                  append([]string{}, file.CharacterIDs...),
		SourceBackupVersion:           file.Version,
		UpgradedFromVersion:           0,
		SkippedTables:                 skipped,
	}

	// 冲突检查（A21:218-220）：备份里的角色 ID 若属于别的账号，一律拒绝。
	conflicts, err := backupConflictingCharacterIDs(ctx, db, accountKey, file.CharacterIDs)
	if err != nil {
		return backupRestoreResult{}, err
	}
	if len(conflicts) > 0 {
		return backupRestoreResult{}, backupReject("character_id_conflict",
			"备份中的角色 ID 已被其他账号占用: %s", strings.Join(conflicts, ", "))
	}

	// ⑤ 单事务：删光该账号 -> 按备份重建。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return backupRestoreResult{}, fmt.Errorf("开启恢复事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	specs := append(coreSpecs, legacy.specs()...)
	deleted, err := backupDeleteAccountData(ctx, tx, specs, accountKey, existingCharacterIDs)
	if err != nil {
		return backupRestoreResult{}, fmt.Errorf("恢复失败（删除阶段），已回滚全部写入: %w", err)
	}
	inserted, restoredTables, err := backupInsertAccountData(ctx, tx, specs, restorable, file)
	if err != nil {
		return backupRestoreResult{}, fmt.Errorf("恢复失败（写入阶段），已回滚全部写入: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return backupRestoreResult{}, fmt.Errorf("恢复提交失败，已回滚全部写入: %w", err)
	}
	committed = true

	result.DeletedRowCount = deleted
	result.RestoredRowCount = inserted
	result.RestoredTables = restoredTables
	return result, nil
}

// backupDeleteAccountData 删除该账号在当前库里的全部数据（A21:693-715）。
//
// 顺序：specs 的**反序** —— 清单是父表在前，反序即子表先删（A21:698-700 的
// `OrderByDescending(AccountBackupOrderIndex)`）。related 子表没有自己的归属列，
// 因此先按父表**当前**的键值查出要删的行（父表此时还没被删，见反序保证）。
func backupDeleteAccountData(
	ctx context.Context,
	runner backupSQLRunner,
	specs []backupTableSpec,
	accountKey string,
	existingCharacterIDs []string,
) (int64, error) {
	byName := make(map[string]backupTableSpec, len(specs))
	for _, spec := range specs {
		byName[spec.name] = spec
	}
	var total int64
	for idx := len(specs) - 1; idx >= 0; idx-- {
		spec := specs[idx]
		var predicate backupPredicate
		if spec.link == backupLinkRelated {
			parent, ok := byName[spec.parent]
			if !ok {
				continue
			}
			parentPredicate, ok, err := backupScopePredicate(parent, accountKey, existingCharacterIDs)
			if err != nil {
				return total, err
			}
			if !ok {
				continue
			}
			keys, err := backupQueryKeys(ctx, runner, parent, parentPredicate, spec.parentKey)
			if err != nil {
				return total, err
			}
			if len(keys) == 0 {
				continue
			}
			predicate, ok, err = backupInPredicate(spec.childKey, keys)
			if err != nil {
				return total, err
			}
			if !ok {
				continue
			}
		} else {
			var ok bool
			var err error
			predicate, ok, err = backupScopePredicate(spec, accountKey, existingCharacterIDs)
			if err != nil {
				return total, err
			}
			if !ok {
				continue
			}
		}
		removed, err := backupDeleteScope(ctx, runner, spec.name, predicate)
		if err != nil {
			return total, err
		}
		total += removed
	}
	return total, nil
}

// backupDeleteScope 执行一次带范围的 DELETE；IN 列表按 backupInChunkSize 分批。
func backupDeleteScope(ctx context.Context, runner backupSQLRunner, table string, predicate backupPredicate) (int64, error) {
	quoted, err := backupQuoteIdent(table)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, chunk := range backupPredicateChunks(predicate) {
		result, err := runner.ExecContext(ctx, "DELETE FROM "+quoted+" WHERE "+chunk.sql, chunk.args...)
		if err != nil {
			return total, fmt.Errorf("删除表 %s 的旧数据失败: %w", table, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			// RowsAffected 拿不到不算失败（删除已经执行），只是少一个统计值。
			continue
		}
		total += affected
	}
	return total, nil
}

// backupQueryKeys 读一张表在给定范围内的某一列（related 子表删除时定位父键用）。
func backupQueryKeys(
	ctx context.Context,
	runner backupSQLRunner,
	spec backupTableSpec,
	predicate backupPredicate,
	keyColumn string,
) ([]string, error) {
	table, err := backupQuoteIdent(spec.name)
	if err != nil {
		return nil, err
	}
	column, err := backupQuoteIdent(keyColumn)
	if err != nil {
		return nil, err
	}
	rows, err := runner.QueryContext(ctx, "SELECT "+column+" FROM "+table+" WHERE "+predicate.sql, predicate.args...)
	if err != nil {
		return nil, fmt.Errorf("读取表 %s 的键值失败: %w", spec.name, err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]struct{}{}
	values := make([]string, 0, 16)
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		text := strings.TrimSpace(value.String)
		if !value.Valid || text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		values = append(values, text)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(values)
	return values, nil
}

// backupInsertAccountData 按清单顺序（父表在前）把备份里的行写回（A21:231-234 / :1023-1060）。
func backupInsertAccountData(
	ctx context.Context,
	runner backupSQLRunner,
	specs []backupTableSpec,
	restorable map[string]backupTableSpec,
	file backupFile,
) (int64, []string, error) {
	dumps := make(map[string]backupTableDump, len(file.Tables))
	for _, dump := range file.Tables {
		dumps[dump.Name] = dump
	}
	var total int64
	restored := make([]string, 0, len(file.Tables))
	for _, spec := range specs {
		if _, ok := restorable[spec.name]; !ok {
			continue
		}
		dump, ok := dumps[spec.name]
		if !ok {
			continue
		}
		inserted, err := backupInsertDump(ctx, runner, dump)
		if err != nil {
			return total, restored, err
		}
		total += inserted
		restored = append(restored, spec.name)
	}
	return total, restored, nil
}

// backupInsertDump 逐行 INSERT 一张表的备份行（A21:1023-1060）。
//
// 关于自增列：**不过滤任何列**。A21:78-87 要排除 item_uid / attachment_id 之类的
// 代理键（让目标库重新发号，再用 A21:791-1009 的两套重映射把子表指回去）；
// 本仓被备份的权威表全是自然主键，而仅有的两个自增键
// （dnf_legacy_character_items.item_uid、dnf_legacy_item_audit_log.audit_id）
// 是 C# 导入时按原库号写入的，原样插回才能让
// dnf_legacy_character_item_extra / dnf_legacy_item_audit_payload_values 的
// 外键关系不变。若目标库里该号已被别的角色占用，INSERT 会因主键冲突失败 ——
// 整个事务回滚、具名报错，绝不会出现"插了一半"的账号。
func backupInsertDump(ctx context.Context, runner backupSQLRunner, dump backupTableDump) (int64, error) {
	if len(dump.Rows) == 0 {
		return 0, nil
	}
	table, err := backupQuoteIdent(dump.Name)
	if err != nil {
		return 0, err
	}
	quotedColumns := make([]string, 0, len(dump.Columns))
	placeholders := make([]string, 0, len(dump.Columns))
	for _, column := range dump.Columns {
		quoted, err := backupQuoteIdent(column)
		if err != nil {
			return 0, err
		}
		quotedColumns = append(quotedColumns, quoted)
		placeholders = append(placeholders, "?")
	}
	query := "INSERT INTO " + table + " (" + strings.Join(quotedColumns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	var total int64
	for index, row := range dump.Rows {
		if len(row) != len(dump.Columns) {
			return total, fmt.Errorf("表 %s 第 %d 行列数不一致（%d != %d）",
				dump.Name, index+1, len(row), len(dump.Columns))
		}
		args := make([]any, 0, len(row))
		for _, value := range row {
			converted, err := value.toDBValue()
			if err != nil {
				return total, fmt.Errorf("表 %s 第 %d 行: %w", dump.Name, index+1, err)
			}
			args = append(args, converted)
		}
		if _, err := runner.ExecContext(ctx, query, args...); err != nil {
			return total, fmt.Errorf("写入表 %s 第 %d 行失败: %w", dump.Name, index+1, err)
		}
		total++
	}
	return total, nil
}

// backupProbeTable 用 `WHERE 1 = 0` 试读一次：缺表 / 缺列在这里就暴露，
// 而且不返回任何行、不写任何字节（A21:182-210 的"目标结构校验"的 MySQL 版）。
func backupProbeTable(ctx context.Context, runner backupSQLRunner, dump backupTableDump) error {
	table, err := backupQuoteIdent(dump.Name)
	if err != nil {
		return err
	}
	if len(dump.Columns) == 0 {
		return errors.New("备份表没有列")
	}
	quoted := make([]string, 0, len(dump.Columns))
	for _, column := range dump.Columns {
		value, err := backupQuoteIdent(column)
		if err != nil {
			return err
		}
		quoted = append(quoted, value)
	}
	rows, err := runner.QueryContext(ctx, "SELECT "+strings.Join(quoted, ", ")+" FROM "+table+" WHERE 1 = 0")
	if err != nil {
		return err
	}
	return rows.Close()
}

// backupLoadCharacterIDs 读该账号的角色 ID（A21:390-409 LoadAccountCharacterIds）。
// includeDeleted=true 时连 delete_flag != 0 的软删除角色一起返回 —— 删除阶段必须
// 覆盖它们，否则会留下"孤儿"子表行。
func backupLoadCharacterIDs(ctx context.Context, runner backupSQLRunner, accountKey string, includeDeleted bool) ([]string, error) {
	table, err := backupQuoteIdent("dnf_characters")
	if err != nil {
		return nil, err
	}
	query := "SELECT `character_id` FROM " + table + " WHERE `account_id` = ?"
	if !includeDeleted {
		query += " AND `delete_flag` = 0"
	}
	query += " ORDER BY `character_id`"
	rows, err := runner.QueryContext(ctx, query, accountKey)
	if err != nil {
		return nil, fmt.Errorf("读取账号角色列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0, 8)
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		text := strings.TrimSpace(value.String)
		if !value.Valid || text == "" {
			continue
		}
		ids = append(ids, text)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// backupAccountHasData 判断该账号现在有没有数据（A21:682-691 AccountHasAnyBackupData）。
func backupAccountHasData(ctx context.Context, runner backupSQLRunner, accountKey string) (bool, error) {
	for _, table := range []string{"dnf_accounts", "dnf_characters"} {
		quoted, err := backupQuoteIdent(table)
		if err != nil {
			return false, err
		}
		rows, err := runner.QueryContext(ctx, "SELECT 1 FROM "+quoted+" WHERE `account_id` = ? LIMIT 1", accountKey)
		if err != nil {
			return false, fmt.Errorf("读取账号现状失败: %w", err)
		}
		found := rows.Next()
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		if closeErr != nil {
			return false, closeErr
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// backupConflictingCharacterIDs 找出"备份里的角色 ID 现在属于别的账号"的那些
// （A21:768-789 FindConflictingBackupCharacterIds）。
func backupConflictingCharacterIDs(
	ctx context.Context,
	runner backupSQLRunner,
	accountKey string,
	characterIDs []string,
) ([]string, error) {
	if len(characterIDs) == 0 {
		return nil, nil
	}
	table, err := backupQuoteIdent("dnf_characters")
	if err != nil {
		return nil, err
	}
	conflicts := make([]string, 0, 4)
	for _, chunk := range backupChunk(characterIDs, backupInChunkSize) {
		placeholders := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, accountKey)
		for _, id := range chunk {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query := "SELECT `character_id` FROM " + table + " WHERE `account_id` <> ? AND `character_id` IN (" +
			strings.Join(placeholders, ", ") + ") ORDER BY `character_id`"
		rows, err := runner.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("检查角色 ID 冲突失败: %w", err)
		}
		for rows.Next() {
			var value sql.NullString
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if value.Valid && strings.TrimSpace(value.String) != "" {
				conflicts = append(conflicts, strings.TrimSpace(value.String))
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

// backupPredicateChunks 把 IN 谓词按 backupInChunkSize 拆成多段；
// 非 IN 谓词（`col = ?`）原样返回一段。
func backupPredicateChunks(predicate backupPredicate) []backupPredicate {
	if len(predicate.args) <= backupInChunkSize {
		return []backupPredicate{predicate}
	}
	// 谓词形状固定是 `col IN (?, ?, ...)`：按参数个数等分占位符即可。
	marker := " IN ("
	position := strings.Index(predicate.sql, marker)
	if position < 0 || !strings.HasSuffix(predicate.sql, ")") {
		return []backupPredicate{predicate}
	}
	column := predicate.sql[:position]
	out := make([]backupPredicate, 0, (len(predicate.args)+backupInChunkSize-1)/backupInChunkSize)
	for start := 0; start < len(predicate.args); start += backupInChunkSize {
		end := start + backupInChunkSize
		if end > len(predicate.args) {
			end = len(predicate.args)
		}
		chunk := predicate.args[start:end]
		placeholders := make([]string, 0, len(chunk))
		for range chunk {
			placeholders = append(placeholders, "?")
		}
		out = append(out, backupPredicate{
			sql:  column + marker + strings.Join(placeholders, ", ") + ")",
			args: chunk,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 文件校验（A21:257-348）
// ---------------------------------------------------------------------------

// backupValidateFile 校验备份文件自身是否自洽（**不碰数据库**）。
func backupValidateFile(file backupFile) error {
	if file.Version != backupFormatVersion {
		return backupReject("unsupported_version",
			"不支持的备份文件版本: %d；本服务端只接受 90CN 面板导出的 v%d 备份"+
				"（A21/86jp 的 v1/v2/v3 文件表名与结构不同，无法直接恢复）。",
			file.Version, backupFormatVersion)
	}
	if file.AccountID <= 0 {
		return backupReject("invalid_account_id", "备份文件中的账号 ID 无效。")
	}
	if strings.TrimSpace(file.AccountKey) == "" {
		return backupReject("invalid_account_key", "备份文件缺少游戏账号 ID（accountKey）。")
	}
	if file.CharacterIDs == nil {
		return backupReject("missing_character_ids", "备份文件缺少角色 ID 列表。")
	}
	if len(file.Tables) == 0 {
		return backupReject("empty_tables", "备份文件没有可恢复的数据表。")
	}
	seenTables := map[string]struct{}{}
	for _, dump := range file.Tables {
		name := strings.TrimSpace(dump.Name)
		if !backupIdentifierPattern.MatchString(name) {
			return backupReject("invalid_table_name", "备份文件里的表名无效: %q", dump.Name)
		}
		if _, exists := seenTables[name]; exists {
			return backupReject("duplicate_table", "备份文件存在重复表: %s", name)
		}
		seenTables[name] = struct{}{}
		if len(dump.Columns) == 0 {
			return backupReject("empty_columns", "备份表 %s 没有列。", name)
		}
		seenColumns := map[string]struct{}{}
		for _, column := range dump.Columns {
			trimmed := strings.TrimSpace(column)
			if !backupIdentifierPattern.MatchString(trimmed) {
				return backupReject("invalid_column_name", "备份表 %s 的列名无效: %q", name, column)
			}
			lower := strings.ToLower(trimmed)
			if _, exists := seenColumns[lower]; exists {
				return backupReject("duplicate_column", "备份表 %s 存在重复列: %s", name, trimmed)
			}
			seenColumns[lower] = struct{}{}
		}
		for index, row := range dump.Rows {
			if len(row) != len(dump.Columns) {
				return backupReject("row_width_mismatch",
					"备份表 %s 第 %d 行列数不一致（%d != %d）。", name, index+1, len(row), len(dump.Columns))
			}
			for _, value := range row {
				if _, err := value.toDBValue(); err != nil {
					return backupReject("invalid_value", "备份表 %s 第 %d 行: %v", name, index+1, err)
				}
			}
		}
	}
	seenCharacters := map[string]struct{}{}
	for _, id := range file.CharacterIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return backupReject("invalid_character_id", "备份文件的角色 ID 列表里有空值。")
		}
		if _, exists := seenCharacters[trimmed]; exists {
			return backupReject("duplicate_character_id", "备份文件的角色 ID 列表里有重复值: %s", trimmed)
		}
		seenCharacters[trimmed] = struct{}{}
	}
	return nil
}

// backupValidateDumpScope 校验"每一行都真的属于这个账号"（硬性要求 6 的归属核对）。
//
// 三道：
//  1. 角色清单与 dnf_characters 表里的 character_id 集合必须相等（A21:175-180）；
//  2. dnf_accounts 里的 account_id 必须等于备份声明的账号（否则备份被改过）；
//  3. 每张表的归属列（account_id / character_id）逐行落在本账号/本角色范围内 ——
//     A21 没有这一条（它信任备份文件），本仓加上它，拒绝"用合法表名把别人角色的行
//     塞进本账号"这种改写过的备份。
func backupValidateDumpScope(file backupFile, restorable map[string]backupTableSpec, accountKey string) error {
	dumps := make(map[string]backupTableDump, len(file.Tables))
	for _, dump := range file.Tables {
		dumps[dump.Name] = dump
	}
	if dump, ok := dumps["dnf_characters"]; ok {
		keys, err := dump.backupKeyValues("character_id")
		if err != nil {
			return backupReject("characters_key_error", "%v", err)
		}
		if !backupEqualKeySets(keys, file.CharacterIDs) {
			return backupReject("character_set_mismatch",
				"备份文件中的 characterIDs 与 dnf_characters 表不一致。")
		}
	}
	if dump, ok := dumps["dnf_accounts"]; ok {
		index, found := dump.backupColumnIndex("account_id")
		if !found {
			return backupReject("accounts_key_missing", "备份表 dnf_accounts 缺少 account_id 列。")
		}
		for rowIndex, row := range dump.Rows {
			if index >= len(row) {
				return backupReject("row_width_mismatch", "备份表 dnf_accounts 行列数不一致。")
			}
			value, err := row[index].toDBValue()
			if err != nil {
				return backupReject("invalid_value", "备份表 dnf_accounts 第 %d 行: %v", rowIndex+1, err)
			}
			if !backupSameKey(backupKeyText(value), accountKey) {
				return backupReject("account_mismatch",
					"备份文件里的 dnf_accounts.account_id（%s）与账号 %s 不一致。",
					backupKeyText(value), accountKey)
			}
		}
	}
	characterScope := map[string]struct{}{}
	for _, id := range file.CharacterIDs {
		characterScope[strings.TrimSpace(id)] = struct{}{}
	}
	for _, dump := range file.Tables {
		spec, ok := restorable[dump.Name]
		if !ok {
			continue
		}
		linkColumn := ""
		switch spec.link {
		case backupLinkAccount, backupLinkCharacterRoster:
			linkColumn = "account_id"
		case backupLinkCharacter:
			linkColumn = "character_id"
		default:
			continue
		}
		index, found := dump.backupColumnIndex(linkColumn)
		if !found {
			return backupReject("link_column_missing",
				"备份表 %s 缺少归属列 %s。", dump.Name, linkColumn)
		}
		for rowIndex, row := range dump.Rows {
			if index >= len(row) {
				return backupReject("row_width_mismatch", "备份表 %s 行列数不一致。", dump.Name)
			}
			value, err := row[index].toDBValue()
			if err != nil {
				return backupReject("invalid_value", "备份表 %s 第 %d 行: %v", dump.Name, rowIndex+1, err)
			}
			text := backupKeyText(value)
			if linkColumn == "account_id" {
				if !backupSameKey(text, accountKey) {
					return backupReject("row_out_of_scope",
						"备份表 %s 第 %d 行的 account_id（%s）不属于账号 %s。",
						dump.Name, rowIndex+1, text, accountKey)
				}
				continue
			}
			if _, ok := characterScope[text]; !ok {
				return backupReject("row_out_of_scope",
					"备份表 %s 第 %d 行的 character_id（%s）不在备份的角色列表内。",
					dump.Name, rowIndex+1, text)
			}
		}
	}
	return nil
}

// backupEqualKeySets 比较两个键集合（A21:1090-1093 EqualIntSets 的字符串版）。
func backupEqualKeySets(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSorted := append([]string{}, left...)
	rightSorted := append([]string{}, right...)
	sort.Strings(leftSorted)
	sort.Strings(rightSorted)
	for idx := range leftSorted {
		if !backupSameKey(leftSorted[idx], rightSorted[idx]) {
			return false
		}
	}
	return true
}

// backupSameKey 比较两个键：先按字符串，再按整数（legacy 镜像表的键是 BIGINT，
// 权威表是 VARCHAR，同一个角色在两边分别读出来会是 "12" 与 "12"、也可能 "012"）。
func backupSameKey(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return true
	}
	leftNumber, leftErr := strconv.ParseInt(left, 10, 64)
	rightNumber, rightErr := strconv.ParseInt(right, 10, 64)
	return leftErr == nil && rightErr == nil && leftNumber == rightNumber
}

// ---------------------------------------------------------------------------
// 请求体解析
// ---------------------------------------------------------------------------

// backupDecodeRestoreBody 读取恢复请求体。
//
// 为什么不用 gmtool.go 的 decodeBody：它把请求体限死在 maxBodyBytes（1 MiB），
// 而恢复请求体就是整个备份文件（几 MB 很正常），1 MiB 会让大账号永远恢复不了。
// 上限放宽到 backupRestoreBodyLimit，并且**严格**校验 JSON 里的键名：
// 恢复是破坏性操作，宁可对看不懂的文件报错，也不静默忽略未知字段。
func backupDecodeRestoreBody(w http.ResponseWriter, r *http.Request) (backupFile, error) {
	body := http.MaxBytesReader(w, r.Body, backupRestoreBodyLimit)
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return backupFile{}, backupReject("body_too_large",
				"备份文件超过 %d MiB 上限，已拒绝。", backupRestoreBodyLimit>>20)
		}
		return backupFile{}, backupReject("body_read_failed", "备份文件读取失败: %v", err)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return backupFile{}, backupReject("body_empty", "备份文件为空。")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return backupFile{}, backupReject("invalid_json", "备份文件不是合法 JSON: %v", err)
	}
	// 允许 success：导出的响应体里带它（本仓统一的成功标记），面板就是把响应原样存盘的。
	if err := backupRejectUnknownKeys(envelope,
		[]string{"version", "exportedAt", "accountID", "accountKey", "characterIDs", "tables", "success"},
		"备份文件顶层"); err != nil {
		return backupFile{}, err
	}
	if err := backupRejectUnknownDumpKeys(envelope["tables"]); err != nil {
		return backupFile{}, err
	}
	var file backupFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&file); err != nil {
		return backupFile{}, backupReject("invalid_backup", "备份文件解析失败: %v", err)
	}
	return file, nil
}

// backupRejectUnknownDumpKeys 逐层校验 tables / rows / 单元格的键名。
//
// 为什么连单元格都要查：恢复是"先删光、再按文件重建"，一个拼错的键
// （`"rows"` 写成 `"row"`、`"text"` 写成 `"tex"`）会让这一格/这张表变成空值，
// 删掉真数据之后写回一个空壳 —— 那正是硬性要求 6 禁止的"静默半应用"。
// 宁可在解析阶段具名拒绝一个看不懂的文件。
func backupRejectUnknownDumpKeys(rawTables json.RawMessage) error {
	if len(bytes.TrimSpace(rawTables)) == 0 {
		return backupReject("missing_tables", "备份文件缺少 tables 字段。")
	}
	var tables []json.RawMessage
	if err := json.Unmarshal(rawTables, &tables); err != nil {
		return backupReject("invalid_tables", "备份文件的 tables 不是数组: %v", err)
	}
	for tableIndex, rawTable := range tables {
		var table map[string]json.RawMessage
		if err := json.Unmarshal(rawTable, &table); err != nil {
			return backupReject("invalid_table", "备份文件第 %d 张表不是对象: %v", tableIndex+1, err)
		}
		if err := backupRejectUnknownKeys(table, []string{"name", "columns", "rows"},
			fmt.Sprintf("备份文件第 %d 张表", tableIndex+1)); err != nil {
			return err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(table["rows"], &rows); err != nil {
			return backupReject("invalid_rows", "备份文件第 %d 张表的 rows 不是数组: %v", tableIndex+1, err)
		}
		for rowIndex, rawRow := range rows {
			var row []json.RawMessage
			if err := json.Unmarshal(rawRow, &row); err != nil {
				return backupReject("invalid_row",
					"备份文件第 %d 张表第 %d 行不是数组: %v", tableIndex+1, rowIndex+1, err)
			}
			for cellIndex, rawCell := range row {
				var cell map[string]json.RawMessage
				if err := json.Unmarshal(rawCell, &cell); err != nil {
					return backupReject("invalid_cell",
						"备份文件第 %d 张表第 %d 行第 %d 格不是对象: %v",
						tableIndex+1, rowIndex+1, cellIndex+1, err)
				}
				if err := backupRejectUnknownKeys(cell,
					[]string{"type", "integer", "real", "text", "blob"},
					fmt.Sprintf("备份文件第 %d 张表第 %d 行第 %d 格", tableIndex+1, rowIndex+1, cellIndex+1)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// backupRejectUnknownKeys 校验 JSON 对象的键名集合（多一个未知键就具名拒绝）。
func backupRejectUnknownKeys(payload map[string]json.RawMessage, allowed []string, where string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	unknown := make([]string, 0, 2)
	for key := range payload {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return backupReject("unknown_field",
		"%s包含本服务端不认识的字段: %s（拒绝恢复）。", where, strings.Join(unknown, ", "))
}
