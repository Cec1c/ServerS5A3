package mail

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场（2026-09-28，角色 6 `test78`，账号 `dnf:10011`）：
//
//	select claimed_flag, deleted_flag, count(*) from dnf_mails where character_id=6 ...
//	  -> (claimed=1, deleted=0): 2 rows      <-- 玩家报的"空邮件"
//
// 那两封信（mail 2 / mail 5，09-24 领取）在库里就是
// `claimed_flag=1, deleted_flag=0, gold=0` 且没有任何附件行：一封已经兑现干净的空信。
//
// 改动前这条路是死胡同：`Owner.Claim` 看见 `Claimed` 就跳过，一封都没选中时回
// `ErrMailboxAlreadyClaimed`（客户端 0x04）；而客户端单封领取（op95 count=1）不会像
// 「全部接受」那样再补一条 op134 删除 ⇒ 这封信永远留在邮箱里，怎么点「领取」都没反应。
//
// 这个用例把现场形状原样摆进内存仓库，钉住默认档（claimed_letter.go）的行为：
// 领取一封已经领取过的空信 = 结清它（成功 + 移出收件箱 + 空页）。
func TestMailboxClaimSettlesStuckEmptyLetter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "6",
		Mails: map[string]dnfrepo.MailRecord{
			// 现场那两行的字段形状：已领取、已读、无金币、无附件、未删除。
			"2": {
				MailID:   "2",
				Claimed:  true,
				Read:     true,
				Metadata: map[string]string{"source": "dnf90-gm-tool", "system_mail": "true", "unlimited": "true"},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 6,
		Repositories:        repos,
		Body:                currentMailIDList(2),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	// 必须成功（不是 0x04「已领取」），且补发的 0x61 页里已经没有这封信。
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(2))

	box, found, err := repos.Mailbox.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	settled := box.Mails["2"]
	if !settled.Claimed || !settled.Deleted || settled.Gold != 0 || len(settled.Attachments) != 0 {
		t.Fatalf("stuck empty letter = %+v, want it settled (deleted) with no assets", settled)
	}

	// 打开邮箱不再把它画出来：玩家下一次打开邮箱就是干净的一页。
	opened, err := NewOwner(repos).Open(ctx, "6")
	if err != nil {
		t.Fatalf("open mailbox: %v", err)
	}
	if opened.Total != 0 || len(opened.Mails) != 0 || opened.Claimable != 0 {
		t.Fatalf("mailbox still lists the settled letter: total=%d mails=%+v", opened.Total, opened.Mails)
	}
}

