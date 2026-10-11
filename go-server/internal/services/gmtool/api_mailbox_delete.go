package gmtool

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// J5：邮件单封/批量删除（含 force 保护）。
//
// 上游参考：0.1.8 `dnfbridge/gm.go:516-596`（GMDeleteMailboxMails）。本文件逐段对拍，
// 只换响应外壳（本仓惯例：恒 HTTP 200 + success 标志；时间/字段命名走前端既有风格）：
//
//	gm.go:517-520  解析角色号（本仓由 characterRequest 完成：面板主键 = dnf_character 主键）
//	gm.go:521-523  批量必须是 1..100（空/超限直接拒绝，零写入）
//	gm.go:524-526  在线守卫（本仓默认档不检查；DNFBRIDGE_GM_REQUIRE_OFFLINE=on 才拦）
//	gm.go:527-539  trim + 去重；空邮件号直接拒绝
//	gm.go:540-543  事务能力缺失 -> 失败
//	gm.go:545-549  事务内重载角色（角色必须存在）
//	gm.go:557-563  事务内重载邮箱；邮箱不存在 = 拒绝（上游 "mailbox is empty"）
//	gm.go:564-573  逐封校验：不存在 -> 拒绝；未领取且有金币/附件 -> 拒绝（force 才放行）
//	gm.go:574-582  克隆 + 逐封 Deleted=true / Read=true / Gold=0 / Attachments=nil
//	gm.go:583-586  单次 SaveMailboxFields(MailboxFieldMails) —— 整批一个事务、一个写入点
//	gm.go:587-588  回报 deleted 计数（本仓另加已删除/剩余计数，便于前端与审计对账）
//
// 与上游**唯一**的语义差异（受父任务"幂等（删已删的 = 无操作并如实回报）"要求驱动）：
// 上游对"已经删过的邮件"（map 里有、Deleted=true）和"根本不存在的邮件号"一视同仁地
// 整批报错（gm.go:566-568）。这里把两者拆开：
//   - `Deleted == true`  -> 幂等无操作：不计入删除数，出现在响应 alreadyDeleted 里，
//     整批不因此失败（重复提交/双击不会报错，也不会二次写入）；
//   - map 里根本没有   -> 仍按上游整批拒绝（编号写错/串角色必须让操作者看见），
//     响应 notFound 列出全部问题邮件号。
//
// 两者的写入行为都与上游一致：拒绝时**一个字节都不写**（校验先于克隆/保存）。
const mailboxDeleteMaxBatch = 100

// 事务回调里用来中止并向外传递"整批拒绝、零写入"的哨兵错误。
var errMailboxEmpty = errors.New("角色邮箱为空，没有可删除的邮件")

// mailboxDeleteRequest 是删除请求体。
//
// 字段用本仓前端的 camelCase（mailIds/force），同时接受上游风格的下划线别名
// mail_ids：两个都空才算空列表，避免"客户端写法不同"被误判成参数缺失。
type mailboxDeleteRequest struct {
	MailIDs    []string `json:"mailIds"`
	MailIDsAlt []string `json:"mail_ids"`
	Force      bool     `json:"force"`
}

// mailboxDeleteRejection 是"整批拒绝、零写入"的结构化原因（供 handler 如实回报）。
type mailboxDeleteRejection struct {
	reason  string
	mailIDs []string
}

func (e *mailboxDeleteRejection) Error() string {
	return fmt.Sprintf("mailbox delete rejected (%s): %s", e.reason, strings.Join(e.mailIDs, ","))
}

