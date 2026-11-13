package mail

import (
	"context"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// UnreadCount returns the durable unread badge count without opening,
// repairing, claim-ID assigning, sorting, or writing the mailbox. Readers that
// expose MailboxUnreadReader use the narrow header projection; other
// repositories, including the memory store, fall back to a read-only Load.
//
// A zero now keeps mailExpired's existing meaning: expiry is not evaluated.
func UnreadCount(ctx context.Context, repos dnfrepo.Group, characterID string, now time.Time) (int, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return 0, ErrMailboxCharacterMissing
	}
	if repos.Mailbox == nil {
		return 0, ErrMailboxRepoMissing
	}
	if reader, ok := repos.Mailbox.(dnfrepo.MailboxUnreadReader); ok {
		headers, err := reader.ListUnreadHeaders(ctx, characterID)
		if err != nil {
			return 0, err
		}
		count := 0
		for _, header := range headers {
			mail := dnfrepo.MailRecord{
				MailID:    header.MailID,
				Read:      header.Read,
				Deleted:   header.Deleted,
				CreatedAt: header.CreatedAt,
				ExpireAt:  header.ExpireAt,
			}
			if header.MailboxSaved != "" {
				mail.Metadata = map[string]string{"mailbox_saved": header.MailboxSaved}
			}
			if mailboxMailUnread(mail, now) {
				count++
			}
		}
		return count, nil
	}

	record, found, err := repos.Mailbox.Load(ctx, characterID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	count := 0
	for _, mail := range record.Mails {
		if mailboxMailUnread(mail, now) {
			count++
		}
	}
	return count, nil
}

func mailboxMailUnread(mail dnfrepo.MailRecord, now time.Time) bool {
	return !mail.Deleted && !mailExpired(mail, now) && !mail.Read
}
