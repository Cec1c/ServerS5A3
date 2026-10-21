package mail

import (
	"context"
	"errors"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strconv"
	"strings"
)

const mailboxClaimIDsKey = "mailbox_claim_ids"
const mailboxClaimHighWaterKey = "mailbox_claim_highwater"

func mailboxExtractErrorCode(err error) byte {
	// 原生 0x29eac80：4 -> dstr 2057「物品栏空间不足」；6 没有提示分支。
	if errors.Is(err, ErrMailboxInventoryFull) {
		return 4
	}
	if errors.Is(err, ErrMailboxAlreadyClaimed) || errors.Is(err, ErrMailboxMailNotFound) || errors.Is(err, ErrMailboxExpired) {
		return 21
	}
	return mailboxErrorCode(err)
}

// 邮件正文按父信编号归组；每个附件摘要必须有独立、持久化的领取编号。
// 当前原生 0x29e39a0 按尾部父信编号归组，0x29e1200 按首部领取编号去重。
// 编号留在邮件元数据中，不改变附件的实例、附魔、徽章或其它 Extra。
func mailboxStoredClaimIDs(mail dnfrepo.MailRecord) ([]uint32, bool, error) {
	raw, ok := mail.Metadata[mailboxClaimIDsKey]
	if !ok {
		return nil, false, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]uint32, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseUint(p, 10, 32)
		if err != nil || id == 0 {
			return nil, true, fmt.Errorf("%w: invalid claim identity mail=%s", ErrMailboxInvalidRequest, mail.MailID)
		}
		ids = append(ids, uint32(id))
	}
	expected := len(mail.Attachments)
	if expected == 0 {
		expected = 1
	}
	if !mail.Claimed && !mail.Deleted && len(ids) != expected {
		return nil, true, fmt.Errorf("%w: claim identity count mail=%s", ErrMailboxInvalidRequest, mail.MailID)
	}
	return ids, true, nil
}

func mailboxSetClaimIDs(mail *dnfrepo.MailRecord, ids []uint32) {
	if mail.Metadata == nil {
		mail.Metadata = map[string]string{}
	}
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = strconv.FormatUint(uint64(id), 10)
	}
	mail.Metadata[mailboxClaimIDsKey] = strings.Join(values, ",")
}

func mailboxNeedsClaimIDs(record dnfrepo.MailboxRecord) bool {
	for _, m := range record.Mails {
		if len(m.Attachments) > 1 || m.Metadata[mailboxClaimIDsKey] != "" {
			return true
		}
	}
	return false
}

func mailboxHasStoredClaimIDs(record dnfrepo.MailboxRecord) bool {
	for _, m := range record.Mails {
		if _, ok := m.Metadata[mailboxClaimIDsKey]; ok {
			return true
		}
	}
	return false
}

func ensureMailboxClaimIDs(record *dnfrepo.MailboxRecord) (bool, error) {
	keys := make([]string, 0, len(record.Mails))
	used := map[uint32]bool{}
	var high, historicalHigh uint64
	for key, m := range record.Mails {
		keys = append(keys, key)
		parent, err := mailboxWireID(m.MailID)
		if err != nil {
			return false, err
		}
		if uint64(parent) > high {
			high = uint64(parent)
		}
		ids, _, err := mailboxStoredClaimIDs(m)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if used[id] {
				return false, fmt.Errorf("%w: duplicate claim identity %d", ErrMailboxInvalidRequest, id)
			}
			used[id] = true
			if uint64(id) > high {
				high = uint64(id)
			}
			if uint64(id) > historicalHigh {
				historicalHigh = uint64(id)
			}
		}
		if raw := m.Metadata[mailboxClaimHighWaterKey]; raw != "" {
			v, e := strconv.ParseUint(raw, 10, 32)
			if e != nil {
				return false, ErrMailboxInvalidRequest
			}
			if v > high {
				high = v
			}
			if v > historicalHigh {
				historicalHigh = v
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := mailboxWireID(record.Mails[keys[i]].MailID)
		b, _ := mailboxWireID(record.Mails[keys[j]].MailID)
		return a < b
	})
	changed := false
	for _, key := range keys {
		m := record.Mails[key]
		if _, exists, err := mailboxStoredClaimIDs(m); err != nil {
			return false, err
		} else if exists {
			continue
		}
		parent, _ := mailboxWireID(m.MailID)
		n := len(m.Attachments)
		if n == 0 {
			n = 1
		}
		ids := make([]uint32, n)
		for i := range ids {
			id := parent
			// 高水位以内包含已领走、已从附件数组删除的编号，不能再分给新父信。
			if i != 0 || used[id] || uint64(id) <= historicalHigh {
				if high >= math.MaxUint32 {
					return false, ErrMailboxInvalidRequest
				}
				high++
				id = uint32(high)
			}
			used[id] = true
			ids[i] = id
		}
		m.Metadata = cloneStringMap(m.Metadata)
		mailboxSetClaimIDs(&m, ids)
		m.Metadata[mailboxClaimHighWaterKey] = strconv.FormatUint(high, 10)
		record.Mails[key] = m
		changed = true
	}
	return changed, nil
}

