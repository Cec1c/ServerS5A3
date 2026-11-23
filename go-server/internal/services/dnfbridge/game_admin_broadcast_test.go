package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepoMem "longheng.io/server/internal/modules/dnf/repository/memory"
)

func newLoopbackTCPPair(t *testing.T) (serverConn, clientConn net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- c
	}()
	client, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acceptErr:
		client.Close()
		t.Fatal(err)
		return nil, nil
	case server := <-accepted:
		t.Cleanup(func() {
			_ = server.Close()
			_ = client.Close()
		})
		return server, client
	case <-time.After(2 * time.Second):
		client.Close()
		t.Fatal("loopback tcp accept timed out")
		return nil, nil
	}
}

func readUpperFramesFromConn(t *testing.T, conn net.Conn, want int) []dnfproto.ChannelPacket {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var buf []byte
	tmp := make([]byte, 4096)
	var packets []dnfproto.ChannelPacket
	for len(packets) < want {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for len(buf) >= dnfproto.GameServerUpperHeaderSize {
				size := int(binary.LittleEndian.Uint32(buf[3:7]))
				if size < dnfproto.GameServerUpperHeaderSize || len(buf) < size {
					break
				}
				pkt, rest := splitCurrentGameServerUpperPacketAuto(t, buf)
				packets = append(packets, pkt)
				buf = rest
			}
		}
		if err != nil {
			t.Fatalf("readUpperFramesFromConn got %d/%d packets before err=%v", len(packets), want, err)
		}
	}
	return packets
}

func expectNoMoreBytesOnConn(t *testing.T, conn net.Conn, wait time.Duration) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var one [1]byte
	n, err := conn.Read(one[:])
	if n > 0 {
		t.Fatalf("unexpected extra byte on conn: %x", one[:n])
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected read timeout (idle conn), got err=%v", err)
	}
}

func readDstrGB18030(t *testing.T, b []byte) (string, []byte) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("short dstr len=%d", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b[:4]))
	if n < 0 || len(b) < 4+n {
		t.Fatalf("truncated dstr want=%d have=%d", n, len(b)-4)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b[4 : 4+n])
	if err != nil {
		t.Fatalf("decode gb18030 failed: %v", err)
	}
	return string(decoded), b[4+n:]
}

func startBroadcastTestSession(t *testing.T, s *Service, conn net.Conn, connID string, charID uint16, channelID int) *gameSession {
	return startBroadcastTestSessionNamed(t, s, conn, connID, charID, channelID, "")
}

func startBroadcastTestSessionNamed(t *testing.T, s *Service, conn net.Conn, connID string, charID uint16, channelID int, charName string) *gameSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	session := &gameSession{
		conn:                            conn,
		connID:                          connID,
		ctx:                             ctx,
		channel:                         channelcatalog.Channel{ID: channelID},
		selectedCharacterID:             charID,
		characterGeneration:             uint64(charID),
		launcherCharacterID:             charID,
		launcherCharacterName:           charName,
		connectionTownActorOwnerChannel: byte(channelID),
		townActorOwnerChannel:           byte(channelID),
	}
	s.mu.Lock()
	if s.liveGameSessions == nil {
		s.liveGameSessions = make(map[string]*gameSession)
	}
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	s.liveGameSessions[connID] = session
	if charID != 0 {
		s.gameSessions[charID] = session
	}
	s.mu.Unlock()
	if charID != 0 {
		if err := s.startGameSessionEvents(ctx, session); err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer stopCancel()
			_ = s.stopGameSessionEvents(stopCtx, session)
		})
	} else {
		t.Cleanup(cancel)
	}
	return session
}

