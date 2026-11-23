package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/platform/eventloop"
)

const (
	broadcastNoticeDefaultSender     = "系统公告"
	broadcastNoticeMegaphoneMode     = byte(11)
	broadcastNoticeMegaphoneOpcode   = uint16(118)
	maxBroadcastNoticeMessageBytes   = 220
	maxBroadcastNoticeSenderBytes    = 20
	maxBroadcastNoticeHistoryEntries = 256
)

var broadcastNoticeRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,96}$`)

type broadcastNoticeRequestError struct {
	code    string
	message string
}

func (e *broadcastNoticeRequestError) Error() string {
	return e.message
}

type broadcastNoticeFingerprint struct {
	message string
	style   string
	sender  string
}

type broadcastNoticeEntry struct {
	fingerprint broadcastNoticeFingerprint
	done        chan struct{}
	result      adminapi.BroadcastNoticeResult
}

func broadcastNoticeError(err error) (string, string) {
	var failure *broadcastNoticeRequestError
	if errors.As(err, &failure) {
		return failure.code, failure.message
	}
	return "broadcast_unavailable", "游戏内广播服务暂不可用"
}

func encodeBroadcastNoticeGB18030(text string, maxBytes int) ([]byte, bool) {
	if !utf8.ValidString(text) || strings.ContainsAny(text, "\x00\r\n") {
		return nil, false
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil || len(encoded) == 0 || len(encoded) > maxBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, false
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(encoded)
	if err != nil || string(decoded) != text {
		return nil, false
	}
	return encoded, true
}

func normalizeBroadcastNoticeRequest(req *adminapi.BroadcastNoticeRequest) (requestID, style string, fp broadcastNoticeFingerprint, msgBytes, senderBytes []byte, err error) {
	if req == nil {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "广播参数缺失",
		}
	}
	requestID = req.RequestID
	if requestID == "" || requestID != strings.TrimSpace(requestID) || !broadcastNoticeRequestIDPattern.MatchString(requestID) {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "广播请求编号无效",
		}
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "广播内容不能为空",
		}
	}
	msgBytes, ok := encodeBroadcastNoticeGB18030(msg, maxBroadcastNoticeMessageBytes)
	if !ok {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "广播内容无效或过长（GB18030 编码最多 220 字节且不可含换行）",
		}
	}
	style = strings.TrimSpace(req.Style)
	if style == "" {
		style = "both"
	}
	if style != "both" && style != "system" && style != "megaphone" {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "广播通道须为 both、system 或 megaphone",
		}
	}
	sender := strings.TrimSpace(req.Sender)
	if sender == "" {
		sender = broadcastNoticeDefaultSender
	}
	senderBytes, ok = encodeBroadcastNoticeGB18030(sender, maxBroadcastNoticeSenderBytes)
	if !ok {
		return "", "", broadcastNoticeFingerprint{}, nil, nil, &broadcastNoticeRequestError{
			code: "invalid_broadcast", message: "发送者标识无效或过长（GB18030 编码最多 20 字节）",
		}
	}
	fp = broadcastNoticeFingerprint{message: msg, style: style, sender: sender}
	return requestID, style, fp, msgBytes, senderBytes, nil
}

// buildBroadcastSystemNoticeBody matches the verified NOTI12 (0x2035370) type=0
// system-text layout used in town_quest_text.go (no sender actor required).
func buildBroadcastSystemNoticeBody(message []byte) []byte {
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0xffff)
	w.writeByte(0)
	w.writeRawDstr(message)
	w.writeRawDstr(nil)
	return w.bytes()
}

func (s *Service) administrativeBroadcastNotice(ctx context.Context, req *adminapi.BroadcastNoticeRequest) (*adminapi.BroadcastNoticeResult, error) {
	if s == nil {
		return nil, &broadcastNoticeRequestError{code: "broadcast_unavailable", message: "游戏内广播服务暂不可用"}
	}
	requestID, style, fp, msgBytes, senderBytes, err := normalizeBroadcastNoticeRequest(req)
	if err != nil {
		return nil, err
	}
	entry, existing := s.reserveBroadcastNoticeEntry(requestID, fp)
	if existing {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, &broadcastNoticeRequestError{code: "broadcast_unavailable", message: "广播请求等待超时，请使用原编号重试"}
		}
		if entry.fingerprint != fp {
			return nil, &broadcastNoticeRequestError{code: "broadcast_conflict", message: "广播请求编号已用于不同内容"}
		}
		res := entry.result
		return &res, nil
	}

	var result adminapi.BroadcastNoticeResult
	defer func() {
		entry.result = result
		close(entry.done)
	}()

	onlineSessions, targets := s.snapshotBroadcastNoticeTargets()
	var systemBody []byte
	if style == "system" || style == "both" {
		systemBody = buildBroadcastSystemNoticeBody(msgBytes)
	}
	queued := 0
	for _, target := range targets {
		if err := s.queueBroadcastNoticeDelivery(target, style, fp.sender, senderBytes, msgBytes, systemBody); err != nil {
			s.logWarn("dnfbridge admin broadcast recipient skipped",
				"request_id", requestID,
				"target_char_id", target.character,
				"error", err)
			continue
		}
		queued++
	}
	result = adminapi.BroadcastNoticeResult{
		RequestID:        requestID,
		Style:            style,
		OnlineSessions:   onlineSessions,
		SelectedSessions: len(targets),
		QueuedSessions:   queued,
	}
	s.logInfo("game-admin-broadcast-notice",
		"request_id", requestID,
		"style", style,
		"online_sessions", result.OnlineSessions,
		"selected_sessions", result.SelectedSessions,
		"queued_sessions", result.QueuedSessions)
	out := result
	return &out, nil
}

func (s *Service) reserveBroadcastNoticeEntry(requestID string, fp broadcastNoticeFingerprint) (*broadcastNoticeEntry, bool) {
	s.broadcastNoticeMu.Lock()
	defer s.broadcastNoticeMu.Unlock()
	if existing, ok := s.broadcastNoticeEntries[requestID]; ok {
		return existing, true
	}
	if s.broadcastNoticeEntries == nil {
		s.broadcastNoticeEntries = make(map[string]*broadcastNoticeEntry)
	}
	for checked := 0; len(s.broadcastNoticeOrder) >= maxBroadcastNoticeHistoryEntries && checked < len(s.broadcastNoticeOrder); checked++ {
		oldest := s.broadcastNoticeOrder[0]
		s.broadcastNoticeOrder = s.broadcastNoticeOrder[1:]
		if old := s.broadcastNoticeEntries[oldest]; old != nil {
			select {
			case <-old.done:
				delete(s.broadcastNoticeEntries, oldest)
			default:
				s.broadcastNoticeOrder = append(s.broadcastNoticeOrder, oldest)
			}
		}
	}
	entry := &broadcastNoticeEntry{fingerprint: fp, done: make(chan struct{})}
	s.broadcastNoticeEntries[requestID] = entry
	s.broadcastNoticeOrder = append(s.broadcastNoticeOrder, requestID)
	return entry, false
}

func (s *Service) snapshotBroadcastNoticeTargets() (int, []boundGameSessionCharacter) {
	s.mu.Lock()
	onlineSessions := 0
	for _, session := range s.liveGameSessions {
		if session != nil && !session.loginRevoked.Load() {
			onlineSessions++
		}
	}
	targets := make([]boundGameSessionCharacter, 0, len(s.gameSessions))
	for id, session := range s.gameSessions {
		if id == 0 || session == nil || session.selectedCharacterID != id || session.loginRevoked.Load() || session.conn == nil {
			continue
		}
		targets = append(targets, boundGameSessionCharacter{
			session:    session,
			character:  id,
			generation: session.characterGeneration,
		})
	}
	s.mu.Unlock()
	if onlineSessions < len(targets) {
		onlineSessions = len(targets)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].character < targets[j].character })
	return onlineSessions, targets
}

// broadcastNoticeRecipientName resolves the recipient's active character name
// from in-memory caches first (zero DB queries on normal selected sessions),
// falling back to the character repository only when caches are empty.
func (s *Service) broadcastNoticeRecipientName(ctx context.Context, target boundGameSessionCharacter) string {
	if p := target.session; p != nil && p.launcherCharacterID == target.character && p.launcherCharacterName != "" {
		return p.launcherCharacterName
	}
	if s.onlinePlayers != nil {
		if info, found := s.onlinePlayers.PlayerForCharacter(target.character); found && info.Session == target.session && info.Name != "" {
			return info.Name
		}
	}
	if repos, ok := s.repositoryGroup(); ok && repos.Character != nil {
		if ch, found, err := repos.Character.Load(ctx, strconv.Itoa(int(target.character))); err == nil && found && ch.Name != "" {
			return ch.Name
		}
	}
	return ""
}

// disambiguateBroadcastMegaphoneSender ensures NOTI118's name DSTR never equals
// the recipient's local character name. DNF.exe (0x200CE2C-0x200CE7B) compares
// the decoded name string (not senderUID) to predict a local item-36 debit for
// mode=11. Wrapping a colliding sender (<=20 GB18030 bytes) in brackets yields
// <=22 bytes (<30 wide-char buffer at 0x200CA71) and is strictly longer than
// the recipient's name.
func disambiguateBroadcastMegaphoneSender(sender string, senderBytes []byte, recipientName string) []byte {
	if recipientName == "" || (sender != recipientName && !strings.EqualFold(sender, recipientName)) {
		return senderBytes
	}
	alt := make([]byte, 0, len(senderBytes)+2)
	alt = append(alt, '[')
	alt = append(alt, senderBytes...)
	alt = append(alt, ']')
	return alt
}

// queueBroadcastNoticeDelivery enqueues delivery onto the recipient's own bounded
// event loop without holding any Service lock across network I/O. One slow or
// broken socket fails locally and never blocks or disconnects other sessions.
func (s *Service) queueBroadcastNoticeDelivery(target boundGameSessionCharacter, style, sender string, senderBytes, msgBytes, systemBody []byte) error {
	p := target.session
	if p == nil || p.conn == nil || p.loginRevoked.Load() {
		return errInvalidGameSessionEvent
	}
	senderCopy := append([]byte(nil), senderBytes...)
	msgCopy := append([]byte(nil), msgBytes...)
	systemCopy := append([]byte(nil), systemBody...)
	expires := time.Now().Add(createWriteTimeout)

	deliver := func() error {
		ctx, cancel := guildChatContext(p)
		defer cancel()
		ctx, deadline := context.WithDeadline(ctx, expires)
		defer deadline()
		validate := func() error {
			if p.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(target) {
				return errInvalidGameSessionEvent
			}
			return nil
		}
		if style == "megaphone" || style == "both" {
			ch, ok := currentSessionOwnerChannelField(p)
			if !ok {
				ch = 0
			}
			recipientName := s.broadcastNoticeRecipientName(ctx, target)
			wireSender := disambiguateBroadcastMegaphoneSender(sender, senderCopy, recipientName)
			megaBody := buildCurrentMegaphoneBody(broadcastNoticeMegaphoneMode, 0, ch, 0, wireSender, msgCopy)
			if err := s.sendCurrentGuildChatFrameChecked(ctx, p, broadcastNoticeMegaphoneOpcode, megaBody, 0, validate); err != nil {
				return err
			}
		}
		if style == "system" || style == "both" {
			if err := s.sendCurrentGuildChatFrameChecked(ctx, p, currentAreaChatNotifyOpcode, systemCopy, 0, validate); err != nil {
				return err
			}
		}
		return nil
	}

	if p.events == nil || p.events.loop == nil {
		return deliver()
	}
	if !p.events.active.Load() {
		return errInvalidGameSessionEvent
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return p.events.loop.Submit(eventloop.Event{
		Type:     "game-admin-broadcast-delivery",
		Priority: eventloop.PriorityMedium,
		Context:  ctx,
		Data: gameSessionEventTask{
			source:              "game-admin-broadcast-delivery",
			characterID:         target.character,
			characterGeneration: target.generation,
			async:               true,
			run: func() error {
				if err := deliver(); err != nil {
					s.logWarn("dnfbridge admin broadcast delivery failed",
						"target_char_id", target.character,
						"error", err)
				}
				return nil
			},
		},
	})
}
