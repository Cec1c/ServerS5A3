package dnfbridge

import (
	"bytes"
	"testing"
	"time"

	"longheng.io/server/internal/platform/config"
)

func TestChannelNoticePlaintextCompatibilityIsOptInAndProfileBound(t *testing.T) {
	for _, tc := range []struct {
		name, profile  string
		enabled, plain bool
	}{
		{"existing90cn", "90cn", false, false},
		{"decodeCopy90cn", "90cn", true, true},
		{"officialDefault", "official-24.9", false, false},
		{"officialCannotOptIn", "official-24.9", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := residentNoticeTestService(t)
			service.options.currentClientVersion = tc.profile
			service.options.channelNoticePlain = tc.enabled
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "notice-compat"}
			channel, _ := service.currentCatalog().Channel(10)
			body, err := service.buildCurrentChannelResidentNotice(channel, time.Unix(1721020000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if err := service.sendGameUpperRawClass(session, 1, body, 0); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
			if len(rest) != 0 || packet.Header.MsgID != 1 || packet.Header.Classification != 0 || packet.Header.Seq != 0 {
				t.Fatal("unexpected channel notice framing")
			}
			got := packet.Body
			if !tc.plain {
				got = decodeCurrentEXEChannelNoticeBody(got)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("channel notice did not match the selected client decode contract")
			}
			// Other notifications preserve the existing plaintext transport and sequence.
			conn.write.Reset()
			if err := service.sendGameUpperRawClass(session, 2, []byte{1, 2, 3}, 0); err != nil {
				t.Fatal(err)
			}
			packet, rest = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), service.gameUpperHeaderSize())
			if len(rest) != 0 || packet.Header.Seq != 1 || !bytes.Equal(packet.Body, []byte{1, 2, 3}) {
				t.Fatal("unrelated notification changed")
			}
		})
	}
}

func TestChannelNoticePlaintextOptionDefaultsOff(t *testing.T) {
	t.Setenv("DNFBRIDGE_CURRENT_CHANNEL_INFO_PLAINTEXT", "false")
	if optionsFromConfig(config.ServiceConfig{}).channelNoticePlain {
		t.Fatal("default encoding changed")
	}
	t.Setenv("DNFBRIDGE_CURRENT_CHANNEL_INFO_PLAINTEXT", "true")
	if !optionsFromConfig(config.ServiceConfig{}).channelNoticePlain {
		t.Fatal("explicit option ignored")
	}
}
