package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 存量旧邮件：GM 面板发放的装备附件躺在邮箱里就已经是"没有耐久证据"的形状。
// 登录期修补（打开邮箱前跑的 reconcileCurrentMailAttachmentExpirations）必须把
// 运行时 PVF 的 [durability] 补成 durability + max_durability 并落库，
// 否则玩家领取后依然是 0 耐久、依然修不了。
//
// 同时钉住修补的边界：
//   - 已经带实例耐久的附件（玩家邮寄/已修补过）不能被覆盖；
//   - 堆叠物永不写耐久；
//   - 已领取/已删除/不可解析的附件不动。
func TestReconcileCurrentMailAttachmentExpirationsAddsEquipmentDurability(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			// GM 面板邮件形状：装备附件没有任何 Extra。
			"1": {
				MailID: "1",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 9001,
					Count:  1,
				}},
			},
			// 玩家邮寄/已修补过的附件：两条耐久证据都在。
			"2": {
				MailID: "2",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 9001,
					Count:  1,
					Extra:  map[string]string{"durability": "12", "max_durability": "57"},
				}},
			},
			// 堆叠物：不许出现耐久键。
			"3": {
				MailID: "3",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 490701731,
					Count:  5,
				}},
			},
			"4": {
				MailID:  "4",
				Claimed: true,
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 9001,
					Count:  1,
				}},
			},
			"5": {
				MailID: "5",
				Attachments: []dnfrepo.MailAttachment{{
					ItemID: 999999999, // 不在运行时 PVF 里
					Count:  1,
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		pvfItemCatalog:     mustMailboxDurabilityCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || changed != 1 {
		t.Fatalf("durability repair changed=%d err=%v, want exactly the one unevidenced equipment", changed, err)
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	repaired := mailbox.Mails["1"].Attachments[0]
	if repaired.Extra["durability"] != "57" || repaired.Extra["max_durability"] != "57" {
		t.Fatalf("repaired GM equipment durability = %+v, want both keys 57", repaired.Extra)
	}
	if repaired.Extra["mailbox_pvf_kind"] != "equipment" {
		t.Fatalf("type Extra not persisted with the repair: %+v", repaired.Extra)
	}
	if got := mailbox.Mails["2"].Attachments[0].Extra["durability"]; got != "12" {
		t.Fatalf("existing instance durability overwritten: %q", got)
	}
	stackableExtra := mailbox.Mails["3"].Attachments[0].Extra
	if _, ok := stackableExtra["durability"]; ok {
		t.Fatalf("stackable attachment gained durability: %+v", stackableExtra)
	}
	if _, ok := stackableExtra["max_durability"]; ok {
		t.Fatalf("stackable attachment gained max_durability: %+v", stackableExtra)
	}
	// 类型修补（mailbox_pvf_kind / mailbox_equipment_type）也必须落库：过去
	// "changed == 0 就不保存"把这类修补整个丢掉了。
	if stackableExtra["mailbox_pvf_kind"] != "stackable" {
		t.Fatalf("type-only repair was not persisted: %+v", stackableExtra)
	}
	if extra := mailbox.Mails["4"].Attachments[0].Extra; len(extra) != 0 {
		t.Fatalf("claimed mail attachment was touched: %+v", extra)
	}
	if extra := mailbox.Mails["5"].Attachments[0].Extra; len(extra) != 0 {
		t.Fatalf("unresolvable attachment was touched: %+v", extra)
	}

	// 幂等：第二次跑没有任何可补的东西。
	again, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 19)
	if err != nil || again != 0 {
		t.Fatalf("repeat repair changed=%d err=%v, want 0", again, err)
	}
}
