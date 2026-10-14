// Package testbenefits owns optional test-server welcome grants and gold-mail
// campaigns. Zero settings preserve normal gameplay and existing balances.
package testbenefits

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"longheng.io/server/internal/modules/dnf/repository"
)

const MaxAmount int64 = math.MaxInt32
const WelcomeReceipt = "test_benefits_welcome_cera_v1"

type Policy struct {
	Revision      uint64 `json:"revision"`
	AccountCera   int64  `json:"accountCera"`
	CharacterGold int64  `json:"characterGold"`
}

func (p Policy) Validate() error {
	if p.AccountCera < 0 || p.AccountCera > MaxAmount || p.CharacterGold < 0 || p.CharacterGold > MaxAmount {
		return errors.New("金额必须为 0 至 2147483647 的整数；0 表示关闭")
	}
	return nil
}

// WelcomeCera is called only inside character creation's locked account
// transaction. The amount is captured at registration, never at later login.
func WelcomeCera(account *repository.AccountRecord, amount int64) error {
	if amount < 0 || amount > MaxAmount {
		return errors.New("invalid welcome cera")
	}
	if amount == 0 || account.Metadata[WelcomeReceipt] != "" {
		return nil
	}
	current := int64(0)
	if raw := account.Metadata["account_cera"]; raw != "" {
		var err error
		current, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || current < 0 {
			return errors.New("invalid account cera balance")
		}
	}
	if current > MaxAmount-amount {
		return errors.New("welcome cera exceeds wallet limit")
	}
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata["account_cera"] = strconv.FormatInt(current+amount, 10)
	account.Metadata[WelcomeReceipt] = strconv.FormatInt(amount, 10)
	return nil
}

type Recipient struct {
	AccountID   string    `json:"accountId"`
	CharacterID string    `json:"characterId"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	MailID      string    `json:"mailId,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type Job struct {
	ID          string                      `json:"id"`
	Gold        int64                       `json:"gold"`
	Title       string                      `json:"title"`
	Body        string                      `json:"body"`
	CreatedAt   time.Time                   `json:"createdAt"`
	Started     bool                        `json:"started"`
	Recipients  []Recipient                 `json:"recipients"`
	CampaignID  string                      `json:"campaignId,omitempty"`
	Attachments []repository.MailAttachment `json:"attachments,omitempty"`
}

func (j Job) Validate() error {
	// Conservative UTF-8 byte limits also fit the current GBK mail fields.
	if j.Gold < 0 || j.Gold > MaxAmount || (j.Gold == 0 && len(j.Attachments) == 0) {
		return errors.New("邮件需要金币或物品；金币范围为 0 至 2147483647")
	}
	if len(j.Attachments) > 1 {
		return errors.New("每条福利规则只支持一种物品，请分别建规则")
	}
	for _, a := range j.Attachments {
		if a.ItemID <= 0 || a.ItemID > math.MaxInt32 || a.Count <= 0 || a.Count > math.MaxInt32 {
			return errors.New("物品 ID 和数量必须为 1 至 2147483647 的整数")
		}
	}
	if j.CampaignID != "" && !validCampaignID(j.CampaignID) {
		return errors.New("无效的福利规则编号")
	}
	if !utf8.ValidString(j.Title) || strings.TrimSpace(j.Title) == "" || len(j.Title) > 60 || strings.ContainsRune(j.Title, 0) || !utf8.ValidString(j.Body) || len(j.Body) > 240 || strings.ContainsRune(j.Body, 0) {
		return errors.New("标题不能为空且最多 60 字节，正文最多 240 字节（中文约 20 / 80 字）")
	}
	return nil
}

// Deliver commits the mail and a permanent receipt together. The receipt
// survives mail deletion/claim, process restart and a lost HTTP response.
func Deliver(ctx context.Context, group repository.Group, job Job, recipient Recipient) (string, error) {
	if err := job.Validate(); err != nil {
		return "", err
	}
	if len(job.ID) != 32 {
		return "", errors.New("invalid campaign id")
	}
	if _, err := hex.DecodeString(job.ID); err != nil {
		return "", err
	}
	uow, ok := group.MailboxAssets.(repository.AccountMailboxCharacterUnitOfWork)
	if !ok {
		return "", repository.ErrMailboxAssetTransactionUnavailable
	}
	key, prefix := receiptIdentity(job, recipient)
	var mailID string
	err := uow.WithinAccountMailboxCharacter(ctx, recipient.AccountID, recipient.CharacterID, func(accounts repository.AccountRepository, character repository.CharacterRecord, mailboxes repository.MailboxRepository) error {
		account, found, err := accounts.Load(ctx, recipient.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("账号不存在")
		}
		if character.AccountID != recipient.AccountID || character.CharacterID != recipient.CharacterID {
			return errors.New("角色与收件账号不匹配")
		}
		if receipt := account.Metadata[key]; receipt != "" {
			if !strings.HasPrefix(receipt, prefix) {
				return errors.New("发放记录与预览内容不一致")
			}
			mailID = strings.TrimPrefix(receipt, prefix)
			return nil
		}
		if character.Stats["delete_flag"] != 0 {
			return errors.New("角色已删除，未发送")
		}
		if !character.CreatedAt.Equal(recipient.CreatedAt) {
			return errors.New("角色已重新创建，请重新预览")
		}
		mailID, err = repository.AppendSystemMail(ctx, mailboxes, repository.SystemMailDelivery{
			RecipientCharacterID: recipient.CharacterID, Title: job.Title, Body: job.Body, Source: "test-benefits:" + deliveryID(job), Gold: job.Gold, Attachments: job.Attachments,
		})
		if err != nil {
			return err
		}
		return repository.SaveAccountMetadataEntry(ctx, accounts, account, key, prefix+mailID, time.Now().UTC())
	})
	return mailID, err
}

func validCampaignID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func deliveryID(j Job) string {
	if j.CampaignID != "" {
		return j.CampaignID
	}
	return j.ID
}
func receiptIdentity(j Job, r Recipient) (string, string) {
	key := "test_benefits_mail_" + deliveryID(j) + "_" + r.CharacterID
	values := []any{j.Gold, j.Title, j.Body, r.AccountID, r.CharacterID, r.CreatedAt.UTC()}
	// Preserve old gold-campaign fingerprints exactly. New reusable rules include
	// the character incarnation, so a deleted/recreated ID is a new character.
	if j.CampaignID != "" {
		incarnation := sha256.Sum256([]byte(r.CreatedAt.UTC().Format(time.RFC3339Nano)))
		key += "_" + hex.EncodeToString(incarnation[:8])
	}
	if len(j.Attachments) != 0 {
		values = append(values, j.Attachments)
	}
	content, _ := json.Marshal(values)
	fingerprint := sha256.Sum256(content)
	return key, hex.EncodeToString(fingerprint[:]) + ":"
}

func PreviousDelivery(account repository.AccountRecord, job Job, recipient Recipient) (string, error) {
	key, prefix := receiptIdentity(job, recipient)
	if receipt := account.Metadata[key]; receipt != "" {
		if !strings.HasPrefix(receipt, prefix) {
			return "", errors.New("福利规则内容与历史记录不一致")
		}
		return strings.TrimPrefix(receipt, prefix), nil
	}
	return "", nil
}
