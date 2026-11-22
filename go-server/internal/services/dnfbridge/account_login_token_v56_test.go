package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestV56LoginTokenLayoutAndLifecycle(t *testing.T) {
	for _, size := range []int{586, 590, 594, 598, 602, 606, 610, 614, 630} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := make([]byte, size)
			binary.LittleEndian.PutUint32(body, 1)
			body[4] = 0x30
			binary.LittleEndian.PutUint32(body[5:9], 32)
			const token = "de509f65000000000000000000000000"
			copy(body[9:], token)
			if got := clientLoginTokenFromBody(body); got != token {
				t.Fatalf("token = %q", got)
			}
			if !isCurrentChannelReconnectLifecycleBody(1, 1, len(body)) {
				t.Fatal("v56 login is not recognized as lifecycle")
			}
			for _, badSize := range []int{582, 585, 587, 593, 595, 601, 603, 605, 607, 609, 611, 613, 615, 858} {
				if isCurrentChannelReconnectDisplayProbeBodyLen(badSize) {
					t.Fatalf("accepted adjacent/invalid length %d", badSize)
				}
			}
			if isCurrentChannelReconnectLifecycleBody(1, 0, len(body)) {
				t.Fatal("accepted wrong classification")
			}
		})
	}
}

func TestObservedLoginShapeStillRequiresAuthentication(t *testing.T) {
	for _, size := range []int{594, 602, 614, 630} {
		for _, token := range []string{"", "de509f65000000000000000000000000"} {
			service := residentNoticeTestService(t)
			service.options.playerStrict = true
			body := make([]byte, size)
			binary.LittleEndian.PutUint32(body, 1)
			body[4] = 0x30
			binary.LittleEndian.PutUint32(body[5:9], 32)
			copy(body[9:], token)
			binary.LittleEndian.PutUint32(body[54:58], 0x200)
			body[580] = byte((size - 582) / 4)
			raw, err := dnfproto.BuildChannelPacket(1, body, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			packets, rest, skipped, err := dnfproto.SplitLatestGameStream(raw, 65535)
			if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 || packets[0].Kind != dnfproto.LatestGameStreamUpper {
				t.Fatalf("size=%d login stream did not reach upper authentication: packets=%d remaining=%d skipped=%d error=%v", size, len(packets), len(rest), skipped, err)
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn}
			if err := service.handleGameStreamPacket(session, packets[0]); !errors.Is(err, errPlayerAuthentication) {
				t.Fatalf("size=%d unverified token error = %v", size, err)
			}
			if session.accountFromToken || session.accountID != "" || conn.write.Len() != 0 || session.gameEndpointSuccessSent {
				t.Fatalf("size=%d recognizing a login shape must not authenticate or acknowledge an unverified token", size)
			}
		}
	}
}

func TestObservedLoginShapePreservesTokenBinding(t *testing.T) {
	const token = "de509f65000000000000000000000000" // Synthetic fixture.
	for _, size := range []int{594, 602, 614, 630} {
		for _, tc := range []struct {
			name      string
			token     string
			cacheTTL  time.Duration
			wantBound bool
		}{
			{name: "valid", token: token, cacheTTL: time.Hour, wantBound: true},
			{name: "unknown", token: token},
			{name: "missing"},
			{name: "expired", token: token, cacheTTL: -time.Hour},
		} {
			t.Run(fmt.Sprintf("%d/%s", size, tc.name), func(t *testing.T) {
				service := &Service{clientAccounts: newClientAccountRegistry()}
				if tc.cacheTTL != 0 {
					service.loginTokens.remember(token, "dnf:10001", time.Now().Add(tc.cacheTTL))
				}
				body := make([]byte, size)
				binary.LittleEndian.PutUint32(body, 1)
				body[4] = 0x30
				binary.LittleEndian.PutUint32(body[5:9], 32)
				copy(body[9:], tc.token)
				binary.LittleEndian.PutUint32(body[54:58], 0x200)
				body[580] = byte((size - 582) / 4)
				raw, err := dnfproto.BuildChannelPacket(1, body, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				packets, rest, skipped, err := dnfproto.SplitLatestGameStream(raw, 65535)
				if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 || packets[0].Kind != dnfproto.LatestGameStreamUpper {
					t.Fatalf("login did not reach upper token binding: packets=%d remaining=%d skipped=%d error=%v", len(packets), len(rest), skipped, err)
				}
				packet, err := dnfproto.ParseChannelPacket(packets[0].Data)
				if err != nil {
					t.Fatal(err)
				}
				session := &gameSession{accountID: "dnf:1", clientIP: "203.0.113.8"}
				if got := service.bindGameSessionAccountFromLoginToken(session, packet.Header.Classification, packet.Body); got != tc.wantBound {
					t.Fatalf("bound=%t, want %t", got, tc.wantBound)
				}
				wantAccount := "dnf:1"
				if tc.wantBound {
					wantAccount = "dnf:10001"
				}
				if session.accountID != wantAccount || session.accountFromToken != tc.wantBound {
					t.Fatalf("account=%q fromToken=%t; want account=%q fromToken=%t", session.accountID, session.accountFromToken, wantAccount, tc.wantBound)
				}
				if session.loginTokenSeen != (tc.token != "") {
					t.Fatalf("token seen=%t for %s", session.loginTokenSeen, tc.name)
				}
			})
		}
	}
}
