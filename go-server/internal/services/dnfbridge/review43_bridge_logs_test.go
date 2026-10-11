package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dproto"
	p "longheng.io/server/internal/modules/dnf/protocol"
)

// All markers are public fixtures. No live token is read or issued.
func review43Marker() string { return loginTokenPrefix + strings.Repeat("7", 24) }

func review43Logs(t *testing.T) (*Service, *gameSession, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.log")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatal("Review43: temporary logger unavailable")
	}
	var ordinary bytes.Buffer
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = logger.Close()
		}
	})
	s := &Service{packetLog: logger, logger: slog.New(slog.NewTextHandler(&ordinary, nil)), clientAccounts: newClientAccountRegistry()}
	s.options.currentClientVersion = "official-24.9"
	session := &gameSession{conn: &bufferConn{}, connID: "review43-synthetic", clientIP: "127.0.0.1"}
	return s, session, func() string {
		if !closed {
			if logger.Close() != nil {
				t.Fatal("Review43: temporary logger close failed")
			}
			closed = true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("Review43: temporary log read failed")
		}
		return string(data) + ordinary.String()
	}
}

func review43NoCredential(t *testing.T, text string) {
	t.Helper()
	marker := review43Marker()
	for _, v := range []string{marker, hex.EncodeToString([]byte(marker)), marker[:8], hex.EncodeToString([]byte(marker[:8]))} {
		if strings.Contains(text, v) {
			t.Fatal("Review43: production log exposed synthetic credential bytes or fragment; values omitted")
		}
	}
	if strings.Contains(text, "token_prefix=") {
		t.Fatal("Review43: production log retained a credential fragment field; values omitted")
	}
}

func review43Upper(t *testing.T, body []byte) []byte {
	t.Helper()
	raw, err := p.BuildChannelPacket(1, body, 1, p.DefaultChannelClassification)
	if err != nil {
		t.Fatal("Review43: synthetic protocol construction failed")
	}
	return raw
}

func TestReview43UpperLoginPreBindLogs(t *testing.T) {
	for _, name := range []string{"plain_unrecognised", "plain_bound", "probe_decode", "probe_decode_failure", "native_decode", "native_decode_failure"} {
		t.Run(name, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			body := buildClientLoginBody(review43Marker())
			switch name {
			case "plain_bound":
				s.loginTokens.remember(review43Marker(), "review43-owner", time.Now().Add(time.Minute))
			case "probe_decode":
				s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecProbe
			case "probe_decode_failure":
				s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecProbe
				body = body[:len(body)-1]
			case "native_decode":
				s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecNative
				var err error
				body, err = encodeUpperNativeBlock(body, 8, upperCurrentCodecKey(0, 16), newUpperNativeCAST5Block)
				if err != nil {
					t.Fatal("Review43: synthetic codec construction failed")
				}
			case "native_decode_failure":
				s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecNative
				body = body[:len(body)-1]
			}
			if s.handleGameUpper(session, review43Upper(t, body)) != nil {
				t.Fatal("Review43: synthetic upper dispatch failed")
			}
			if name == "plain_bound" && !session.accountFromToken {
				t.Fatal("Review43: logging protection altered valid binding")
			}
			review43NoCredential(t, logs())
		})
	}
}

func TestReview43BinderPacketAndOrdinaryLogs(t *testing.T) {
	for _, name := range []string{"unrecognised", "bound", "stale_pid"} {
		t.Run(name, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			if name != "unrecognised" {
				s.loginTokens.remember(review43Marker(), "review43-owner", time.Now().Add(time.Minute))
			}
			if name == "stale_pid" {
				session.clientPID = 43
				session.accountID = "review43-old-owner"
				_ = s.RegisterClientAccount(43, session.accountID)
			}
			bound := s.bindGameSessionAccountFromLoginToken(session, p.DefaultChannelClassification, buildClientLoginBody(review43Marker()))
			if bound != (name != "unrecognised") {
				t.Fatal("Review43: binder result changed")
			}
			text := logs()
			review43NoCredential(t, text)
			if bound && !strings.Contains(text, "game-login-token-account-bound") {
				t.Fatal("Review43: safe bind diagnostic lost")
			}
		})
	}
}

func TestReview43AlternativeReceiveLogs(t *testing.T) {
	for _, name := range []string{"raw_chunk", "legacy_op1", "tcp_op1", "tcp_nonlogin_then_login", "tcp_login_then_nonlogin"} {
		t.Run(name, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			body := buildClientLoginBody(review43Marker())
			switch name {
			case "raw_chunk":
				s.logGamePacket(session, "RECV", "game-raw", review43Upper(t, body))
			case "legacy_op1":
				if s.handleLegacyGamePacket(session, review43Upper(t, body)) != nil {
					t.Fatal("Review43: legacy parser path failed")
				}
			case "tcp_op1":
				raw, err := p.BuildLatestGameTCP(1, 1, body, p.TransportOptions{})
				if err != nil || s.handleGameFrame(session, raw) != nil {
					t.Fatal("Review43: TCP parser path failed")
				}
			case "tcp_nonlogin_then_login", "tcp_login_then_nonlogin":
				login, err := p.BuildLatestGameTCP(1, 1, body, p.TransportOptions{})
				if err != nil {
					t.Fatal("Review43: TCP construction failed")
				}
				ordinary, err := p.BuildLatestGameTCP(1, 60000, []byte("public-diagnostic-row"), p.TransportOptions{})
				if err != nil {
					t.Fatal("Review43: ordinary TCP construction failed")
				}
				first, second := ordinary, login
				if name == "tcp_login_then_nonlogin" {
					first, second = login, ordinary
				}
				raw := append(bytes.Clone(first), second[p.TCPOuterHeaderSize:]...)
				binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
				records, err := p.ParseLatestGameTCPRecords(raw)
				if err != nil || len(records) != 2 || s.handleGameFrame(session, raw) != nil {
					t.Fatal("Review43: production multi-record TCP parser path failed")
				}
			}
			review43NoCredential(t, logs())
		})
	}
}

