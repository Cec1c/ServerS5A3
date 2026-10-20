// 本文件实现 DNF 账号仓储的 MySQL 读写。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/platform/db"
)

const (
	mysqlAccountTable                  = "accounts"
	mysqlAccountHonorColumn            = "honor_exp"
	mysqlAccountHonorDDL               = "BIGINT UNSIGNED NOT NULL DEFAULT 0"
	mysqlAccountRepresentNameColumn    = "represent_account_name"
	mysqlAccountRepresentNameDDL       = "VARCHAR(64) NULL"
	mysqlAccountRepresentNameUniqueKey = "uk_dnf_accounts_represent_account_name"
)

type mysqlAccountStore struct {
	mysqlStoreBase
}

// Load 按账号 ID 从 MySQL 读取 DNF 账号记录。
func (s *mysqlAccountStore) Load(ctx context.Context, accountID string) (repository.AccountRecord, bool, error) {
	table, err := s.router.readTable(mysqlAccountTable, accountID)
	if err != nil {
		return repository.AccountRecord{}, false, err
	}
	metadataTable, err := s.router.readTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return repository.AccountRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT account_id, state, honor_exp, represent_account_name, created_at, updated_at FROM " + table + " WHERE account_id = ?")
	var record repository.AccountRecord
	var representName sql.NullString
	var createdAt, updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, accountID).Scan(
		&record.AccountID,
		&record.State,
		&record.HonorExp,
		&representName,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.AccountRecord{}, ok, scanErr
	}
	if representName.Valid {
		record.RepresentAccountName = strings.TrimSpace(representName.String)
	}
	record.Metadata, err = loadStringMap(
		ctx,
		s.router.db,
		s.router.selectQuery("SELECT entry_key, entry_value FROM "+metadataTable+" WHERE account_id = ? ORDER BY entry_key"),
		accountID,
	)
	if err != nil {
		return repository.AccountRecord{}, false, err
	}
	record.CreatedAt = scanTime(createdAt)
	record.UpdatedAt = scanTime(updatedAt)
	return repository.CloneAccount(record), true, nil
}

// Save 保存完整 DNF 账号记录到 MySQL。
func (s *mysqlAccountStore) Save(ctx context.Context, record repository.AccountRecord) error {
	accountID, err := requireRecordKey(repository.AccountKey, record, "account")
	if err != nil {
		return err
	}
	table, err := s.router.writeTable(mysqlAccountTable, accountID)
	if err != nil {
		return err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return err
	}
	updatedAt := timeOrNow(record.UpdatedAt, s.router.now)
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	representName := strings.TrimSpace(record.RepresentAccountName)
	var representNameArg any
	if representName != "" {
		representNameArg = representName
	}
	columns := []string{"account_id", "state", mysqlAccountHonorColumn, mysqlAccountRepresentNameColumn, "created_at", "updated_at"}
	args := []any{accountID, record.State, record.HonorExp, representNameArg, sqlTime(createdAt), updatedAt}
	updates := []string{
		updateValue("state"),
		updateValue(mysqlAccountHonorColumn),
		updateValue(mysqlAccountRepresentNameColumn),
		keepCreatedAt("created_at"),
		updateValue("updated_at"),
	}
	err = withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, execErr := database.ExecContext(ctx, buildUpsert(table, columns, updates), args...); execErr != nil {
			return execErr
		}
		return replaceStringMap(ctx, database, metadataTable, "account_id", accountID, record.Metadata)
	})
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return repository.ErrRepresentAccountNameExists
	}
	return err
}