// 操作者的真实动作：**单封领取**（现场 packet_log 里 op95 全是 count=1 的单封领取）。
// 「全部接受」会自己补一条 op134 删除，但点开一封信再点收取不会 —— 领完的信必须由
// 服务端结清，否则邮箱里会慢慢积满空邮件（现场角色 6 的 181 就是这样留下的）。
func TestMailboxClaimFinishesFreshlyClaimedLetter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "6",
		Mails: map[string]dnfrepo.MailRecord{
			"181": {
				MailID:      "181",
				Metadata:    map[string]string{"source": "dnf90-gm-tool", "system_mail": "true"},
				Attachments: []dnfrepo.MailAttachment{{ItemID: 490006368, Count: 1}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 6,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(181),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(181))

	inventory, found, err := repos.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if stack := inventory.Slots["0:3"]; stack.ItemID != 490006368 || stack.Count != 1 {
		t.Fatalf("claim did not deliver the attachment: %+v", stack)
	}
	box, _, err := repos.Mailbox.Load(ctx, "6")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	finished := box.Mails["181"]
	if !finished.Claimed || !finished.Deleted || finished.Gold != 0 || len(finished.Attachments) != 0 {
		t.Fatalf("freshly claimed letter = %+v, want it finished with no empty receipt left", finished)
	}
	// 再打开邮箱：一封都不剩（客户端不会再画出空邮件）。
	opened, err := NewOwner(repos).Open(ctx, "6")
	if err != nil {
		t.Fatalf("open mailbox: %v", err)
	}
	if opened.Total != 0 || len(opened.Mails) != 0 {
		t.Fatalf("mailbox still lists the finished letter: total=%d mails=%+v", opened.Total, opened.Mails)
	}
}

// 旧实现遗留的"已领取 + 附件行还在"形状（clearUnclaimableMailboxAssets 的注释里写的就是
// 这一族：旧领取实现把转出的资产留在了信上）。默认档对这一族的口径是**不重复发货**：
// 资产在旧实现里已经转出，这里只把投影结清 —— 与 Owner.Open 的
// clearUnclaimableMailboxAssets 同口径，也保证不会凭空多发一次道具。
//
// 同时钉住"领取完成后持久化的附件行必须没了"：领取之后 Load 出来的聚合里，
// 这封信既没有附件、也没有金币，且已经从收件箱移除。
func TestMailboxClaimSettlesLegacyClaimedLetterWithoutRedelivering(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "6",
		Mails: map[string]dnfrepo.MailRecord{
			"2": {
				MailID:      "2",
				Claimed:     true,
				Read:        true,
				Gold:        7,
				Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 3, Bind: true}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 6,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(2),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(2))

	// 不重复发货：背包里不该出现那件附件，金币也不该再加 7。
	inventory, found, err := repos.Inventory.Load(ctx, "6")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if found && len(inventory.Slots) != 0 {
		t.Fatalf("legacy claimed attachment was re-delivered: %+v", inventory.Slots)
	}
	character, _, err := repos.Character.Load(ctx, "6")
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	if character.Stats["gold"] != 0 {
		t.Fatalf("legacy claimed gold was re-delivered: %d", character.Stats["gold"])
	}

	// 持久化形状：附件行与金币都必须没了，信也移出了收件箱。
	box, _, err := repos.Mailbox.Load(ctx, "6")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	settled := box.Mails["2"]
	if !settled.Claimed || !settled.Deleted || settled.Gold != 0 || len(settled.Attachments) != 0 {
		t.Fatalf("legacy claimed letter = %+v, want deleted with no durable assets", settled)
	}
}

// 收藏（`mailbox_saved=true`）的信绝不能被结清掉：玩家点过收藏就是"我要留着这封信"，
// 自动移除等于替他删信。这里的领取是"发货但保留"（金币/附件照发，信留在收件箱）。
func TestMailboxClaimKeepsSavedClaimedLetter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "6",
		Mails: map[string]dnfrepo.MailRecord{
			"9": {MailID: "9", Claimed: true, Read: true, Metadata: map[string]string{"mailbox_saved": "true"}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 6,
		Repositories:        repos,
		Body:                currentMailIDList(9),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractSuccessWithSnapshot(t, got, currentExtractAck(9))
	box, _, _ := repos.Mailbox.Load(ctx, "6")
	if kept := box.Mails["9"]; kept.Deleted || !kept.Claimed {
		t.Fatalf("saved claimed letter = %+v, want it kept in the inbox", kept)
	}
}

// 回退档（`DNFBRIDGE_MAILBOX_CLAIMED_LETTER=keep`）：逐字节回到改动前的行为 ——
// 已领取的信一律跳过、一封都没选中时回 0x04、空信留在收件箱里。这就是线上出问题时
// 那条"一条 env 换档"的退路。
func TestMailboxClaimedLetterKeepModeRestoresLegacyBehaviour(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "6",
		Mails: map[string]dnfrepo.MailRecord{
			"2": {MailID: "2", Claimed: true, Read: true},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	legacy := NewOwner(repos).WithClaimedLetterMode(ClaimedLetterKeep)
	if _, err := legacy.Claim(ctx, "6", ExtractRequest{MailIDs: []uint32{2}}); err != ErrMailboxAlreadyClaimed {
		t.Fatalf("keep mode claim error = %v, want %v", err, ErrMailboxAlreadyClaimed)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "6")
	if legacyMail := box.Mails["2"]; legacyMail.Deleted {
		t.Fatalf("keep mode settled the letter: %+v", legacyMail)
	}

	// 同一条 env（经 NewOwner 读环境）也必须落到 keep 档，并且端到端回 0x04。
	t.Setenv(MailboxClaimedLetterEnv, "keep")
	if got := NewOwner(repos).claimedLetter; got != ClaimedLetterKeep {
		t.Fatalf("env keep selected %q, want %q", got, ClaimedLetterKeep)
	}
	legacyResult, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 6,
		Repositories:        repos,
		Body:                currentMailIDList(2),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, legacyResult, dnfenum.CmdPacketMailboxExtractItem, []byte{0x00, 0x04})
}

// 删除一封信必须把它的可领取资产一起从聚合里清掉：`MailboxFieldMails` 在 MySQL 侧是
// 整表重写（replaceMails 先 DELETE 四张表再按聚合重插），聚合里留着附件，行就会跟着
// 被重新插回去 —— 现场角色 3 的 mail 2/5 就是"deleted_flag=1 却还挂着附件行"，
// 也正是把排障带偏的那个形状。回退档下这条清理关闭（逐字节回到改动前）。
func TestMailboxChangeStateDeleteDropsLetterAssets(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		mode        ClaimedLetterMode
		wantAssets  bool
		wantDeleted bool
	}{
		{name: "finish drops the asset projection", mode: ClaimedLetterFinish},
		{name: "keep retains it byte for byte", mode: ClaimedLetterKeep, wantAssets: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "3", Name: "t3"})
			if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
				CharacterID: "3",
				Mails: map[string]dnfrepo.MailRecord{
					"2": {
						MailID:      "2",
						Claimed:     true,
						Read:        true,
						Attachments: []dnfrepo.MailAttachment{{ItemID: 415580009, Count: 1}},
					},
				},
			}); err != nil {
				t.Fatalf("save mailbox: %v", err)
			}
			owner := NewOwner(repos).WithClaimedLetterMode(testCase.mode)
			if err := owner.ChangeState(ctx, "3", ChangeStateRequest{MailIDs: []uint32{2}, Status: 0}); err != nil {
				t.Fatalf("delete mail: %v", err)
			}
			box, _, err := repos.Mailbox.Load(ctx, "3")
			if err != nil {
				t.Fatalf("load mailbox: %v", err)
			}
			deleted := box.Mails["2"]
			if !deleted.Deleted || deleted.Read != true || deleted.Gold != 0 {
				t.Fatalf("deleted letter = %+v, want deleted+read+no gold", deleted)
			}
			if hasAssets := len(deleted.Attachments) != 0; hasAssets != testCase.wantAssets {
				t.Fatalf("deleted letter attachments = %+v, want assets=%v", deleted.Attachments, testCase.wantAssets)
			}
		})
	}
}

