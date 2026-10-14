package gmtool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// Separate GM processes must coordinate in MariaDB, not through a process map.
// The request receipt and mail commit together under the existing asset UOW.
func sendItemMailOnce(ctx context.Context, group repository.Group, character repository.CharacterRecord, requestID, fingerprint string, delivery repository.SystemMailDelivery) (string, bool, error) {
	uow, ok := group.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork)
	if !ok {
		return "", false, repository.ErrMailboxAssetTransactionUnavailable
	}
	identity := sha256.Sum256([]byte(requestID))
	key := "gm_mail_request_" + hex.EncodeToString(identity[:])
	// Include recipient/incarnation; the same request cannot silently change target.
	content := sha256.Sum256([]byte(fingerprint + "|" + character.AccountID + "|" + character.CharacterID + "|" + character.CreatedAt.UTC().Format(time.RFC3339Nano)))
	prefix := hex.EncodeToString(content[:]) + ":"
	var id string
	var replayed bool
	err := uow.WithinAccountMailboxCharacter(ctx, character.AccountID, character.CharacterID, func(accounts repository.AccountRepository, current repository.CharacterRecord, mailboxes repository.MailboxRepository) error {
		if current.Stats["delete_flag"] != 0 || !current.CreatedAt.Equal(character.CreatedAt) {
			return errors.New("角色已删除或重新创建")
		}
		account, found, err := accounts.Load(ctx, character.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("账号不存在")
		}
		if receipt := account.Metadata[key]; receipt != "" {
			if !strings.HasPrefix(receipt, prefix) {
				return errors.New("请求编号已用于其他发放内容")
			}
			id = strings.TrimPrefix(receipt, prefix)
			replayed = true
			return nil
		}
		id, err = repository.AppendSystemMail(ctx, mailboxes, delivery)
		if err != nil {
			return err
		}
		return repository.SaveAccountMetadataEntry(ctx, accounts, account, key, prefix+id, time.Now().UTC())
	})
	return id, replayed, err
}
