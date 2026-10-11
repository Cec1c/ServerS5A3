package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestNativeAuth42LoginLogsRetainOnlyMetadataAndHash(t *testing.T) {
	// Synthetic entropy only; no live credential, token or client is inspected.
	token, err := newLoginToken()
	if err != nil {
		t.Fatal("fixture entropy failed")
	}
	body := buildClientLoginBody(token)
	raw, err := dnfproto.BuildChannelPacket(1, body, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(raw)
	path := filepath.Join(t.TempDir(), "redacted.txt")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"RECV", "SEND"} {
		logger.Log(direction, "game-upper", raw, "msg_id", uint16(1), "classification", byte(1), "body_len", len(body), "body_hex", hex.EncodeToString(body), "body_prefix", token[:8], "argument", token)
	}
	// Missing fields cannot turn a parseable login frame into a plaintext log.
	logger.Log("RECV", "game-upper", raw)
	// Unframed and fragmented raw chunks carry no payload bytes in their logs.
	logger.Log("RECV", "game-raw", []byte(token[8:]))
	logger.Log("RECV", "channel-raw", []byte(token[:8]))
	logger.Event("game-upper-meta", "msg_id", uint16(1), "classification", byte(1), "body_hex", hex.EncodeToString(body), "argument", token)
	// Token binding diagnostics must also omit token substrings for rejection.
	s := &Service{packetLog: logger}
	s.bindGameSessionAccountFromLoginToken(&gameSession{connID: "synthetic42", clientIP: "127.0.0.1"}, 1, body)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{token, token[:8], token[8:], hex.EncodeToString([]byte(token)), hex.EncodeToString(raw), "body_hex=", "body_prefix=", "token_prefix=", "argument="} {
		if strings.Contains(text, forbidden) {
			t.Fatal("login packet/event diagnostic leaked credential payload; value omitted")
		}
	}
	if !strings.Contains(text, "msg_id=1 classification=1") || !strings.Contains(text, "payload_redacted=true") || !strings.Contains(text, fmt.Sprintf("payload_sha256=%x", sha256.Sum256(raw))) {
		t.Fatal("redacted login diagnostic lost opcode/class/hash metadata")
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("logging changed protocol bytes")
	}
}