func postGameAdminJSON(t *testing.T, url, auth, rawBody string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(rawBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func decodeStrictAdminResponse(t *testing.T, raw []byte) adminapi.Response {
	t.Helper()
	var out adminapi.Response
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode adminapi.Response failed: %v body=%s", err, string(raw))
	}
	return out
}

func TestGameAdminBroadcastNoticeEndToEndTCPAndHTTP(t *testing.T) {
	secret := strings.Repeat("b", 32)
	s := &Service{
		options: options{
			playerAdminSecret:        secret,
			currentClientVersion:     "official-24.9",
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
		},
	}
	api := &accountAPIServer{service: s}
	mux := http.NewServeMux()
	mux.HandleFunc(adminapi.Path, api.handleGameAdmin)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()
	endpoint := httpSrv.URL + adminapi.Path

	srv1, cli1 := newLoopbackTCPPair(t)
	srv2, cli2 := newLoopbackTCPPair(t)
	srvLobby, _ := newLoopbackTCPPair(t)
	p1 := startBroadcastTestSession(t, s, srv1, "conn-101", 101, 5)
	p2 := startBroadcastTestSession(t, s, srv2, "conn-102", 102, 11)
	_ = startBroadcastTestSession(t, s, srvLobby, "conn-lobby", 0, 5)

	// 1. style="both" (default style & default sender "系统公告")
	bothMsg := "【仅测试公告，不是更新，不更新】全服双通道验证。"
	code, raw := postGameAdminJSON(t, endpoint, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"e2e-both-20261123","message":"  `+bothMsg+`  "}}`)
	if code != http.StatusOK {
		t.Fatalf("both HTTP status=%d body=%s", code, string(raw))
	}
	res := decodeStrictAdminResponse(t, raw)
	if !res.OK || res.BroadcastNotice == nil {
		t.Fatalf("both unexpected response: %+v", res)
	}
	if res.BroadcastNotice.RequestID != "e2e-both-20261123" ||
		res.BroadcastNotice.Style != "both" ||
		res.BroadcastNotice.OnlineSessions != 3 ||
		res.BroadcastNotice.SelectedSessions != 2 ||
		res.BroadcastNotice.QueuedSessions != 2 {
		t.Fatalf("both receipt mismatch: %+v", res.BroadcastNotice)
	}

	for _, tc := range []struct {
		name    string
		cli     net.Conn
		wantCh  byte
		session *gameSession
	}{
		{"player101", cli1, 5, p1},
		{"player102", cli2, 11, p2},
	} {
		frames := readUpperFramesFromConn(t, tc.cli, 2)
		mega, sys := frames[0], frames[1]
		if mega.Header.Classification != 0 || mega.Header.MsgID != 118 {
			t.Fatalf("%s frame0 header=%+v want class=0 msg=118", tc.name, mega.Header)
		}
		if len(mega.Body) < 6 || mega.Body[0] != 11 || mega.Body[1] != 0 || mega.Body[2] != 0 || mega.Body[3] != tc.wantCh {
			t.Fatalf("%s megaphone prefix=%x want mode=11 group=0 deco=0 ch=%d", tc.name, mega.Body[:min(6, len(mega.Body))], tc.wantCh)
		}
		if gotUID := binary.LittleEndian.Uint16(mega.Body[4:6]); gotUID != 0 {
			t.Fatalf("%s megaphone senderUID=%d want 0", tc.name, gotUID)
		}
		senderText, rest := readDstrGB18030(t, mega.Body[6:])
		msgText, rest := readDstrGB18030(t, rest)
		groupText, rest := readDstrGB18030(t, rest)
		if senderText != "系统公告" || msgText != bothMsg || groupText != "" || len(rest) != 0 {
			t.Fatalf("%s megaphone fields sender=%q msg=%q group=%q tail=%d", tc.name, senderText, msgText, groupText, len(rest))
		}

		if sys.Header.Classification != 0 || sys.Header.MsgID != currentAreaChatNotifyOpcode {
			t.Fatalf("%s frame1 header=%+v want class=0 msg=%d", tc.name, sys.Header, currentAreaChatNotifyOpcode)
		}
		if len(sys.Body) < 4 || sys.Body[0] != 0 || binary.LittleEndian.Uint16(sys.Body[1:3]) != 0xffff || sys.Body[3] != 0 {
			t.Fatalf("%s system prefix=%x", tc.name, sys.Body[:min(4, len(sys.Body))])
		}
		sysMsg, rest := readDstrGB18030(t, sys.Body[4:])
		extra, rest := readDstrGB18030(t, rest)
		if sysMsg != bothMsg || extra != "" || len(rest) != 0 {
			t.Fatalf("%s system fields msg=%q extra=%q tail=%d", tc.name, sysMsg, extra, len(rest))
		}
		flushGuildChatTestEvents(t, s, tc.session)
	}

	// 2. Idempotent retry with identical normalized payload must NOT re-send
	code, raw = postGameAdminJSON(t, endpoint, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"e2e-both-20261123","message":"`+bothMsg+`","style":"both","sender":"系统公告"}}`)
	if code != http.StatusOK {
		t.Fatalf("idempotent retry HTTP status=%d", code)
	}
	retryRes := decodeStrictAdminResponse(t, raw)
	if !retryRes.OK || retryRes.BroadcastNotice == nil || *retryRes.BroadcastNotice != *res.BroadcastNotice {
		t.Fatalf("idempotent retry mismatch: got=%+v want=%+v", retryRes, res)
	}
	expectNoMoreBytesOnConn(t, cli1, 80*time.Millisecond)
	expectNoMoreBytesOnConn(t, cli2, 80*time.Millisecond)

	// 3. Same requestId with different content must return broadcast_conflict
	code, raw = postGameAdminJSON(t, endpoint, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"e2e-both-20261123","message":"不同内容","style":"both"}}`)
	if code != http.StatusOK {
		t.Fatalf("conflict HTTP status=%d", code)
	}
	conflictRes := decodeStrictAdminResponse(t, raw)
	if conflictRes.OK || conflictRes.ErrorCode != "broadcast_conflict" {
		t.Fatalf("expected broadcast_conflict, got %+v", conflictRes)
	}
	expectNoMoreBytesOnConn(t, cli1, 80*time.Millisecond)

	// 4. style="megaphone" with custom sender sends ONLY NOTI 118
	megaMsg := "【仅测试公告】喇叭单通道验证。"
	code, raw = postGameAdminJSON(t, endpoint, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"e2e-mega-20261123","message":"`+megaMsg+`","style":"megaphone","sender":"维护通知"}}`)
	if code != http.StatusOK {
		t.Fatalf("megaphone HTTP status=%d", code)
	}
	megaRes := decodeStrictAdminResponse(t, raw)
	if !megaRes.OK || megaRes.BroadcastNotice == nil || megaRes.BroadcastNotice.Style != "megaphone" || megaRes.BroadcastNotice.QueuedSessions != 2 {
		t.Fatalf("megaphone receipt=%+v", megaRes)
	}
	for _, cli := range []net.Conn{cli1, cli2} {
		frames := readUpperFramesFromConn(t, cli, 1)
		if frames[0].Header.MsgID != 118 {
			t.Fatalf("want NOTI 118, got %d", frames[0].Header.MsgID)
		}
		senderText, rest := readDstrGB18030(t, frames[0].Body[6:])
		msgText, _ := readDstrGB18030(t, rest)
		if senderText != "维护通知" || msgText != megaMsg {
			t.Fatalf("custom megaphone sender=%q msg=%q", senderText, msgText)
		}
		expectNoMoreBytesOnConn(t, cli, 80*time.Millisecond)
	}

	// 5. style="system" sends ONLY NOTI 12
	sysOnlyMsg := "【仅测试公告】系统文字单通道验证。"
	code, raw = postGameAdminJSON(t, endpoint, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"e2e-sys-20261123","message":"`+sysOnlyMsg+`","style":"system"}}`)
	if code != http.StatusOK {
		t.Fatalf("system HTTP status=%d", code)
	}
	sysRes := decodeStrictAdminResponse(t, raw)
	if !sysRes.OK || sysRes.BroadcastNotice == nil || sysRes.BroadcastNotice.Style != "system" || sysRes.BroadcastNotice.QueuedSessions != 2 {
		t.Fatalf("system receipt=%+v", sysRes)
	}
	for _, cli := range []net.Conn{cli1, cli2} {
		frames := readUpperFramesFromConn(t, cli, 1)
		if frames[0].Header.MsgID != currentAreaChatNotifyOpcode {
			t.Fatalf("want NOTI 12, got %d", frames[0].Header.MsgID)
		}
		gotMsg, _ := readDstrGB18030(t, frames[0].Body[4:])
		if gotMsg != sysOnlyMsg {
			t.Fatalf("system msg=%q want=%q", gotMsg, sysOnlyMsg)
		}
		expectNoMoreBytesOnConn(t, cli, 80*time.Millisecond)
	}
}

func TestGameAdminBroadcastNoticeValidationAndAuth(t *testing.T) {
	secret := strings.Repeat("v", 32)
	s := &Service{options: options{playerAdminSecret: secret, gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain}}
	api := &accountAPIServer{service: s}
	srv := httptest.NewServer(http.HandlerFunc(api.handleGameAdmin))
	defer srv.Close()

	// Auth failure preserves 404 private stealth contract
	if code, _ := postGameAdminJSON(t, srv.URL, "", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"auth-test-1","message":"ok"}}`); code != http.StatusNotFound {
		t.Fatalf("missing auth status=%d want 404", code)
	}
	if code, _ := postGameAdminJSON(t, srv.URL, strings.Repeat("w", 32), `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"auth-test-2","message":"ok"}}`); code != http.StatusNotFound {
		t.Fatalf("wrong auth status=%d want 404", code)
	}
	// Unknown JSON field returns 400
	if code, _ := postGameAdminJSON(t, srv.URL, secret, `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"auth-test-3","message":"ok","unknown":1}}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d want 400", code)
	}

	invalidPayloads := []struct {
		name string
		body string
	}{
		{"missing_object", `{"version":1,"action":"broadcast_notice"}`},
		{"empty_request_id", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"","message":"hi"}}`},
		{"short_request_id", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"short","message":"hi"}}`},
		{"empty_message", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-req-01","message":"   "}}`},
		{"newline_message", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-req-02","message":"a\nb"}}`},
		{"overlong_message_222b", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-req-03","message":"` + strings.Repeat("维", 111) + `"}}`},
		{"bad_style", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-req-04","message":"hi","style":"popup"}}`},
		{"overlong_sender_22b", `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-req-05","message":"hi","sender":"` + strings.Repeat("公", 11) + `"}}`},
	}
	for _, tc := range invalidPayloads {
		code, raw := postGameAdminJSON(t, srv.URL, secret, tc.body)
		if code != http.StatusOK {
			t.Fatalf("%s status=%d want 200 JSON error envelope", tc.name, code)
		}
		res := decodeStrictAdminResponse(t, raw)
		if res.OK || res.ErrorCode != "invalid_broadcast" {
			t.Fatalf("%s got %+v want invalid_broadcast", tc.name, res)
		}
	}

	// Exact boundary lengths (220 GB18030 bytes message, 20 GB18030 bytes sender) succeed with 0 online
	boundaryBody := `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"valid-boundary-01","message":"` + strings.Repeat("维", 110) + `","sender":"` + strings.Repeat("公", 10) + `","style":"both"}}`
	code, raw := postGameAdminJSON(t, srv.URL, secret, boundaryBody)
	if code != http.StatusOK {
		t.Fatalf("boundary status=%d", code)
	}
	res := decodeStrictAdminResponse(t, raw)
	if !res.OK || res.BroadcastNotice == nil || res.BroadcastNotice.QueuedSessions != 0 {
		t.Fatalf("boundary res=%+v", res)
	}
}

func TestGameAdminBroadcastNoticeSlowClosedIsolationAndConcurrency(t *testing.T) {
	secret := strings.Repeat("i", 32)
	s := &Service{
		options: options{
			playerAdminSecret:        secret,
			currentClientVersion:     "official-24.9",
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
		},
	}
	api := &accountAPIServer{service: s}
	srv := httptest.NewServer(http.HandlerFunc(api.handleGameAdmin))
	defer srv.Close()

	// Slow peer: net.Pipe with nobody reading (writes block until deadline)
	slowSrv, slowCli := net.Pipe()
	defer slowSrv.Close()
	defer slowCli.Close()
	// Closed peer: client side closed before broadcast
	closedSrv, closedCli := net.Pipe()
	defer closedSrv.Close()
	_ = closedCli.Close()
	// Healthy peer: real loopback TCP
	healthySrv, healthyCli := newLoopbackTCPPair(t)

	_ = startBroadcastTestSession(t, s, slowSrv, "conn-slow", 201, 5)
	_ = startBroadcastTestSession(t, s, closedSrv, "conn-closed", 202, 5)
	healthy := startBroadcastTestSession(t, s, healthySrv, "conn-healthy", 203, 7)

	start := time.Now()
	const reqID = "concurrent-iso-20261123"
	body := `{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"` + reqID + `","message":"【仅测试公告】隔离并发测试","style":"both"}}`

	var wg sync.WaitGroup
	results := make([]adminapi.Response, 4)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			code, raw := postGameAdminJSON(t, srv.URL, secret, body)
			if code != http.StatusOK {
				t.Errorf("goroutine %d status=%d", idx, code)
				return
			}
			results[idx] = decodeStrictAdminResponse(t, raw)
		}(i)
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("broadcast HTTP blocked by slow peer: elapsed=%v", elapsed)
	}
	for i, res := range results {
		if !res.OK || res.BroadcastNotice == nil || res.BroadcastNotice.RequestID != reqID || res.BroadcastNotice.SelectedSessions != 3 {
			t.Fatalf("goroutine %d unexpected result=%+v", i, res)
		}
	}

	// Healthy peer must receive exactly 2 frames (NOTI 118 + NOTI 12) despite 4 concurrent identical requests and slow/closed peers
	frames := readUpperFramesFromConn(t, healthyCli, 2)
	if frames[0].Header.MsgID != 118 || frames[1].Header.MsgID != currentAreaChatNotifyOpcode {
		t.Fatalf("healthy peer frames=%d,%d", frames[0].Header.MsgID, frames[1].Header.MsgID)
	}
	flushGuildChatTestEvents(t, s, healthy)
	expectNoMoreBytesOnConn(t, healthyCli, 80*time.Millisecond)

	// Unblock slow peer before cleanup finishes so session loop exits promptly
	_ = slowCli.Close()
}

