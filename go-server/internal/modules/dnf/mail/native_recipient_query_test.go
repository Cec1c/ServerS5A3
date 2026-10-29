package mail

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// These synthetic-name vectors come from the bounded native op324 producer.
// Region zero must remain a valid query value, not an absent-request sentinel.
func TestNativeMailboxRecipientQueryDefaultsToCurrentRegionWithoutAssetMutation(t *testing.T) {
	for _, tail := range []string{"00", "01", "f9"} {
		for _, exists := range []bool{false, true} {
			t.Run(tail+map[bool]string{false: "/missing", true: "/found"}[exists], func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				original := dnfrepo.CharacterRecord{CharacterID: "88", Name: "来来来", Job: "3", Level: 90, Stats: map[string]int64{"grow_type": 2, "gold": 12345}}
				if exists {
					mustSaveCharacter(t, repos, original)
				}
				body, err := hex.DecodeString("06000000c0b4c0b4c0b4" + tail)
				if err != nil {
					t.Fatal(err)
				}
				got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 324, Repositories: repos, Body: body})
				if err != nil {
					t.Fatal(err)
				}
				want := []byte{0, mailboxErrorCode(ErrMailboxRecipientMissing)}
				if exists {
					want = append([]byte{1}, body[:len(body)-1]...) // Echo the native GB18030 name DSTR.
					want = append(want, 2, 90, 0, 3, 0, 1, 0, 0)
				}
				assertOneMailboxResponse(t, got, dnfenum.CmdPacketQueryCharacInfoMailbox, want)
				if len(got.PostActions) != 0 || got.MailboxAlarmRecipientID != 0 {
					t.Fatal("recipient query must not send or deliver mail")
				}
				after, found, err := repos.Character.Load(ctx, "88")
				if err != nil || found != exists || (exists && !reflect.DeepEqual(after, original)) {
					t.Fatal("read-only recipient query changed character", err)
				}
				_, found, err = repos.Mailbox.Load(ctx, "88")
				if err != nil || found {
					t.Fatal("recipient query created a mailbox", err)
				}
			})
		}
	}
}
