package dnfbridge

import (
	"context"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// GM 面板邮件（以及其它非活动结算邮件）的附件是不带到期时间的，而当前客户端把
// "附件行的到期时间为 0" 直接显示成"已超过使用期限"——操作者截图里那个春节欢庆爆竹
// （PVF 到期 2028-04-05、DB 里也就 2028）就是这么被显示成过期的。
// 这里钉住通用修复：**不看邮件来源**，只要附件是 PVF 声明了未来绝对到期日的消耗品，
// 就把那个到期日写进附件实例。
func TestReconcileCurrentMailAttachmentExpirationsRepairsGMPanelMail(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"7": {
				MailID: "7",
				// 注意：没有任何 source 元数据 —— 这就是 GM 面板发的邮件形状。
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 490005585, // [material]，PVF [expiration date] 2028-08-16 06:00
					Count:  5,
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     mustTestJoustCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 1 {
		t.Fatalf("repair changed=%d err=%v", changed, err)
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	want := time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC)
	if got := mailbox.Mails["7"].Attachments[0].ExpireAt; !got.Equal(want) {
		t.Fatalf("attachment expiry=%s want=%s", got, want)
	}
	if again, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19); err != nil || again != 0 {
		t.Fatalf("repeat repair changed=%d err=%v", again, err)
	}
}

// 不该被动的东西一个都不能动：没有 PVF 到期日、不在 PVF 里、以及已领取/已删除的邮件。
func TestReconcileCurrentMailAttachmentExpirationsLeavesOtherMailsAlone(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID: "1",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 490005593, // [random reward item]，PVF 没有 [expiration date]
					Count:  1,
				}},
			},
			"2": {
				MailID: "2",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 999999999, // 不在 PVF 里
					Count:  1,
				}},
			},
			"3": {
				MailID:  "3",
				Claimed: true,
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 490005585,
					Count:  1,
				}},
			},
			"4": {
				MailID:  "4",
				Deleted: true,
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 490005585,
					Count:  1,
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     mustTestJoustCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 0 {
		t.Fatalf("repair changed=%d err=%v, want 0 changes", changed, err)
	}
	mailbox, _, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, mailID := range []string{"1", "2", "3", "4"} {
		if got := mailbox.Mails[mailID].Attachments[0].ExpireAt; !got.IsZero() {
			t.Fatalf("mail %s attachment expiry changed to %s", mailID, got)
		}
	}
}
