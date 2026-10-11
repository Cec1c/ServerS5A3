package dnfbridge

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// buildClientLoginBody 按实测的当前 EXE class1/op1 登录包布局构造 body：
// body+0 标记, body+1 容量 0x30, body+2 u32 长度, body+6 起 32 个 ASCII 十六进制字符。
func buildClientLoginBody(token string) []byte {
	body := make([]byte, 64)
	body[1] = 0x30
	binary.LittleEndian.PutUint32(body[2:6], uint32(len(token)))
	copy(body[loginTokenBodyOffset:], token)
	return body
}

func TestNewLoginTokenKeepsClientCompatibleShape(t *testing.T) {
	seen := make(map[string]struct{})
	for range 32 {
		token, err := newLoginToken()
		if err != nil {
			t.Fatalf("newLoginToken: %v", err)
		}
		if len(token) != loginTokenChars {
			t.Fatalf("token %q length=%d, want %d", token, len(token), loginTokenChars)
		}
		if !strings.HasPrefix(token, loginTokenPrefix) {
			t.Fatalf("token %q lost the client-compatible prefix %q", token, loginTokenPrefix)
		}
		if got := normalizeLoginToken(token); got != token {
			t.Fatalf("normalizeLoginToken(%q)=%q, want unchanged", token, got)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("token %q repeated", token)
		}
		seen[token] = struct{}{}
	}
}

func TestNormalizeLoginToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "de509f65e9ccaae621cb7278fc2b8e6c", "de509f65e9ccaae621cb7278fc2b8e6c"},
		{"uppercase normalized", "DE509F65E9CCAAE621CB7278FC2B8E6C", "de509f65e9ccaae621cb7278fc2b8e6c"},
		{"padded", "  de509f65e9ccaae621cb7278fc2b8e6c ", "de509f65e9ccaae621cb7278fc2b8e6c"},
		{"too short", "de509f65e9ccaae621cb7278fc2b8e6", ""},
		{"too long", "de509f65e9ccaae621cb7278fc2b8e6cc", ""},
		{"non hex", "de509f65e9ccaae621cb7278fc2b8e6g", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLoginToken(tc.in); got != tc.want {
				t.Fatalf("normalizeLoginToken(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClientLoginTokenFromBodyStructuredLayout(t *testing.T) {
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	got := clientLoginTokenFromBody(buildClientLoginBody(token))
	if got != token {
		t.Fatalf("token=%q, want %q", got, token)
	}
}

// realCapturedClientLoginBody 复现 2026-09-24 HK 抓包里当前 EXE class1/op1 登录包
// body 的真实前缀（conn_id=game-000018 pkt_seq=3, wire_body_len=586）。
// 令牌（+6..+37）是启动器写进 TCLS 启动串的 outer token。
func realCapturedClientLoginBody(token string) []byte {
	body := make([]byte, 586)
	copy(body[0:6], []byte{0x00, 0x30, 0x20, 0x00, 0x00, 0x00})
	copy(body[6:38], token)
	copy(body[38:51], []byte{
		0x00, 0x08, 0x00, 0x00, 0x00, 0xf2, 0x03, 0x22,
		0xcf, 0x10, 0x4e, 0x91, 0xd0,
	})
	copy(body[51:], []byte{
		0x00, 0x02, 0x00, 0x00, 0x01, 0x01, 0x00, 0x1c,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x1a, 0x01,
	})
	return body
}

func TestClientLoginTokenFromBodyMatchesRealClientBytes(t *testing.T) {
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	body := realCapturedClientLoginBody(token)
	if len(body) != 586 {
		t.Fatalf("body length=%d, want the captured 586", len(body))
	}
	if got := clientLoginTokenFromBody(body); got != token {
		t.Fatalf("token=%q, want %q", got, token)
	}
	// 旧启动器的固定兼容串也必须能被读出来（只是查不到账号，会话随后会落到
	// 共享兜底账号并写 WARN，见 repository_access.go）。
	legacy := "de509f65e9ccaae621cb7278fc2b8e6c"
	if got := clientLoginTokenFromBody(realCapturedClientLoginBody(legacy)); got != legacy {
		t.Fatalf("legacy token=%q, want %q", got, legacy)
	}
}

func TestClientLoginTokenFromBodyScanFallback(t *testing.T) {
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	// 长度字段与真实长度不符时，退化到开头区域的有界扫描。
	body := make([]byte, 128)
	binary.LittleEndian.PutUint32(body[2:6], 7)
	copy(body[24:], token)
	if got := clientLoginTokenFromBody(body); got != token {
		t.Fatalf("token=%q, want %q", got, token)
	}
}

func TestClientLoginTokenFromBodyRejectsIncompleteBodies(t *testing.T) {
	if got := clientLoginTokenFromBody(nil); got != "" {
		t.Fatalf("nil body token=%q, want empty", got)
	}
	if got := clientLoginTokenFromBody(make([]byte, loginTokenBodyOffset+loginTokenChars-1)); got != "" {
		t.Fatalf("short body token=%q, want empty", got)
	}
	body := make([]byte, 64)
	copy(body[loginTokenBodyOffset:], []byte(strings.Repeat("z", loginTokenChars)))
	if got := clientLoginTokenFromBody(body); got != "" {
		t.Fatalf("garbage body token=%q, want empty", got)
	}
}

func TestLoginTokenStoreCachesExpiresAndRemembersMisses(t *testing.T) {
	var store accountLoginTokenStore
	now := time.Now()
	token := loginTokenPrefix + "0123456789abcdef01234567"
	store.remember(token, "dnf:10001", now.Add(time.Minute))
	if accountID, ok := store.recall(token, now); !ok || accountID != "dnf:10001" {
		t.Fatalf("recall=%q ok=%t, want dnf:10001", accountID, ok)
	}
	if _, ok := store.recall(token, now.Add(2*time.Minute)); ok {
		t.Fatal("expired token still resolved from cache")
	}
	if store.recentlyMissed(token, now) {
		t.Fatal("token cached as a miss while it was remembered")
	}
	unknown := loginTokenPrefix + "ffffffffffffffffffffffff"
	store.rememberMiss(unknown, now)
	if !store.recentlyMissed(unknown, now.Add(time.Second)) {
		t.Fatal("miss not cached")
	}
	if store.recentlyMissed(unknown, now.Add(2*loginTokenMissCacheTTL)) {
		t.Fatal("miss cache outlived its TTL")
	}
}

// TestBindGameSessionAccountFromLoginTokenOverridesStalePIDBinding 钉死"令牌是唯一
// 权威来源"：会话先按客户端进程号登记拿到了一个陈旧账号，登录令牌到达后必须
// 无条件覆盖它，并且把该 PID 的登记一起纠正过来（自愈），后续同 PID 会话不再
// 可能拿到那个陈旧账号。
func TestBindGameSessionAccountFromLoginTokenOverridesStalePIDBinding(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	service.options.accountID = "dnf:1"
	// PID 101 上留着一条陈旧的启动器登记（PID 复用/登记丢失的现场形态）。
	if err := service.RegisterClientAccount(101, "dnf:1"); err != nil {
		t.Fatal(err)
	}
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	service.loginTokens.remember(token, "dnf:10001", time.Now().Add(time.Hour))
	session := &gameSession{
		connID:         "game-000001",
		accountID:      "dnf:1",
		clientPID:      101,
		clientIP:       "203.0.113.7",
		accountFromPID: true,
	}
	if !service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification, buildClientLoginBody(token)) {
		t.Fatal("token not recognized")
	}
	if session.accountID != "dnf:10001" {
		t.Fatalf("session account=%q, want dnf:10001", session.accountID)
	}
	if !session.accountFromToken || !session.loginTokenSeen {
		t.Fatalf("accountFromToken=%t loginTokenSeen=%t", session.accountFromToken, session.loginTokenSeen)
	}
	if got := service.accountIDForSession(session); got != "dnf:10001" {
		t.Fatalf("accountIDForSession=%q, want dnf:10001", got)
	}
	if session.sharedAccountFallbackReason != "" {
		t.Fatalf("shared fallback reason=%q, want empty after a token bound the account", session.sharedAccountFallbackReason)
	}
	// 自愈：同 PID 的下一条会话（即使没有令牌）也拿到被令牌证明过的账号。
	if got, ok := service.registeredClientAccount(101); !ok || got != "dnf:10001" {
		t.Fatalf("pid binding=%q ok=%t, want dnf:10001 after token correction", got, ok)
	}
	laterAccount, _, found := service.registeredClientAccountForPID(101)
	if !found {
		t.Fatal("pid binding disappeared after token correction")
	}
	later := &gameSession{
		connID:         "game-000002",
		clientPID:      101,
		accountID:      laterAccount,
		accountFromPID: true,
	}
	if got := service.accountIDForSession(later); got != "dnf:10001" {
		t.Fatalf("later session account=%q, want the token-proven dnf:10001", got)
	}
}

func TestBindGameSessionAccountFromLoginTokenIgnoresLegacyAndUnknownTokens(t *testing.T) {
	service := &Service{clientAccounts: newClientAccountRegistry()}
	service.options.accountID = "dnf:1"
	session := &gameSession{connID: "game-000002", accountID: "dnf:1", clientIP: "203.0.113.8"}

	// 旧启动器固定兼容串：形状合法但不是签发的令牌，必须保持原绑定并记下原因。
	legacy := "de509f65e9ccaae621cb7278fc2b8e6c"
	if service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification, buildClientLoginBody(legacy)) {
		t.Fatal("legacy compatibility token should not bind an account")
	}
	if session.accountID != "dnf:1" || session.accountFromToken {
		t.Fatalf("session account=%q accountFromToken=%t", session.accountID, session.accountFromToken)
	}
	if !session.loginTokenSeen {
		t.Fatal("a 32-hex token in the login packet must be recorded even when it resolves to nothing")
	}
	if session.sharedAccountFallbackReason != sharedAccountReasonTokenNotRecognised {
		t.Fatalf("fallback reason=%q, want %q", session.sharedAccountFallbackReason, sharedAccountReasonTokenNotRecognised)
	}
	if service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification, nil) {
		t.Fatal("empty body should not bind an account")
	}
	if service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification+1, buildClientLoginBody(legacy)) {
		t.Fatal("non-default classification should not bind an account")
	}
}
