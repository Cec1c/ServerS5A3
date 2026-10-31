package mysql

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

// 账号存在性和指定键在同一查询中读取；不加载、克隆其它账号元数据。
func (s *mysqlAccountStore) LoadMetadataEntry(ctx context.Context, accountID, key string) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctxErr(ctx); err != nil {
		return "", false, err
	}
	accountID, key = strings.TrimSpace(accountID), strings.TrimSpace(key)
	if accountID == "" {
		return "", false, db.ErrRecordKeyRequired
	}
	if key == "" {
		return "", false, repository.ErrAccountMetadataKeyRequired
	}
	table, err := s.router.readTable(mysqlAccountTable, accountID)
	if err != nil {
		return "", false, err
	}
	metadata, err := s.router.readTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return "", false, err
	}
	query := s.router.selectQuery("SELECT m.entry_key, m.entry_value FROM " + table + " a INNER JOIN " + metadata + " m ON m.account_id = a.account_id WHERE a.account_id = ? AND m.entry_key = ?")
	var storedKey, value string
	if err := s.router.db.QueryRowContext(ctx, query, accountID, key).Scan(&storedKey, &value); err != nil {
		found, err := scanErr(err)
		return "", found, err
	}
	// MySQL校对规则可能忽略大小写或尾空格；原Metadata字典按完整键精确查找。
	if storedKey != key {
		return "", false, nil
	}
	return value, true, nil
}