// 删除一封已经被结清（移出收件箱）的信必须是幂等无操作：客户端「全部接受」在 op95
// 之后会照自己的选择列表补发一条 op134 status=0，如果那批里混着一封刚刚被结清的信，
// 旧代码会整批回 0x03（邮件不存在）—— 一封已经不在的信能让整批删除全部失败。
// 回退档保留旧的 0x03。
func TestMailboxChangeStateDeleteOfSettledLetterIsIdempotent(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mode      ClaimedLetterMode
		wantError bool
	}{
		{name: "finish answers success", mode: ClaimedLetterFinish},
		{name: "keep answers mail not found", mode: ClaimedLetterKeep, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "6", Name: "test78"})
			if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
				CharacterID: "6",
				Mails: map[string]dnfrepo.MailRecord{
					"2": {MailID: "2", Claimed: true, Read: true},
				},
			}); err != nil {
				t.Fatalf("save mailbox: %v", err)
			}
			owner := NewOwner(repos).WithClaimedLetterMode(testCase.mode)
			if err := owner.ChangeState(ctx, "6", ChangeStateRequest{MailIDs: []uint32{2}, Status: 0}); err != nil {
				t.Fatalf("first delete: %v", err)
			}
			err := owner.ChangeState(ctx, "6", ChangeStateRequest{MailIDs: []uint32{2}, Status: 0})
			if testCase.wantError {
				if err != ErrMailboxMailNotFound {
					t.Fatalf("keep mode second delete error = %v, want %v", err, ErrMailboxMailNotFound)
				}
				return
			}
			if err != nil {
				t.Fatalf("idempotent delete error = %v, want nil", err)
			}
		})
	}
}

