package dnfbridge

import (
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Preserve the same instance-evidence priority used by the existing item row
// and GM-override readers, without assigning the resolver's claim-time default.
func currentMailboxOriginalAttachmentExpiry(attachment dnfrepo.MailAttachment) time.Time {
	unix := currentItemListStackExpire(dnfrepo.ItemStack{ExpireAt: attachment.ExpireAt, Extra: attachment.Extra})
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(int64(unix), 0).UTC()
}
