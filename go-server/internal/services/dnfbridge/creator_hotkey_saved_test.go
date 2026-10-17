package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCreatorHotkeySavedConfigurationUsesCharacterKeyboardType(t *testing.T) {
	for _, job := range []int{0, 10, 12} {
		for _, accountType := range []string{"", "0", "1", "2"} {
			t.Run(strconv.Itoa(job)+"_account_"+accountType, func(t *testing.T) {
				s, session, conn, repos := testGameOptionFixture(t)
				s.options.currentClientVersion = "official-24.9"
				ctx := context.Background()
				character, found, err := repos.Character.Load(ctx, "19")
				if err != nil || !found {
					t.Fatal(found, err)
				}
				character.Job = strconv.Itoa(job)
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				keys := []byte("I01\t87\nI02\t65\nI03\t83\nI04\t68\nI06\t32\n")
				if err := s.handleSaveGameOption2(session, testLengthPrefixedBlob(keys)); err != nil {
					t.Fatal(err)
				}
				if err := s.saveAccountGameOptionValues(session, "account-1", map[string]string{"hotkey_key_type": accountType}); err != nil {
					t.Fatal(err)
				}
				account, _, _ := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
				saved, _, _ := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
				if err := s.sendAccountGameOptionNotices(session, "relogin"); err != nil {
					t.Fatal(err)
				}
				wantType := parseSettingsUint8(accountType)
				if job == 10 {
					wantType = 1
				}
				packets := drainUpperPackets(t, conn)
				if len(packets) != 1 || packets[0].msgID != 454 || !bytes.Equal(packets[0].body, buildAccountHotkeyNoticeBody(wantType, keys)) {
					t.Fatalf("job=%d type=%q expected=%d packets=%+v", job, accountType, wantType, packets)
				}
				afterAccount, _, _ := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
				afterSaved, _, _ := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
				if !reflect.DeepEqual(account, afterAccount) || !reflect.DeepEqual(saved, afterSaved) {
					t.Fatal("replay changed persisted settings")
				}
			})
		}
	}
}

func TestCreatorHotkeySavedConfigurationRequiresOwnedCharacter(t *testing.T) {
	for _, owner := range []string{"missing", "another-account"} {
		t.Run(owner, func(t *testing.T) {
			s, session, conn, repos := testGameOptionFixture(t)
			s.options.currentClientVersion = "official-24.9"
			ctx := context.Background()
			if owner == "missing" {
				session.selectedCharacterID = 20
			} else {
				c, _, _ := repos.Character.Load(ctx, "19")
				c.AccountID = owner
				c.Job = "10"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			id := strconv.Itoa(int(session.selectedCharacterID))
			if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: characterHotkeyConfigScope(id), Values: map[string]string{"hotkey_config_hex": "4930310938370a"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.sendAccountHotkeyNotice(session, "relogin", "account-1", nil); err != nil {
				t.Fatal(err)
			}
			if got := drainUpperPackets(t, conn); len(got) != 0 {
				t.Fatalf("unowned character sent key configuration: %+v", got)
			}
		})
	}
}