type review43Dproto struct{ inner []byte }

func (d *review43Dproto) DecodeClient(context.Context, []byte) (dproto.DecodeResult, error) {
	return dproto.DecodeResult{InnerPackets: [][]byte{d.inner}}, nil
}
func (d *review43Dproto) EncodeServer(_ context.Context, b []byte) (dproto.EncodeResult, error) {
	return dproto.EncodeResult{Packet: b}, nil
}
func (*review43Dproto) HandleClientControl(context.Context, uint16, []byte) ([][]byte, error) {
	return nil, nil
}
func (*review43Dproto) Close() error { return nil }

func TestReview43DprotoLogs(t *testing.T) {
	for _, name := range []string{"opaque_no_provider", "decoded_inner_login", "malformed_envelope"} {
		t.Run(name, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			raw, err := p.BuildDprotoClientEnvelope([]byte(review43Marker()), 1)
			if err != nil {
				t.Fatal("Review43: synthetic envelope failed")
			}
			if name == "decoded_inner_login" {
				session.dproto = &review43Dproto{inner: review43Upper(t, buildClientLoginBody(review43Marker()))}
			}
			if name == "malformed_envelope" {
				raw[3]++
			}
			if s.handleGameDprotoPacket(session, raw) != nil {
				t.Fatal("Review43: synthetic Dproto dispatch failed")
			}
			review43NoCredential(t, logs())
		})
	}
}

func TestReview43DecodeAndParserErrorsContainNoBody(t *testing.T) {
	s, _, logs := review43Logs(t)
	body := buildClientLoginBody(review43Marker())
	_, _, supported, err := decodeCurrentUpperClientBody(1, body[:len(body)-1])
	if !supported || err == nil {
		t.Fatal("Review43: intended malformed codec input did not fail")
	}
	s.logPacketEvent("game-upper-client-body-decode-deferred", "msg_id", uint16(1), "reason", err.Error(), "wire_body_len", len(body)-1)
	raw := review43Upper(t, body)
	raw[3]++
	_, err = p.ParseChannelPacketUnchecked(raw)
	if err == nil {
		t.Fatal("Review43: intended malformed parser input did not fail")
	}
	s.logPacketEvent("game-split-error", "error", err, "buffer_bytes", len(raw))
	s.logWarn("synthetic parser failure", "error", err)
	review43NoCredential(t, logs())
}

func TestReview43OrdinaryPacketDiagnosticsRemain(t *testing.T) {
	s, session, logs := review43Logs(t)
	body := []byte("public-diagnostic-row")
	raw, err := p.BuildChannelPacket(60000, body, 1, p.DefaultChannelClassification)
	if err != nil || s.handleGameUpper(session, raw) != nil {
		t.Fatal("Review43: ordinary diagnostic route failed")
	}
	s.logPacketEvent("review43-ordinary-event", "body_len", len(body))
	ordinaryTCP, err := p.BuildLatestGameTCP(1, 60000, []byte("public-tcp-row"), p.TransportOptions{})
	if err != nil || s.handleGameFrame(session, ordinaryTCP) != nil {
		t.Fatal("Review43: ordinary TCP diagnostic route failed")
	}
	notice, err := p.BuildChannelPacket(1, []byte("public-endpoint-notice"), 1, 0)
	if err != nil {
		t.Fatal("Review43: endpoint notice construction failed")
	}
	// BuildChannelPacket normalizes zero to class1. Set the actual class0 header
	// explicitly and validate it before asserting non-login notice diagnostics.
	notice[0] = 0
	parsedNotice, err := p.ParseChannelPacket(notice)
	if err != nil || parsedNotice.Header.Classification != 0 {
		t.Fatal("Review43: class-zero notice fixture invalid")
	}
	s.logGamePacket(session, "SEND", "game-upper", notice, "msg_id", uint16(1), "classification", byte(0))
	text := logs()
	if !strings.Contains(text, "game-upper") || !strings.Contains(text, hex.EncodeToString(body)) || !strings.Contains(text, "msg_id=60000") || !strings.Contains(text, "review43-ordinary-event") {
		t.Fatal("Review43: non-login diagnostic output was lost")
	}
	if !strings.Contains(text, hex.EncodeToString([]byte("public-tcp-row"))) || !strings.Contains(text, hex.EncodeToString([]byte("public-endpoint-notice"))) {
		t.Fatal("Review43: ordinary TCP or class-zero endpoint diagnostics lost")
	}
	review43NoCredential(t, text)
}
