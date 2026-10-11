package mail

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMailboxAvatarExtensionKeepsExpiryAndFollowingSummaryAligned(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for _, sockets := range [][]byte{nil, bytes.Repeat([]byte{1}, 30)} {
		avatar := dnfrepo.MailAttachment{ItemID: 40734, Count: 1, Extra: map[string]string{"mailbox_equipment_type": "[coat avatar]"}}
		if sockets != nil {
			avatar.Extra["avatar_socket_data"] = hex.EncodeToString(sockets)
			avatar.Extra["avatar_color_data"] = "01020304"
		}
		w := mailboxBodyWriter{}
		m := dnfrepo.MailRecord{MailID: "11", SenderName: "GM", CreatedAt: now, ExpireAt: now.Add(3600 * time.Second)}
		if err := w.writeSummary(mailboxSummary{mail: m, mailID: 11, attachment: &avatar}, now); err != nil {
			t.Fatal(err)
		}
		plain := dnfrepo.MailAttachment{ItemID: 10159142, Count: 40}
		if err := w.writeSummary(mailboxSummary{mail: m, mailID: 12, attachment: &plain}, now); err != nil {
			t.Fatal(err)
		}
		c := invCursor{data: w.bytes()}
		c.u32(t, "avatar mail id")
		c.u32(t, "sender")
		c.dstring(t, "name")
		c.need(t, 24+119, "fixed item fields")
		n := c.u32(t, "native avatar extension length")
		if n != uint32(len(sockets)) || !bytes.Equal(c.need(t, int(n), "avatar extension"), sockets) {
			t.Fatalf("avatar extension length=%d want=%d", n, len(sockets))
		}
		colorLength := c.u32(t, "native avatar colour extension length")
		if sockets == nil {
			if colorLength != 0 {
				t.Fatalf("empty colour length=%d", colorLength)
			}
		} else if colorLength != 4 || !bytes.Equal(c.need(t, int(colorLength), "avatar colour data"), []byte{1, 2, 3, 4}) {
			t.Fatalf("colour extension=%d", colorLength)
		}
		if got := c.u32(t, "remaining seconds"); got != 3600 {
			t.Fatalf("remaining seconds became blob data: %d", got)
		}
		if got := c.u32(t, "claim id"); got != 11 {
			t.Fatalf("claim id=%d", got)
		}
		c.u8(t, "mail type")
		if got := c.u32(t, "following summary id"); got != 12 {
			t.Fatalf("following mail id=%d", got)
		}
		c.u32(t, "sender")
		c.dstring(t, "name")
		c.need(t, 24+119, "plain fixed item fields")
		if got := c.u32(t, "plain remaining seconds"); got != 3600 {
			t.Fatalf("plain attachment has unexpected extension: %d", got)
		}
		c.need(t, 5, "plain summary tail")
		if c.off != len(c.data) {
			t.Fatalf("trailing bytes=%d", len(c.data)-c.off)
		}
	}
}

func TestMailboxAvatarExtensionUsesOnlyPVFEquipmentType(t *testing.T) {
	for _, token := range []string{"[hat avatar]", "[hair avatar]", "[face avatar]", "[coat avatar]", "[pants avatar]", "[shoes avatar]", "[breast avatar]", "[waist avatar]", "[skin avatar]", "[aurora avatar]", "`[Weapon Avatar]`"} {
		if !mailboxAttachmentIsAvatar(dnfrepo.MailAttachment{Extra: map[string]string{"mailbox_equipment_type": token}}) {
			t.Fatalf("avatar type rejected: %s", token)
		}
	}
	for _, token := range []string{"[coat]", "[creature]", "[artifact red]", "", "[avatar]"} {
		if mailboxAttachmentIsAvatar(dnfrepo.MailAttachment{Extra: map[string]string{"mailbox_equipment_type": token, "avatar_socket_data": "00"}}) {
			t.Fatalf("ordinary attachment took avatar branch: %s", token)
		}
	}
}