// 只在确实需要分配编号时进入邮件资产事务，重读后保存，避免与奖励投递相互覆盖。
func (o Owner) prepareClaimIDsForOpen(ctx context.Context, record dnfrepo.MailboxRecord) (dnfrepo.MailboxRecord, error) {
	if !mailboxNeedsClaimIDs(record) {
		return record, nil
	}
	probe := dnfrepo.CloneMailbox(record)
	changed, err := ensureMailboxClaimIDs(&probe)
	if err != nil {
		return record, err
	}
	if !changed {
		return record, nil
	}
	if o.repos.MailboxAssets == nil {
		return record, ErrMailboxAssetsUOW
	}
	err = o.repos.MailboxAssets.WithinMailboxAssets(ctx, record.CharacterID, record.CharacterID, func(_ dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, boxes dnfrepo.MailboxRepository) error {
		fresh, found, e := boxes.Load(ctx, record.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return ErrMailboxMailNotFound
		}
		changed, e := ensureMailboxClaimIDs(&fresh)
		if e != nil {
			return e
		}
		if changed {
			fresh.UpdatedAt = o.clock()
			if e = dnfrepo.SaveMailboxFields(ctx, boxes, fresh, dnfrepo.MailboxFieldMails); e != nil {
				return e
			}
		}
		record = fresh
		return nil
	})
	return record, err
}

type mailboxClaimObject struct{ MailID, ClaimID uint32 }
type mailboxClaimSelection struct {
	key     string
	mail    dnfrepo.MailRecord
	id      uint32
	partial bool
}

// 原生既可单领某件附件，也可发送一批领取编号；不能把附件编号当成父信编号。
func selectMailboxClaims(record dnfrepo.MailboxRecord, ids []uint32) ([]mailboxClaimSelection, error) {
	byID := map[uint32]mailboxClaimSelection{}
	for key, m := range record.Mails {
		claims, stored, err := mailboxStoredClaimIDs(m)
		if err != nil {
			return nil, err
		}
		if !stored {
			parent, e := mailboxWireID(m.MailID)
			if e != nil {
				return nil, e
			}
			claims = []uint32{parent}
		}
		for i, id := range claims {
			if _, duplicate := byID[id]; duplicate {
				return nil, ErrMailboxInvalidRequest
			}
			selected := m
			partial := stored && !m.Claimed && !m.Deleted && len(m.Attachments) > 0
			if partial {
				selected.Attachments = []dnfrepo.MailAttachment{m.Attachments[i]}
				if i != 0 {
					selected.Gold = 0
				}
			}
			byID[id] = mailboxClaimSelection{key: key, mail: selected, id: id, partial: partial}
		}
	}
	out := make([]mailboxClaimSelection, 0, len(ids))
	seen := map[uint32]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, ErrMailboxInvalidRequest
		}
		seen[id] = true
		s, ok := byID[id]
		if !ok {
			return nil, ErrMailboxMailNotFound
		}
		out = append(out, s)
	}
	return out, nil
}

func settleMailboxClaim(mail *dnfrepo.MailRecord, selection mailboxClaimSelection) error {
	if !selection.partial {
		mail.Gold = 0
		mail.Attachments = nil
		mail.Claimed = true
		return nil
	}
	ids, _, err := mailboxStoredClaimIDs(*mail)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if id == selection.id {
			if mail.Gold < selection.mail.Gold {
				return ErrMailboxInvalidRequest
			}
			mail.Gold -= selection.mail.Gold
			mail.Attachments = append(mail.Attachments[:i:i], mail.Attachments[i+1:]...)
			remaining := append(ids[:i:i], ids[i+1:]...)
			if len(remaining) == 0 {
				mail.Claimed = true
			} else {
				mailboxSetClaimIDs(mail, remaining)
			}
			return nil
		}
	}
	return ErrMailboxMailNotFound
}
