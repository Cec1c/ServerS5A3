package mail

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMailboxDailyProjectionExtraIsolation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "conflict"}[conflict], func(t *testing.T) {
			original := map[string]string{"keep": "original"}
			projection := map[string]string{"new": "value"}
			if conflict {
				projection["keep"] = "changed"
			}
			before := map[string]string{"keep": "original"}
			got, err := mailboxAttachmentWithClaimProjection(dnfrepo.MailAttachment{Extra: original}, alignedcmd.MailboxAttachmentProjection{Extra: projection})
			if (err != nil) != conflict || !reflect.DeepEqual(original, before) {
				t.Fatalf("conflict=%t err=%v original=%v", conflict, err, original)
			}
			if !conflict {
				got.Extra["keep"] = "mutated result"
				projection["new"] = "mutated input"
				if !reflect.DeepEqual(original, before) || got.Extra["new"] != "value" {
					t.Fatal("projection map alias")
				}
			}
		})
	}
}

func TestMailboxDailyCreationEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "missing_source", "unknown_old_source", "missing_marker", "player", "auction", "not_system"} {
		t.Run(mode, func(t *testing.T) {
			m := dnfrepo.MailRecord{SenderCharacterID: "0", CreatedAt: time.Now(), Metadata: map[string]string{
				"source": "reward", "system_mail": "true", "attachment_grant_anchor": dnfrepo.SystemMailCreationAnchor,
			}}
			switch mode {
			case "missing_source":
				delete(m.Metadata, "source")
			case "unknown_old_source":
				m.Metadata = map[string]string{"source": "unknown", "system_mail": "true"}
			case "missing_marker":
				delete(m.Metadata, "attachment_grant_anchor")
			case "player":
				m.SenderCharacterID = "42"
			case "auction":
				m.Metadata["source"] = "auction_listing_return"
			case "not_system":
				delete(m.Metadata, "system_mail")
			}
			if got := mailCreationProvesNewAttachment(m); got != (mode == "valid") {
				t.Fatalf("creation evidence mode=%s accepted=%t", mode, got)
			}
		})
	}
}
