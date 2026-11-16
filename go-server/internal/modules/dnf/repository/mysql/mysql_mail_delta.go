package mysql

import (
	"bytes"
	"context"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func sameMailHeader(a, b r.MailRecord) bool {
	return a.SenderCharacterID == b.SenderCharacterID && a.SenderName == b.SenderName &&
		a.RecipientCharacterID == b.RecipientCharacterID && a.RecipientName == b.RecipientName &&
		a.Title == b.Title && a.Body == b.Body && a.Gold == b.Gold && a.Read == b.Read &&
		a.Claimed == b.Claimed && a.Deleted == b.Deleted && a.CreatedAt.Equal(b.CreatedAt) && a.ExpireAt.Equal(b.ExpireAt)
}

func sameMailAttachment(a, b r.MailAttachment) bool {
	return a.ItemID == b.ItemID && a.Count == b.Count && a.Bind == b.Bind &&
		a.ExpireAt.Equal(b.ExpireAt) && (a.RawEntry == nil) == (b.RawEntry == nil) && bytes.Equal(a.RawEntry, b.RawEntry)
}

// updateMailRows retains the full aggregate replacement contract. The caller
// holds the parent mailbox lock and has read all four child tables in the same
// physical transaction. Keys omitted by the new aggregate are still deleted.
func updateMailRows(ctx context.Context, db SQLDB, mails, metadata, attachments, extras, owner string, before, after map[string]r.MailRecord) error {
	for _, id := range sortedStringKeys(before) {
		if _, exists := after[id]; exists {
			continue
		}
		for _, table := range []string{extras, attachments, metadata, mails} {
			if _, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND mail_id = ?", owner, id); err != nil {
				return err
			}
		}
	}
	for _, id := range sortedStringKeys(after) {
		next := after[id]
		prev, exists := before[id]
		if !exists {
			if err := insertMails(ctx, db, mails, metadata, attachments, extras, owner, map[string]r.MailRecord{id: next}); err != nil {
				return err
			}
			continue
		}
		if !sameMailHeader(prev, next) {
			query := "UPDATE " + mails + " SET sender_character_id = ?, sender_name = ?, recipient_character_id = ?, recipient_name = ?, title = ?, body = ?, gold = ?, read_flag = ?, claimed_flag = ?, deleted_flag = ?, created_at = ?, expire_at = ? WHERE character_id = ? AND mail_id = ?"
			if _, err := db.ExecContext(ctx, query, next.SenderCharacterID, next.SenderName, next.RecipientCharacterID, next.RecipientName, next.Title, next.Body, next.Gold, next.Read, next.Claimed, next.Deleted, sqlTime(next.CreatedAt), sqlTime(next.ExpireAt), owner, id); err != nil {
				return err
			}
		}
		if err := updateMailStringRows(ctx, db, metadata, "metadata_key", "metadata_value", []string{"character_id", "mail_id"}, []any{owner, id}, prev.Metadata, next.Metadata); err != nil {
			return err
		}
		for index := len(next.Attachments); index < len(prev.Attachments); index++ {
			for _, table := range []string{extras, attachments} {
				if _, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND mail_id = ? AND attachment_index = ?", owner, id, index); err != nil {
					return err
				}
			}
		}
		for index, nextAttachment := range next.Attachments {
			var previous r.MailAttachment
			if index >= len(prev.Attachments) {
				query := "INSERT INTO " + attachments + " (character_id, mail_id, attachment_index, item_id, item_count, bind_flag, expire_at, raw_entry) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
				if _, err := db.ExecContext(ctx, query, owner, id, index, nextAttachment.ItemID, nextAttachment.Count, nextAttachment.Bind, sqlTime(nextAttachment.ExpireAt), nextAttachment.RawEntry); err != nil {
					return err
				}
			} else {
				previous = prev.Attachments[index]
				if !sameMailAttachment(previous, nextAttachment) {
					query := "UPDATE " + attachments + " SET item_id = ?, item_count = ?, bind_flag = ?, expire_at = ?, raw_entry = ? WHERE character_id = ? AND mail_id = ? AND attachment_index = ?"
					if _, err := db.ExecContext(ctx, query, nextAttachment.ItemID, nextAttachment.Count, nextAttachment.Bind, sqlTime(nextAttachment.ExpireAt), nextAttachment.RawEntry, owner, id, index); err != nil {
						return err
					}
				}
			}
			if err := updateMailStringRows(ctx, db, extras, "extra_key", "extra_value", []string{"character_id", "mail_id", "attachment_index"}, []any{owner, id, index}, previous.Extra, nextAttachment.Extra); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateMailStringRows(ctx context.Context, db SQLDB, table, keyColumn, valueColumn string, ownerColumns []string, ownerValues []any, before, after map[string]string) error {
	where := strings.Join(ownerColumns, " = ? AND ") + " = ? AND " + keyColumn + " = ?"
	for _, key := range sortedStringKeys(before) {
		if _, exists := after[key]; !exists {
			args := append(append([]any(nil), ownerValues...), key)
			if _, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+where, args...); err != nil {
				return err
			}
		}
	}
	columns := append(append([]string(nil), ownerColumns...), keyColumn, valueColumn)
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ") + ")"
	for _, key := range sortedStringKeys(after) {
		old, exists := before[key]
		if exists && old == after[key] {
			continue
		}
		if exists {
			args := append([]any{after[key]}, ownerValues...)
			args = append(args, key)
			if _, err := db.ExecContext(ctx, "UPDATE "+table+" SET "+valueColumn+" = ? WHERE "+where, args...); err != nil {
				return err
			}
			continue
		}
		// INSERT must retain the legacy unique-key error for distinct Go keys
		// that compare equal under the actual SQL collation; never upsert them.
		args := append(append([]any(nil), ownerValues...), key, after[key])
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}
