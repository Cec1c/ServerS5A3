package gmtool

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 邮箱只读快照（对应上游 0.1.8 dnfbridge/gm.go 的 GMMailbox + gmMailboxSnapshot）。
//
// 纯读：不写库、不需要开关。行数/字段口径与上游一致，只是把响应外壳换成本仓惯例
// （恒 HTTP 200 + success 标志），时间字段换成本仓前端统一处理的 Unix 秒。
//
// 口径要点（逐条对应上游 gm.go:597-640）：
//   - 跳过 Deleted 邮件；
//   - MailID 为空时回退 map key；
//   - Claimable = !Claimed && (Gold > 0 || 附件非空)；
//   - Saved 读 Metadata["mailbox_saved"] == "true"（大小写不敏感、两端裁剪）；
//   - 排序：CreatedAt 新的在前；相同则 MailID 按数值降序，数值不可解析时按字符串降序。
func (s *Server) handleMailboxSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	record, found, err := group.Mailbox.Load(ctx, character.CharacterID)
	if err != nil {
		writeFailure(w, "读取邮箱失败: "+err.Error())
		return
	}
	if !found {
		record = dnfrepo.MailboxRecord{CharacterID: character.CharacterID}
	}

	mails := make([]map[string]any, 0, len(record.Mails))
	claimableCount := 0
	unreadCount := 0
	attachmentCount := 0
	for key, mail := range record.Mails {
		if mail.Deleted {
			continue
		}
		mailID := strings.TrimSpace(mail.MailID)
		if mailID == "" {
			mailID = key
		}
		attachments := make([]map[string]any, 0, len(mail.Attachments))
		for _, attachment := range mail.Attachments {
			attachments = append(attachments, map[string]any{
				"itemId": attachment.ItemID,
				// 名称来自本仓 PVF 物品索引；索引里没有就留空，前端不显示（不造名字）。
				"name":     s.items.name(uint32(attachment.ItemID)),
				"count":    attachment.Count,
				"bind":     attachment.Bind,
				"expireAt": unixOrZero(attachment.ExpireAt),
			})
		}
		claimable := !mail.Claimed && (mail.Gold > 0 || len(mail.Attachments) > 0)
		if claimable {
			claimableCount++
		}
		if !mail.Read {
			unreadCount++
		}
		attachmentCount += len(mail.Attachments)
		mails = append(mails, map[string]any{
			"mailId":      mailID,
			"senderName":  mail.SenderName,
			"title":       mail.Title,
			"body":        mail.Body,
			"gold":        mail.Gold,
			"attachments": attachments,
			"read":        mail.Read,
			"claimed":     mail.Claimed,
			"claimable":   claimable,
			"saved":       strings.EqualFold(strings.TrimSpace(mail.Metadata["mailbox_saved"]), "true"),
			"createdAt":   unixOrZero(mail.CreatedAt),
			"expireAt":    unixOrZero(mail.ExpireAt),
		})
	}
	sortMailboxRows(mails)

	writeOK(w, map[string]any{
		"characterId":     panelID,
		"count":           len(mails),
		"claimableCount":  claimableCount,
		"unreadCount":     unreadCount,
		"attachmentCount": attachmentCount,
		"mails":           mails,
	})
}

// sortMailboxRows 复刻上游 gmMailboxSnapshot 的排序：CreatedAt 降序，
// 同刻按 MailID 数值降序，数值不可解析时退化为字符串降序。
func sortMailboxRows(rows []map[string]any) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		leftCreated, _ := left["createdAt"].(int64)
		rightCreated, _ := right["createdAt"].(int64)
		if leftCreated != rightCreated {
			return leftCreated > rightCreated
		}
		leftID, _ := left["mailId"].(string)
		rightID, _ := right["mailId"].(string)
		leftNumber, leftErr := strconv.ParseUint(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightID, 10, 64)
		if leftErr == nil && rightErr == nil {
			return leftNumber > rightNumber
		}
		return leftID > rightID
	})
}