// 打开邮箱时的存量大扫除：默认档连"已删除却还挂着附件行"的存量形状一起清
// （现场角色 3 的 mail 2/5 就是这个形状，也正是把排障引到跨角色 join 上的那两行）。
// 已删除的信再也领不到资产（Claim 回 not-found、ChangeState 不会删第二次），
// 留着这些行只会让 dnf_mail_attachments 里出现"某个角色的信还挂着附件"的假象。
// 回退档不动这些行（逐字节回到改动前）。
func TestMailboxOpenClearsAttachmentRowsOfDeletedLetters(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mode     ClaimedLetterMode
		wantRows bool
	}{
		{name: "finish drops deleted letter rows", mode: ClaimedLetterFinish},
		{name: "keep retains deleted letter rows", mode: ClaimedLetterKeep, wantRows: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "3", Name: "t3"})
			if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
				CharacterID: "3",
				Mails: map[string]dnfrepo.MailRecord{
					"2": {
						MailID:      "2",
						Deleted:     true,
						Read:        true,
						Attachments: []dnfrepo.MailAttachment{{ItemID: 415580009, Count: 1}},
					},
					"9": {MailID: "9", Attachments: []dnfrepo.MailAttachment{{ItemID: 2683901, Count: 1}}},
				},
			}); err != nil {
				t.Fatalf("save mailbox: %v", err)
			}
			owner := NewOwner(repos).WithClaimedLetterMode(testCase.mode)
			if _, err := owner.Open(ctx, "3"); err != nil {
				t.Fatalf("open mailbox: %v", err)
			}
			box, _, err := repos.Mailbox.Load(ctx, "3")
			if err != nil {
				t.Fatalf("load mailbox: %v", err)
			}
			if hasRows := len(box.Mails["2"].Attachments) != 0; hasRows != testCase.wantRows {
				t.Fatalf("deleted letter attachments = %+v, wantRows=%v", box.Mails["2"].Attachments, testCase.wantRows)
			}
			// 未领取的活信绝不能被这条大扫除碰到。
			if live := box.Mails["9"]; len(live.Attachments) != 1 || live.Deleted {
				t.Fatalf("live letter was touched by the repair: %+v", live)
			}
		})
	}
}

// 开关归一化：只有明确的回退写法才是 keep，其余（含空值/写错）都按修好的行为走。
func TestMailboxClaimedLetterModeNormalization(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		want ClaimedLetterMode
	}{
		{raw: "", want: ClaimedLetterFinish},
		{raw: "  ", want: ClaimedLetterFinish},
		{raw: "finish", want: ClaimedLetterFinish},
		{raw: "whatever-typo", want: ClaimedLetterFinish},
		{raw: "keep", want: ClaimedLetterKeep},
		{raw: " KEEP ", want: ClaimedLetterKeep},
		{raw: "legacy", want: ClaimedLetterKeep},
		{raw: "off", want: ClaimedLetterKeep},
		{raw: "0", want: ClaimedLetterKeep},
		{raw: "false", want: ClaimedLetterKeep},
		{raw: "no", want: ClaimedLetterKeep},
	} {
		if got := normalizeClaimedLetterMode(testCase.raw); got != testCase.want {
			t.Fatalf("normalizeClaimedLetterMode(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	// 默认（env 未设）必须是修好的那一档。
	t.Setenv(MailboxClaimedLetterEnv, "")
	if got := ClaimedLetterModeFromEnv(); got != ClaimedLetterFinish {
		t.Fatalf("default mode = %q, want %q", got, ClaimedLetterFinish)
	}
	if got := ClaimedLetterModeFromEnv().String(); got != "finish" {
		t.Fatalf("default mode name = %q, want finish", got)
	}
	if got := (ClaimedLetterMode("")).String(); got != "finish" {
		t.Fatalf("zero mode name = %q, want finish", got)
	}
}
