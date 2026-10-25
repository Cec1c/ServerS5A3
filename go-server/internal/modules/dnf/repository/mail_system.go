package repository

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

var ErrSystemMailInvalid = errors.New("system mailbox delivery is invalid")

// SystemMailDelivery is a server-owned mail created by a
// committed gameplay transaction. It deliberately has no player sender or
// postage; callers must supply transaction-scoped MailboxRepository instances
// so reward consumption and the durable mail commit together.
type SystemMailDelivery struct {
	RecipientCharacterID string
	Title                string
	Body                 string
	Source               string
	RecipientName        string
	CompletionKey        string
	Attachments          []MailAttachment
	Gold                 int64
	CreatedAt            time.Time
	// ExpireAt is optional; zero preserves the ordinary unlimited system mail.
	ExpireAt time.Time
}

// AppendSystemMail appends one system mail to a character's
// mailbox. It is safe to call only through the transaction supplied by the
// relevant asset owner; this helper owns no transaction itself.
func AppendSystemMail(ctx context.Context, repo MailboxRepository, delivery SystemMailDelivery) (string, error) {
	recipientID := strings.TrimSpace(delivery.RecipientCharacterID)
	if repo == nil || recipientID == "" || strings.TrimSpace(delivery.Source) == "" || (len(delivery.Attachments) == 0 && delivery.Gold == 0) || delivery.Gold < 0 || delivery.Gold > math.MaxInt32 {
		return "", ErrSystemMailInvalid
	}
	attachments := cloneMailAttachments(delivery.Attachments)
	for _, attachment := range attachments {
		if attachment.ItemID <= 0 || attachment.Count <= 0 {
			return "", ErrSystemMailInvalid
		}
	}

	record, found, err := repo.Load(ctx, recipientID)
	if err != nil {
		return "", err
	}
	if !found {
		record = MailboxRecord{CharacterID: recipientID}
	}
	if record.Mails == nil {
		record.Mails = make(map[string]MailRecord)
	}
	mailID, err := nextSystemMailID(record.Mails)
	if err != nil {
		return "", err
	}
	now := delivery.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	expires := delivery.ExpireAt.UTC()
	if !expires.IsZero() && !expires.After(now) {
		return "", ErrSystemMailInvalid
	}
	record.Mails[mailID] = MailRecord{
		MailID:               mailID,
		SenderCharacterID:    "0",
		SenderName:           "系统",
		RecipientCharacterID: recipientID,
		RecipientName:        strings.TrimSpace(delivery.RecipientName),
		Title:                strings.TrimSpace(delivery.Title),
		Body:                 strings.TrimSpace(delivery.Body),
		Attachments:          attachments,
		Gold:                 delivery.Gold,
		CreatedAt:            now,
		ExpireAt:             expires,
		Metadata: map[string]string{
			"source":      strings.TrimSpace(delivery.Source),
			"system_mail": "true",
			"unlimited":   "true",
		},
	}
	if !expires.IsZero() {
		delete(record.Mails[mailID].Metadata, "unlimited")
	}
	if delivery.CompletionKey != "" {
		mail := record.Mails[mailID]
		mail.Metadata["completion_key"] = delivery.CompletionKey
		record.Mails[mailID] = mail
	}
	record.UpdatedAt = now
	if err := SaveMailboxFields(ctx, repo, record, MailboxFieldMails); err != nil {
		return "", err
	}
	return mailID, nil
}

func nextSystemMailID(mails map[string]MailRecord) (string, error) {
	var max uint64
	for key := range mails {
		value, err := strconv.ParseUint(strings.TrimSpace(key), 10, 32)
		if err != nil {
			continue
		}
		if value > max {
			max = value
		}
	}
	if max >= math.MaxUint32 {
		return "", ErrSystemMailInvalid
	}
	return strconv.FormatUint(max+1, 10), nil
}
