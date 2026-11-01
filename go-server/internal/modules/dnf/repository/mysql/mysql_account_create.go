package mysql

import (
	"context"
	"errors"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

// 普通INSERT的主键竞争保证首次创建者唯一；元数据与账号行同事务发布。
// 不能用upsert或INSERT IGNORE：前者覆盖旧记录，后者会吞掉非主键错误。
func (s *mysqlAccountStore) CreateIfAbsent(ctx context.Context, record repository.AccountRecord) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id, err := requireRecordKey(repository.AccountKey, record, "account")
	if err != nil {
		return false, err
	}
	table, err := s.router.writeTable(mysqlAccountTable, id)
	if err != nil {
		return false, err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, id)
	if err != nil {
		return false, err
	}
	beginner, ok := s.router.db.(sqlTxBeginner)
	if !ok {
		return false, repository.ErrAccountCreationUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	database := newSQLTransactionDB(tx.Tx, s.router.db)
	updated := timeOrNow(record.UpdatedAt, s.router.now)
	created := record.CreatedAt
	if created.IsZero() {
		created = updated
	}
	var name any
	if trimmed := strings.TrimSpace(record.RepresentAccountName); trimmed != "" {
		name = trimmed
	}
	columns := []string{"account_id", "state", mysqlAccountHonorColumn, mysqlAccountRepresentNameColumn, "created_at", "updated_at"}
	_, err = database.ExecContext(ctx, buildInsert(table, columns), id, record.State, record.HonorExp, name, sqlTime(created), updated)
	if err != nil {
		var duplicate *mysqldriver.MySQLError
		if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
			return false, err
		}
		// 只把“这个账号确已存在”作为幂等成功，其他唯一键冲突仍返回原错误。
		var existing string
		if checkErr := database.QueryRowContext(ctx, "SELECT account_id FROM "+table+" WHERE account_id = ? FOR UPDATE", id).Scan(&existing); checkErr != nil {
			return false, errors.Join(err, checkErr)
		}
		return false, nil
	}
	insertMetadata := buildInsert(metadataTable, []string{"account_id", "entry_key", "entry_value"})
	for _, key := range sortedStringKeys(record.Metadata) {
		if _, err := database.ExecContext(ctx, insertMetadata, id, key, record.Metadata[key]); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