func (s *mysqlAccountStore) SaveMetadataEntry(
	ctx context.Context,
	accountID string,
	key string,
	value string,
	updatedAt time.Time,
) error {
	accountID = strings.TrimSpace(accountID)
	key = strings.TrimSpace(key)
	if accountID == "" {
		return db.ErrRecordKeyRequired
	}
	if key == "" {
		return repository.ErrAccountMetadataKeyRequired
	}
	table, err := s.router.writeTable(mysqlAccountTable, accountID)
	if err != nil {
		return err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return err
	}
	updatedAt = timeOrNow(updatedAt, s.router.now)
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		updateQuery := "UPDATE " + table + " SET updated_at = ? WHERE account_id = ?"
		if _, err := database.ExecContext(ctx, updateQuery, updatedAt, accountID); err != nil {
			return err
		}
		columns := []string{"account_id", "entry_key", "entry_value"}
		updates := []string{updateValue("entry_value")}
		_, err := database.ExecContext(
			ctx,
			buildUpsert(metadataTable, columns, updates),
			accountID,
			key,
			value,
		)
		return err
	})
}

// AddAccountEpicPieceBalance 原子自增账号级史诗图鉴碎片数量，返回写入后的绝对值。
//
// 形状与 0.1.8 的 `mysqlAccountStore.AddMetadataCounter`
// （`90CN\90原神开源0.1.8\dnf90-source-oneclick-main\go-server\internal\modules\dnf\
// repository\mysql\mysql_account.go:161-215`）逐字对齐：一条 `INSERT … ON DUPLICATE
// KEY UPDATE entry_value = CAST(LEAST(<cap>, CAST(entry_value AS UNSIGNED) + ?) AS CHAR)`
// （**刻意不用 `VALUES(entry_value)`**：那种写法在 MySQL 8.0.20+ 已废弃、在 MariaDB 上
// 语义才等价，而显式 `+ ?` 两边都能跑），随后把该行读回来当权威绝对值。
// `LEAST` 把结果钉在 Int32 上限，与参考实现 `Int32 count[]` 的上限一致。
func (s *mysqlAccountStore) AddAccountEpicPieceBalance(
	ctx context.Context,
	accountID string,
	pieceID uint32,
	amount uint32,
	updatedAt time.Time,
) (uint32, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, db.ErrRecordKeyRequired
	}
	if pieceID == 0 || amount == 0 {
		return 0, repository.ErrAccountEpicPieceIDRequired
	}
	table, err := s.router.writeTable(mysqlAccountTable, accountID)
	if err != nil {
		return 0, err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return 0, err
	}
	updatedAt = timeOrNow(updatedAt, s.router.now)
	key := repository.AccountEpicPieceMetadataKey(pieceID)
	quotedValue := quoteSQLIdentifier("entry_value")
	updates := []string{
		quotedValue + " = CAST(LEAST(" + strconv.FormatUint(uint64(repository.AccountEpicPieceBalanceMax), 10) +
			", CAST(" + quotedValue + " AS UNSIGNED) + ?) AS CHAR)",
	}
	upsert := buildUpsert(metadataTable, []string{"account_id", "entry_key", "entry_value"}, updates)
	var balance uint64
	err = withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, updateErr := database.ExecContext(
			ctx,
			"UPDATE "+table+" SET updated_at = ? WHERE account_id = ?",
			updatedAt,
			accountID,
		); updateErr != nil {
			return updateErr
		}
		if _, insertErr := database.ExecContext(
			ctx,
			upsert,
			accountID,
			key,
			repository.FormatAccountEpicPieceBalance(amount),
			amount,
		); insertErr != nil {
			return insertErr
		}
		return database.QueryRowContext(
			ctx,
			"SELECT CAST(entry_value AS UNSIGNED) FROM "+metadataTable+" WHERE account_id = ? AND entry_key = ?",
			accountID,
			key,
		).Scan(&balance)
	})
	if err != nil {
		return 0, err
	}
	if balance > uint64(repository.AccountEpicPieceBalanceMax) {
		balance = uint64(repository.AccountEpicPieceBalanceMax)
	}
	return uint32(balance), nil
}

