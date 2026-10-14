package mail

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAttachmentRowPreservesSealEvidence(t *testing.T) {
	sealedRaw := make([]byte, mailboxItemRawSize)
	sealedRaw[0x0D] = 1
	for _, tt := range []struct {
		name       string
		attachment dnfrepo.MailAttachment
		want       byte
	}{
		{"模板附件的封装标记", dnfrepo.MailAttachment{Extra: map[string]string{"seal_flag": "1"}}, 1},
		{"现有封装行", dnfrepo.MailAttachment{RawEntry: sealedRaw}, 1},
		{"现有解封行", dnfrepo.MailAttachment{RawEntry: make([]byte, mailboxItemRawSize)}, 0},
		{"明确解封附件", dnfrepo.MailAttachment{Extra: map[string]string{"seal_flag": "0"}}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.attachment.ItemID, tt.attachment.Count = 700, 1
			if row := mailboxAttachmentRaw(tt.attachment); len(row) != mailboxItemRawSize || row[0x0D] != tt.want {
				t.Fatalf("邮件附件行封装丢失：seal=%d want=%d", row[0x0D], tt.want)
			}
		})
	}
}
