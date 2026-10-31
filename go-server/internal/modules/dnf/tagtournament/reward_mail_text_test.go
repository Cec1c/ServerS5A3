package tagtournament

import (
	"context"
	"fmt"
	"testing"
)

func TestStageRewardMailExplainsRoundAndKeepsSystemSender(t *testing.T) {
	for _, mode := range []Mode{Normal, Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			o, g, now := ownerFixture(t)
			r := admitRun(t, o, mode)
			for stage := 1; stage <= len(r.admission.plan.Encounters); stage++ {
				encounter := clearRound(t, r, now)
				result, err := o.Settle(context.Background(), r)
				if err != nil || len(result.MailIDs) != 1 {
					t.Fatal(stage, result, err)
				}
				box, _, _ := g.Mailbox.Load(context.Background(), "1")
				mail := box.Mails[result.MailIDs[0]]
				title := fmt.Sprintf("帝国竞技场第%d回合奖励", stage)
				body := fmt.Sprintf("您已通关帝国竞技场第%d回合，获得本回合奖励。\n请查收附件。", stage)
				if mode == Hard {
					body = fmt.Sprintf("您已通关帝国竞技场困难模式第%d回合，获得本回合奖励。\n请查收附件。", stage)
				} else if encounter.Optional {
					title = "帝国竞技场竞技之王奖励"
					body = "您已击败帝国竞技场竞技之王，获得竞技之王通关奖励。\n请查收附件。"
				}
				if mail.Title != title || mail.Body != body || mail.SenderName != "系统" || mail.SenderCharacterID != "0" || mail.Metadata["source"] != "tag_tournament" {
					t.Fatal("阶段邮件的文案或原发件人、来源不符", stage, mail)
				}
				if len(mail.Attachments) != 2 || mail.Gold != 0 {
					t.Fatal("修改文案改变了两种材料附件", stage, mail)
				}
			}
		})
	}
}
