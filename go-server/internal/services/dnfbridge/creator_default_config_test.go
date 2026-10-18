package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFFreshCreatorReceivesDefaultTextWithoutOverwritingSettings(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	for _, accountExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-account", true: "existing-account"}[accountExists], func(t *testing.T) {
			s, session, conn, repos := testGameOptionFixture(t)
			s.options.currentClientVersion = "official-24.9"
			s.options.pvfPath = path
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "19")
			c.Job = "10"
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if accountExists {
				if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: accountGameOptionScope("account-1"), Values: map[string]string{"source": "fixture-account"}}); err != nil {
					t.Fatal(err)
				}
			}
			accountBefore, foundBefore, _ := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
			if err := s.sendAccountGameOptionNotices(session, "creator_first_entry"); err != nil {
				t.Fatal(err)
			}
			rows := drainUpperPackets(t, conn)
			if len(rows) != 1 || rows[0].msgID != 454 {
				t.Fatalf("fresh Creator has no default notice: %+v", rows)
			}
			body := rows[0].body
			if len(body) < 5 || body[0] != 1 || int(binary.LittleEndian.Uint32(body[1:5])) != len(body)-5 {
				t.Fatal("invalid text notice shape")
			}
			for _, pair := range []string{"I01\t77\n", "I02\t55\n", "I03\t73\n", "I04\t58\n", "I06\t13\n", "I21\t71\n", "I22\t59\n", "I23\t72\n"} {
				if !bytes.Contains(body[5:], []byte(pair)) {
					t.Fatalf("missing PVF default %q", pair)
				}
			}
			if dir := os.Getenv("DNFBRIDGE_CREATOR_NATIVE_OUTPUT"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "creator-default-454-body.bin"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			accountAfter, foundAfter, _ := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
			saved, found, _ := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
			if found || saved.Scope != "" || foundBefore != foundAfter || !reflect.DeepEqual(accountBefore, accountAfter) {
				t.Fatal("default replay rewrote player/account settings")
			}
			custom := []byte("I01\t84\nI06\t65\n")
			if err := s.handleSaveGameOption2(session, testLengthPrefixedBlob(custom)); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := s.sendAccountGameOptionNotices(session, "creator_relogin"); err != nil {
				t.Fatal(err)
			}
			rows = drainUpperPackets(t, conn)
			if len(rows) != 1 || !bytes.Equal(rows[0].body, buildAccountHotkeyNoticeBody(1, custom)) {
				t.Fatal("custom keys replaced by default")
			}
		})
	}
}

func TestCreatorDefaultTextRejectsIncompleteOrMalformedPVF(t *testing.T) {
	for _, text := range []string{"", "[key]\n`up` `I01` `character` `left` 87 `NormalType`\n", "[key]\n`up` `I01` `character` `left` 87\n", "[key]\n`up` `I01` `character` `left` 99999 `CreatorType`\n"} {
		if _, err := parseCreatorDefaultConfig(text); err == nil {
			t.Fatalf("bad defaults accepted %q", text)
		}
	}
}
