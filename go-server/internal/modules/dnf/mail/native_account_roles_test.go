package mail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// The real native op789 producer emits exactly this one-byte server selector.
// Export only synthetic-account responses for the original x86 op718 parser.
func TestNativeMailboxAccountRolesRemainAccountScopedAndReadOnly(t *testing.T) {
	for _, serverID := range []byte{0, 1, 118, 249} {
		t.Run(fmt.Sprint(serverID), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			original := []dnfrepo.CharacterRecord{
				{CharacterID: "77", AccountID: "synthetic-account", Name: "发送者", Slot: 0},
				{CharacterID: "88", AccountID: "synthetic-account", Name: "来来来", Slot: 1},
				{CharacterID: "99", AccountID: "synthetic-other", Name: "其他账号", Slot: 0},
			}
			for _, character := range original {
				mustSaveCharacter(t, repos, character)
			}
			got, err := NewHandler().Handle(ctx, alignedcmd.Request{
				Opcode: uint16(dnfenum.CmdPacketRequestServerCharacterList), Body: []byte{serverID},
				AccountID: "synthetic-account", SelectedCharacterID: 77, Repositories: repos,
			})
			if err != nil {
				t.Fatal(err)
			}
			name, err := encodeMailboxText("来来来", mailboxRecipientListNameMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			want := appendU32([]byte{serverID, 1}, 88)
			want = appendU32(want, uint32(len(name)))
			want = append(want, name...)
			want = append(want, 0)
			if len(got.UpperResponses) != 1 {
				t.Fatalf("expected one notification: %+v", got)
			}
			assertMailboxResponse(t, got.UpperResponses[0], 718, 0, want)
			if len(got.PostActions) != 0 || got.MailboxAlarmRecipientID != 0 {
				t.Fatal("list request has mail delivery side effects")
			}
			for _, character := range original {
				after, exists, err := repos.Character.Load(ctx, character.CharacterID)
				if err != nil || !exists || !reflect.DeepEqual(after, character) {
					t.Fatalf("list modified role %s: %v", character.CharacterID, err)
				}
			}
			if directory := os.Getenv("DNF_MAIL_ACCOUNT_ROLES_NATIVE_DIR"); directory != "" {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("server-%d-718.bin", serverID)), got.UpperResponses[0].Body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