func TestGameAdminBroadcastNoticeSenderNameCollisionDisambiguation(t *testing.T) {
	secret := strings.Repeat("n", 32)
	repos := dnfrepoMem.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "303",
		AccountID:   "dnf:303",
		Name:        "系统公告",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		options: options{
			playerAdminSecret:        secret,
			currentClientVersion:     "official-24.9",
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	api := &accountAPIServer{service: s}
	srv := httptest.NewServer(http.HandlerFunc(api.handleGameAdmin))
	defer srv.Close()

	srvDefaultClash, cliDefaultClash := newLoopbackTCPPair(t)
	srvCustomClash, cliCustomClash := newLoopbackTCPPair(t)
	srvRepoFallback, cliRepoFallback := newLoopbackTCPPair(t)
	srvNormal, cliNormal := newLoopbackTCPPair(t)

	pDefault := startBroadcastTestSessionNamed(t, s, srvDefaultClash, "conn-301", 301, 5, "系统公告")
	pCustom := startBroadcastTestSessionNamed(t, s, srvCustomClash, "conn-302", 302, 5, "维护通知")
	// Empty launcherCharacterName exercises repository fallback lookup.
	pRepo := startBroadcastTestSessionNamed(t, s, srvRepoFallback, "conn-303", 303, 5, "")
	pNormal := startBroadcastTestSessionNamed(t, s, srvNormal, "conn-304", 304, 5, "普通冒险家")

	// 1. Default sender "系统公告": collides with pDefault (cached) and pRepo (repo fallback), not pCustom/pNormal
	code, raw := postGameAdminJSON(t, srv.URL, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"clash-default-01","message":"【仅测试公告】同名避让默认署名","style":"megaphone"}}`)
	if code != http.StatusOK || !decodeStrictAdminResponse(t, raw).OK {
		t.Fatalf("default clash failed: code=%d body=%s", code, string(raw))
	}
	for _, tc := range []struct {
		name       string
		cli        net.Conn
		wantSender string
		session    *gameSession
	}{
		{"cached_default_clash", cliDefaultClash, "[系统公告]", pDefault},
		{"custom_other", cliCustomClash, "系统公告", pCustom},
		{"repo_fallback_clash", cliRepoFallback, "[系统公告]", pRepo},
		{"normal_player", cliNormal, "系统公告", pNormal},
	} {
		frames := readUpperFramesFromConn(t, tc.cli, 1)
		if frames[0].Header.MsgID != 118 || frames[0].Body[0] != 11 || frames[0].Body[1] != 0 {
			t.Fatalf("%s unexpected frame header=%+v prefix=%x", tc.name, frames[0].Header, frames[0].Body[:4])
		}
		gotSender, _ := readDstrGB18030(t, frames[0].Body[6:])
		if gotSender != tc.wantSender {
			t.Fatalf("%s sender=%q want=%q", tc.name, gotSender, tc.wantSender)
		}
		flushGuildChatTestEvents(t, s, tc.session)
	}

	// 2. Custom sender "维护通知": collides only with pCustom
	code, raw = postGameAdminJSON(t, srv.URL, secret,
		`{"version":1,"action":"broadcast_notice","broadcastNotice":{"requestId":"clash-custom-01","message":"【仅测试公告】同名避让自定义署名","style":"megaphone","sender":"维护通知"}}`)
	if code != http.StatusOK || !decodeStrictAdminResponse(t, raw).OK {
		t.Fatalf("custom clash failed: code=%d body=%s", code, string(raw))
	}
	for _, tc := range []struct {
		name       string
		cli        net.Conn
		wantSender string
		session    *gameSession
	}{
		{"default_other", cliDefaultClash, "维护通知", pDefault},
		{"custom_clash", cliCustomClash, "[维护通知]", pCustom},
		{"repo_other", cliRepoFallback, "维护通知", pRepo},
		{"normal_player", cliNormal, "维护通知", pNormal},
	} {
		frames := readUpperFramesFromConn(t, tc.cli, 1)
		gotSender, _ := readDstrGB18030(t, frames[0].Body[6:])
		if gotSender != tc.wantSender {
			t.Fatalf("%s sender=%q want=%q", tc.name, gotSender, tc.wantSender)
		}
		flushGuildChatTestEvents(t, s, tc.session)
	}

	// 3. Max 20-byte GB18030 sender disambiguation stays within 22 bytes (< 30 wide-char NOTI118 limit)
	maxSender := strings.Repeat("公", 10)
	maxBytes, ok := encodeBroadcastNoticeGB18030(maxSender, maxBroadcastNoticeSenderBytes)
	if !ok || len(maxBytes) != 20 {
		t.Fatalf("maxSender encode ok=%v len=%d", ok, len(maxBytes))
	}
	alt := disambiguateBroadcastMegaphoneSender(maxSender, maxBytes, maxSender)
	if len(alt) != 22 {
		t.Fatalf("disambiguated max sender bytes=%d want=22", len(alt))
	}
	decodedAlt, err := simplifiedchinese.GB18030.NewDecoder().Bytes(alt)
	if err != nil || string(decodedAlt) != "["+maxSender+"]" {
		t.Fatalf("disambiguated max sender=%q err=%v", string(decodedAlt), err)
	}
}
