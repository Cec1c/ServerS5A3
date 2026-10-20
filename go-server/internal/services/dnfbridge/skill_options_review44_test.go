package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"
)

// skill-options-review44（独立独审 Turing 的 skill-lock-protocol43 候选）
//
// 断言的是候选**声称的范围**，全部来自源码行为与固定 EXE 已证事实：
//
//	A) official-24.9：op447 只接受 512 字节；1/511/513 被拒且**不覆盖**此前有效快照
//	   （EXE 端 0x2255DB0 的 reader：cmp [ebp-4],0x200 → 非 512 直接 xor al,al;ret）；
//	B) 同一模式下 op197/op368（账号级游戏选项/快捷聊天）仍然落库 —— 守卫不得波及已验收的声音/账号设置；
//	C) 非 official-24.9 profile：op447 的非 512 长度仍按既有契约落库（其他 profile 不受影响）。
func TestReview44CharacterOptionGuardScopeAndSideEffects(t *testing.T) {
	t.Setenv(currentCharacterOptionNoticeEnv, "on")
	t.Setenv("DNFBRIDGE_CHARACTER_OPTION_PROJECTION", "off")

	t.Run("A_official249_exact512_and_reject_without_overwrite", func(t *testing.T) {
		s, session, _, repos := testGameOptionFixture(t)
		s.options.currentClientVersion = "official-24.9"
		good := testLengthPrefixedBlob(bytes.Repeat([]byte{0xff}, 512))
		if err := s.handleSaveCharacterOption(session, good); err != nil {
			t.Fatal(err)
		}
		before, found, err := repos.Settings.Load(context.Background(), characterOptionScope("19"))
		if err != nil || !found {
			t.Fatalf("valid 512 snapshot missing found=%v err=%v", found, err)
		}
		if before.Values["character_option_len"] != "512" {
			t.Fatalf("stored len=%q want 512", before.Values["character_option_len"])
		}
		for _, n := range []int{1, 511, 513} {
			t.Run(strconv.Itoa(n), func(t *testing.T) {
				bad := testLengthPrefixedBlob(bytes.Repeat([]byte{0x6d}, n))
				if err := s.handleSaveCharacterOption(session, bad); err != nil {
					t.Fatal(err)
				}
				after, found, err := repos.Settings.Load(context.Background(), characterOptionScope("19"))
				if err != nil || !found {
					t.Fatalf("snapshot lost found=%v err=%v", found, err)
				}
				if after.Values["character_option_body_hex"] != before.Values["character_option_body_hex"] {
					t.Fatalf("len=%d overwrote the valid 512 snapshot; the pinned EXE reader rejects it", n)
				}
			})
		}
	})

	t.Run("B_official249_account_options_still_persist", func(t *testing.T) {
		s, session, _, repos := testGameOptionFixture(t)
		s.options.currentClientVersion = "official-24.9"
		accountBlob := testGameOptionBlob(178, 0x11)
		if _, err := s.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(accountBlob)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.handleGameOptionCommand(session, currentSaveQuickchatOpcode, testQuickchatBody(0, []byte("bank-zero"))); err != nil {
			t.Fatal(err)
		}
		record, found, err := repos.Settings.Load(context.Background(), accountGameOptionScope("account-1"))
		if err != nil || !found {
			t.Fatalf("account option gone under the candidate found=%v err=%v", found, err)
		}
		if record.Values["main_game_option_len"] != "178" || record.Values["quickchat_bank0_hex"] == "" {
			t.Fatalf("account/sound option regressed: %v", record.Values)
		}
	})

	t.Run("C_other_profiles_keep_the_old_contract", func(t *testing.T) {
		s, session, _, repos := testGameOptionFixture(t)
		s.options.currentClientVersion = "90cn"
		short := testLengthPrefixedBlob(bytes.Repeat([]byte{0x33}, 100))
		if err := s.handleSaveCharacterOption(session, short); err != nil {
			t.Fatal(err)
		}
		record, found, err := repos.Settings.Load(context.Background(), characterOptionScope("19"))
		if err != nil || !found {
			t.Fatalf("non-official profile lost its save: found=%v err=%v", found, err)
		}
		if record.Values["character_option_len"] != "100" {
			t.Fatalf("non-official profile len=%q want 100 (guard must be scoped to official-24.9)", record.Values["character_option_len"])
		}
	})
}