func (s *Server) handleMailboxDelete(w http.ResponseWriter, r *http.Request) {
	// 顺序与上游 gm.go:517-526 一致：先解析角色，再校验批量，再在线守卫，最后事务。
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[mailboxDeleteRequest](w, r)
	if !ok {
		return
	}

	requested := body.MailIDs
	if len(requested) == 0 {
		requested = body.MailIDsAlt
	}
	// 1) 批量上限（上游 gm.go:521-523：1..100）。
	if len(requested) == 0 || len(requested) > mailboxDeleteMaxBatch {
		s.logEvent("gm-mailbox-delete-rejected", "panel_id", panelID,
			"character_id", character.CharacterID, "reason", "batch_size", "force", body.Force,
			"requested", len(requested), "max", mailboxDeleteMaxBatch)
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"error": fmt.Sprintf("邮件编号必须是 1..%d 个，当前 %d 个（本批未做任何写入）。",
				mailboxDeleteMaxBatch, len(requested)),
		})
		return
	}

	// 2) 在线守卫（J0）。默认档直接放行；开关为 on 且角色在线 -> 恒 200 + success:false。
	if !s.requireOffline(w, "mailbox-delete", panelID) {
		return
	}

	// 3) trim + 去重（上游 gm.go:527-539）。
	unique := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		mailID := strings.TrimSpace(raw)
		if mailID == "" {
			s.logEvent("gm-mailbox-delete-rejected", "panel_id", panelID, "character_id", character.CharacterID,
				"reason", "empty_mail_id", "requested", len(requested))
			writeJSON(w, map[string]any{
				"success":     false,
				"characterId": panelID,
				"error":       "邮件编号不能为空字符串（本批未做任何写入）。",
			})
			return
		}
		if _, duplicate := seen[mailID]; duplicate {
			continue
		}
		seen[mailID] = struct{}{}
		unique = append(unique, mailID)
	}

	if group.MailboxAssets == nil {
		s.logEvent("gm-mailbox-delete-rejected", "panel_id", panelID, "character_id", character.CharacterID,
			"reason", "mailbox_transaction_unavailable")
		writeFailure(w, "删除邮件失败: 邮箱事务能力未就绪")
		return
	}

	var (
		deleted        []string
		alreadyDeleted []string
		remaining      int
	)
	// 4) 单事务：校验与写入在同一个事务边界内完成（上游 gm.go:545-590 用的就是
	//    这个 WithinMailboxAssets 语义），任何一封不合格 -> 整批零写入。
	err := group.MailboxAssets.WithinMailboxAssets(ctx, character.CharacterID, character.CharacterID, func(
		characters dnfrepo.CharacterRepository,
		_ dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		if _, found, loadErr := characters.Load(ctx, character.CharacterID); loadErr != nil {
			return loadErr
		} else if !found {
			return errCharacterMissing
		}
		mailbox, found, loadErr := mailboxes.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errMailboxEmpty
		}

		var missing, protected []string
		deletable := make([]string, 0, len(unique))
		for _, mailID := range unique {
			mail, exists := mailbox.Mails[mailID]
			if !exists {
				missing = append(missing, mailID)
				continue
			}
			if mail.Deleted {
				// 幂等：已删除的邮件是无操作，如实回报，不二次写入。
				alreadyDeleted = append(alreadyDeleted, mailID)
				continue
			}
			// 上游 gm.go:569：Claimable = !Claimed && (Gold > 0 || 附件非空)。
			claimable := !mail.Claimed && (mail.Gold > 0 || len(mail.Attachments) > 0)
			if claimable && !body.Force {
				protected = append(protected, mailID)
				continue
			}
			deletable = append(deletable, mailID)
		}
		if len(missing) > 0 {
			return &mailboxDeleteRejection{reason: "mail_not_found", mailIDs: missing}
		}
		if len(protected) > 0 {
			return &mailboxDeleteRejection{reason: "unclaimed_assets", mailIDs: protected}
		}
		if len(deletable) == 0 {
			// 全都是"已经删过"的：零写入，但照实回报。
			remaining = countLiveMails(mailbox)
			return nil
		}

		// 5) 克隆 + 逐封置删除态（上游 gm.go:574-582，逐字段相同）。
		mailbox = dnfrepo.CloneMailbox(mailbox)
		for _, mailID := range deletable {
			mail := mailbox.Mails[mailID]
			mail.Deleted = true
			mail.Read = true
			mail.Gold = 0
			mail.Attachments = nil
			mailbox.Mails[mailID] = mail
		}
		mailbox.UpdatedAt = time.Now().UTC()
		// 6) 整批一个写入点（上游 gm.go:584 的 SaveMailboxFields + MailboxFieldMails）。
		if saveErr := dnfrepo.SaveMailboxFields(ctx, mailboxes, mailbox, dnfrepo.MailboxFieldMails); saveErr != nil {
			return saveErr
		}
		deleted = deletable
		remaining = countLiveMails(mailbox)
		return nil
	})
	if err != nil {
		s.reportMailboxDeleteFailure(w, panelID, character.CharacterID, body.Force, unique, alreadyDeleted, err)
		return
	}

	s.logEvent("gm-mailbox-delete",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"mail_ids", strings.Join(deleted, ","),
		"deleted", len(deleted),
		"already_deleted", strings.Join(alreadyDeleted, ","),
		"already_deleted_count", len(alreadyDeleted),
		"force", body.Force,
		"requested", len(unique),
		"remaining", remaining,
	)
	writeOK(w, map[string]any{
		"characterId":    panelID,
		"force":          body.Force,
		"requested":      len(unique),
		"deleted":        len(deleted),
		"mailIds":        append([]string{}, deleted...),
		"alreadyDeleted": append([]string{}, alreadyDeleted...),
		"remaining":      remaining,
	})
}

