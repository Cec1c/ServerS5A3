package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dproto"
	p "longheng.io/server/internal/modules/dnf/protocol"
)

func supplement44MixedFrame(t *testing.T, loginFirst bool) []byte {
	t.Helper()
	login, err := p.BuildLatestGameTCP(1, 1, buildClientLoginBody(review43Marker()), p.TransportOptions{})
	if err != nil {
		t.Fatal("fixture login transport construction failed")
	}
	ordinary, err := p.BuildLatestGameTCP(1, 60000, []byte("supplement-public-neighbour"), p.TransportOptions{})
	if err != nil {
		t.Fatal("fixture ordinary transport construction failed")
	}
	first, second := ordinary, login
	if loginFirst {
		first, second = login, ordinary
	}
	raw := append(bytes.Clone(first), second[p.TCPOuterHeaderSize:]...)
	binary.LittleEndian.PutUint16(raw[2:4], uint16(len(raw)))
	return raw
}

func TestSupplement44MixedFramePreservesOrdinaryRecordAndDispatchBytes(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			s, session, logs := review43Logs(t)
			raw := supplement44MixedFrame(t, first)
			before := bytes.Clone(raw)
			if s.handleGameFrame(session, raw) != nil {
				t.Fatal("mixed frame dispatch failed")
			}
			text := logs()
			review43NoCredential(t, text)
			if !strings.Contains(text, "type=60000") || !strings.Contains(text, hex.EncodeToString([]byte("supplement-public-neighbour"))) {
				t.Fatal("ordinary neighbour diagnostic was lost")
			}
			if !bytes.Equal(raw, before) {
				t.Fatal("logging changed business frame")
			}
			records, err := p.ParseLatestGameTCPRecords(raw)
			if err != nil || len(records) != 2 {
				t.Fatal("logging altered transport parsing")
			}
		})
	}
}

func TestSupplement44DirectTransportCannotBorrowOrdinaryMetadata(t *testing.T) {
	s, session, logs := review43Logs(t)
	raw := supplement44MixedFrame(t, false)
	s.logGamePacket(session, "RECV", "game", raw, "cmd", byte(1), "type", uint16(60000))
	text := logs()
	review43NoCredential(t, text)
	if !strings.Contains(text, fmt.Sprintf("payload_sha256=%x", sha256.Sum256(raw))) {
		t.Fatal("whole-frame safe hash diagnostic missing")
	}
}

type supplement44ErrorDproto struct {
	*review43Dproto
	err error
}

func (s *supplement44ErrorDproto) DecodeClient(context.Context, []byte) (dproto.DecodeResult, error) {
	return dproto.DecodeResult{}, s.err
}

func TestSupplement44ProviderErrorAndUnclassifiedEventAreMetadataOnly(t *testing.T) {
	s, session, logs := review43Logs(t)
	providerError := fmt.Errorf("decoder rejected payload %x", []byte(review43Marker()))
	session.dproto = &supplement44ErrorDproto{review43Dproto: &review43Dproto{}, err: providerError}
	raw, err := p.BuildDprotoClientEnvelope([]byte("public-opaque-input"), 1)
	if err != nil {
		t.Fatal("fixture opaque construction failed")
	}
	got := s.handleGameDprotoPacket(session, raw)
	if got != providerError {
		t.Fatal("logging candidate changed provider error semantics")
	}
	// Same ordinary logger/error boundary used by the real game read loop.
	s.logWarn("dnfbridge handle game packet failed", "error", got)
	s.logInfo("dnfbridge provider failure observed", "reason", got)
	s.logPacketEvent("game-dproto-provider-failed", "error", got)
	s.logPacketEvent("game-split-error", "error", got.Error(), "buffer_bytes", len(raw))
	s.logPacketEvent("game-upper-client-body-decode-deferred", "msg_id", uint16(1), "reason", got.Error(), "wire_body_len", len(raw))
	text := logs()
	review43NoCredential(t, text)
	if !strings.Contains(text, fmt.Sprintf("error_sha256=%x", sha256.Sum256([]byte(providerError.Error())))) {
		t.Fatal("safe provider error hash missing")
	}
}

func TestSupplement44TransportLoginPrivacyDoesNotDependOnPayloadEncoding(t *testing.T) {
	for _, name := range []string{"plain", "aes_fixture"} {
		t.Run(name, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			body := buildClientLoginBody(review43Marker())
			if name == "aes_fixture" {
				var err error
				body, err = aesECBEncrypt(padZero(body, 16), "synthetic-key-44")
				if err != nil {
					t.Fatal("fixture AES construction failed")
				}
			}
			raw, err := p.BuildLatestGameTCP(1, 1, body, p.TransportOptions{})
			if err != nil {
				t.Fatal("fixture transport construction failed")
			}
			before := bytes.Clone(raw)
			if s.handleGameFrame(session, raw) != nil {
				t.Fatal("classified login transport dispatch failed")
			}
			text := logs()
			review43NoCredential(t, text)
			if strings.Contains(text, hex.EncodeToString(body)) || !strings.Contains(text, "payload_redacted=true") {
				t.Fatal("classified login encoded body entered diagnostics")
			}
			if !bytes.Equal(raw, before) {
				t.Fatal("logging changed encoded transport bytes")
			}
		})
	}
}

func TestSupplement44MalformedLoginHeaderCannotEscapeRedaction(t *testing.T) {
	s, session, logs := review43Logs(t)
	raw := review43Upper(t, buildClientLoginBody(review43Marker()))
	raw[3]++
	before := bytes.Clone(raw)
	if _, err := p.ParseChannelPacketUnchecked(raw); err == nil {
		t.Fatal("malformed fixture accepted by parser")
	}
	s.logGamePacket(session, "RECV", "game-upper", raw)
	review43NoCredential(t, logs())
	if !bytes.Equal(raw, before) {
		t.Fatal("redaction repaired business packet")
	}
}
