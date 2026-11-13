package mysql

import (
	"context"
	"database/sql"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.MailboxUnreadReader = (*mysqlMailboxStore)(nil)

// ListUnreadHeaders reads only the flags and the mailbox_saved metadata value
// needed to reproduce mailExpired/mailboxMailSaved in Go. It never reads
// attachments, bodies, gold, or claim state.
func (s *mysqlMailboxStore) ListUnreadHeaders(ctx context.Context, characterID string) ([]repository.MailboxUnreadHeader, error) {
	characterID = strings.TrimSpace(characterID)
	mailsTable, err := s.router.readTable(mysqlMailsTable, characterID)
	if err != nil {
		return nil, err
	}
	metadataTable, err := s.router.readTable(mysqlMailMetadataTable, characterID)
	if err != nil {
		return nil, err
	}
	query := s.router.selectQuery(
		"SELECT m.mail_id, m.read_flag, m.deleted_flag, m.created_at, m.expire_at, md.metadata_value" +
			" FROM " + mailsTable + " m" +
			" LEFT JOIN " + metadataTable + " md ON md.character_id = m.character_id AND md.mail_id = m.mail_id" +
			" AND BINARY md.metadata_key = 'mailbox_saved'" +
			" WHERE m.character_id = ?",
	)
	rows, err := s.router.db.QueryContext(ctx, query, characterID)
	if err != nil {
		return nil, err
	}
	headers := make([]repository.MailboxUnreadHeader, 0)
	for rows.Next() {
		var header repository.MailboxUnreadHeader
		var createdAt, expireAt sql.NullTime
		var saved sql.NullString
		if err := rows.Scan(
			&header.MailID,
			&header.Read,
			&header.Deleted,
			&createdAt,
			&expireAt,
			&saved,
		); err != nil {
			rows.Close()
			return nil, err
		}
		header.CreatedAt = scanTime(createdAt)
		header.ExpireAt = scanTime(expireAt)
		if saved.Valid {
			header.MailboxSaved = saved.String
		}
		headers = append(headers, header)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return headers, nil
}