// reportMailboxDeleteFailure 把事务里的失败翻译成前端能读懂的响应 + 一条可 grep 的
// 具名事件。拒绝一律零写入（校验先于克隆/保存），所以文案里显式写明"未做任何写入"。
func (s *Server) reportMailboxDeleteFailure(
	w http.ResponseWriter,
	panelID int,
	characterID string,
	force bool,
	unique []string,
	alreadyDeleted []string,
	err error,
) {
	payload := map[string]any{
		"success":     false,
		"characterId": panelID,
		"force":       force,
		"requested":   len(unique),
	}
	var rejection *mailboxDeleteRejection
	switch {
	case errors.As(err, &rejection):
		s.logEvent("gm-mailbox-delete-rejected",
			"panel_id", panelID,
			"character_id", characterID,
			"reason", rejection.reason,
			"mail_ids", strings.Join(rejection.mailIDs, ","),
			"force", force,
			"requested", len(unique),
			"already_deleted", strings.Join(alreadyDeleted, ","),
		)
		switch rejection.reason {
		case "mail_not_found":
			payload["error"] = fmt.Sprintf(
				"邮件 %s 不在该角色的邮箱里（编号写错或已被彻底清掉）；本批未做任何写入。",
				strings.Join(rejection.mailIDs, ", "))
			payload["notFound"] = append([]string{}, rejection.mailIDs...)
		case "unclaimed_assets":
			payload["error"] = fmt.Sprintf(
				"邮件 %s 还有未领取的金币/附件；确认要连附件一起删，请勾选「强制删除」（force=true）后重试。本批未做任何写入。",
				strings.Join(rejection.mailIDs, ", "))
			payload["protected"] = append([]string{}, rejection.mailIDs...)
		default:
			payload["error"] = "删除邮件被拒绝；本批未做任何写入。"
		}
	case errors.Is(err, errMailboxEmpty):
		s.logEvent("gm-mailbox-delete-rejected", "panel_id", panelID, "character_id", characterID,
			"reason", "mailbox_empty", "requested", len(unique))
		payload["error"] = "该角色的邮箱记录不存在（没有可删除的邮件）。"
	case errors.Is(err, errCharacterMissing):
		s.logEvent("gm-mailbox-delete-rejected", "panel_id", panelID, "character_id", characterID,
			"reason", "character_missing", "requested", len(unique))
		payload["error"] = fmt.Sprintf("角色不存在: %d", panelID)
	default:
		s.logEvent("gm-mailbox-delete-failed", "panel_id", panelID, "character_id", characterID,
			"reason", "write_failed", "error", err.Error(), "requested", len(unique))
		payload["error"] = "删除邮件失败: " + err.Error()
	}
	if len(alreadyDeleted) > 0 {
		payload["alreadyDeleted"] = append([]string{}, alreadyDeleted...)
	}
	writeJSON(w, payload)
}

// countLiveMails 数"没被删除"的邮件，用于回报操作后剩余量。
func countLiveMails(mailbox dnfrepo.MailboxRecord) int {
	count := 0
	for _, mail := range mailbox.Mails {
		if mail.Deleted {
			continue
		}
		count++
	}
	return count
}