// AdvanceStoryDigestViewedLevel 单调推进账号级"剧情回顾已看过"等级。
// 与 SaveMetadataEntry 一样只写自己那一行元数据，但 upsert 的更新表达式用
// GREATEST(旧值, 新值)：重放、并发两次上报、或旧值更大的乱序请求都不会把
// 记录写回去，因此天然幂等、可重入。
func (s *mysqlAccountStore) AdvanceStoryDigestViewedLevel(
	ctx context.Context,
	accountID string,
	level uint32,
) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return db.ErrRecordKeyRequired
	}
	accountTable, err := s.router.writeTable(mysqlAccountTable, accountID)
	if err != nil {
		return err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return err
	}
	updatedAt := s.router.now().UTC()
	key := repository.AccountStoryDigestViewedLevelMetadataKey
	value := repository.FormatAccountStoryDigestViewedLevel(level)
	quotedValue := quoteSQLIdentifier("entry_value")
	updates := []string{
		quotedValue + " = GREATEST(CAST(" + quotedValue + " AS UNSIGNED), CAST(VALUES(" + quotedValue + ") AS UNSIGNED))",
	}
	upsert := buildUpsert(metadataTable, []string{"account_id", "entry_key", "entry_value"}, updates)
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, err := database.ExecContext(
			ctx,
			"UPDATE "+accountTable+" SET updated_at = ? WHERE account_id = ?",
			updatedAt,
			accountID,
		); err != nil {
			return err
		}
		_, err := database.ExecContext(ctx, upsert, accountID, key, value)
		return err
	})
}

func (s *mysqlAccountStore) FindAccountIDByRepresentName(ctx context.Context, name string) (string, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return "", false, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, nil
	}
	for _, database := range s.router.readDBs {
		table := mysqlTable(database, s.router.tablePrefix, mysqlAccountTable)
		query := "SELECT account_id FROM " + table + " WHERE represent_account_name = ? LIMIT 1"
		var accountID string
		err := s.router.db.QueryRowContext(ctx, query, name).Scan(&accountID)
		if err == nil {
			return accountID, true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		return "", false, err
	}
	return "", false, nil
}

// ListAccounts 是有界的跨账号检索（GM 面板用），与 FindAccountIDByRepresentName
// 走同一套 `router.readDBs` + `mysqlTable` 分片约定：逐库查 → 按 account_id 去重 →
// 统一排序 → 最后截断。
//
// 为什么"去重 + 重排 + 再截断"三步都不能省：同一个账号可能出现在多个读库的分片里，
// 逐库各自 LIMIT 会返回重复行、且跨库顺序无意义；只有把候选合并后统一按
// `updated_at DESC, account_id ASC` 排一次，再截到 limit，结果才与"只有一个库"时一致。
func (s *mysqlAccountStore) ListAccounts(ctx context.Context, search string, limit int) ([]repository.AccountRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	search = strings.TrimSpace(search)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	pattern := "%" + search + "%"
	records := make([]repository.AccountRecord, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, database := range s.router.readDBs {
		table := mysqlTable(database, s.router.tablePrefix, mysqlAccountTable)
		query := "SELECT account_id, state, " + mysqlAccountHonorColumn + ", " +
			mysqlAccountRepresentNameColumn + ", created_at, updated_at FROM " + table
		args := []any{}
		if search != "" {
			query += " WHERE account_id LIKE ? OR " + mysqlAccountRepresentNameColumn + " LIKE ?"
			args = append(args, pattern, pattern)
		}
		query += " ORDER BY updated_at DESC, account_id ASC LIMIT ?"
		args = append(args, limit)
		rows, err := s.router.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var record repository.AccountRecord
			var representName sql.NullString
			var createdAt, updatedAt sql.NullTime
			if err := rows.Scan(
				&record.AccountID,
				&record.State,
				&record.HonorExp,
				&representName,
				&createdAt,
				&updatedAt,
			); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, duplicate := seen[record.AccountID]; duplicate {
				continue
			}
			seen[record.AccountID] = struct{}{}
			if representName.Valid {
				record.RepresentAccountName = strings.TrimSpace(representName.String)
			}
			record.CreatedAt = scanTime(createdAt)
			record.UpdatedAt = scanTime(updatedAt)
			records = append(records, record)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		}
		return records[i].AccountID < records[j].AccountID
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}
