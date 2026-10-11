package dnfbridge

import (
	"bytes"
	"golang.org/x/text/encoding/simplifiedchinese"
	"strings"
	"unicode"
	"unicode/utf16"
)

const (
	currentAccountFriendInfoOpcode         = uint16(1371)
	currentAccountFriendPendingOpcode      = uint16(1372)
	currentAccountFriendDeleteOpcode       = uint16(1373)
	currentAccountFriendRequestOpcode      = uint16(1436)
	currentAccountFriendAcceptOpcode       = uint16(1437)
	currentAccountFriendRemoveOpcode       = uint16(1438)
	currentAccountFriendRefuseCancelOpcode = uint16(1439)
	currentAccountFriendNoteOpcode         = uint16(1440)
)

type currentAccountFriendRequest struct {
	Target, Text string
	Action       byte
}

// BBBDA9/BBBE17/BBBE93/BBA58D/BBBF15 are the actual send sequences.
// 1438 and 1440 select the character-name field at friend-row +54; the other
// commands select the adventure name from the pending row or name input.
func decodeCurrentAccountFriendRequest(op uint16, body []byte) (currentAccountFriendRequest, error) {
	var r currentAccountFriendRequest
	offset := 0
	switch op {
	case currentAccountFriendRefuseCancelOpcode:
		if len(body) == 0 || body[0] != 4 && body[0] != 5 {
			return r, errCurrentSocialBodyInvalid
		}
		r.Action = body[0]
		offset = 1
	case currentAccountFriendRequestOpcode, currentAccountFriendAcceptOpcode, currentAccountFriendRemoveOpcode, currentAccountFriendNoteOpcode:
	default:
		return r, errCurrentSocialBodyInvalid
	}
	raw, end, ok := readCurrentRawDstr(body, offset)
	if !ok || len(raw) == 0 || len(raw) > 16 {
		return r, errCurrentSocialBodyInvalid
	}
	target, err := decodeCharacterName(raw)
	if err != nil || target == "" || strings.TrimSpace(target) != target {
		return r, errCurrentSocialBodyInvalid
	}
	for _, c := range target {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return r, errCurrentSocialBodyInvalid
		}
	}
	r.Target = target
	offset = end
	if op == currentAccountFriendRequestOpcode || op == currentAccountFriendNoteOpcode {
		text, end, ok := readCurrentRawDstr(body, offset)
		if !ok || len(text) > 160 {
			return r, errCurrentSocialBodyInvalid
		}
		value, err := simplifiedchinese.GB18030.NewDecoder().Bytes(text)
		if err != nil {
			return r, errCurrentSocialBodyInvalid
		}
		check, err := simplifiedchinese.GB18030.NewEncoder().Bytes(value)
		if err != nil || !bytes.Equal(check, text) {
			return r, errCurrentSocialBodyInvalid
		}
		r.Text = string(value)
		if len(utf16.Encode([]rune(r.Text))) > 40 {
			return r, errCurrentSocialBodyInvalid
		}
		for _, c := range r.Text {
			if unicode.IsControl(c) {
				return r, errCurrentSocialBodyInvalid
			}
		}
		if op == currentAccountFriendRequestOpcode && strings.TrimSpace(r.Text) == "" {
			return r, errCurrentSocialBodyInvalid
		}
		offset = end
	}
	if offset != len(body) {
		return r, errCurrentSocialBodyInvalid
	}
	return r, nil
}

// Current callbacks all consume a successful ACK tail. A one-byte success
// alone would make the native DSTR reader overrun the packet.
func buildCurrentAccountFriendSuccessTail(op uint16, r currentAccountFriendRequest) ([]byte, error) {
	var w packetWriter
	if op == currentAccountFriendRefuseCancelOpcode {
		w.writeByte(r.Action)
	}
	if err := writeCurrentAccountFriendDstr(&w, r.Target); err != nil {
		return nil, err
	}
	if op == currentAccountFriendNoteOpcode {
		if err := writeCurrentAccountFriendDstr(&w, r.Text); err != nil {
			return nil, err
		}
	}
	switch op {
	case 1436, 1437, 1438, 1439, 1440:
		return w.bytes(), nil
	default:
		return nil, errCurrentSocialBodyInvalid
	}
}

// Fields map precisely to 0201FE50's 208-byte in-memory record. Neutral fields
// retain explicit inactive values until their specialized services are proven.
// Channel at +52, level/job/grow and the two names are independent fields.
type currentAccountFriendNativeRow struct {
	Head                  uint16
	NeutralState          byte
	AdventureName         string
	Level, Job, Grow      byte
	Channel               uint16
	CharacterName, Remark string
	NeutralTimestamp      uint32
	NeutralFlag           bool
}
type currentAccountFriendPendingRow struct {
	Kind                   byte
	AdventureName, Message string
	ExpiresUnix            uint32
}

func nativeAccountFriendTextValid(text string, capacity int) bool {
	if len(utf16.Encode([]rune(text))) >= capacity {
		return false
	}
	for _, c := range text {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// Remarks/messages allow 40 WCHAR. The roster-name encoder truncates to 30
// bytes and therefore cannot encode these fields without silently losing text.
func writeCurrentAccountFriendDstr(w *packetWriter, text string) error {
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return err
	}
	w.writeRawDstr(encoded)
	return nil
}
func buildCurrentAccountFriendInfoBody(rows []currentAccountFriendNativeRow) ([]byte, error) {
	if len(rows) > 255 {
		return nil, errCurrentSocialBodyInvalid
	}
	var w packetWriter
	w.writeByte(byte(len(rows)))
	for _, r := range rows {
		if r.AdventureName == "" || r.CharacterName == "" || !nativeAccountFriendTextValid(r.AdventureName, 30) || !nativeAccountFriendTextValid(r.CharacterName, 17) || !nativeAccountFriendTextValid(r.Remark, 41) {
			return nil, errCurrentSocialBodyInvalid
		}
		w.writeUint16(r.Head)
		w.writeByte(r.NeutralState)
		if err := writeCurrentAccountFriendDstr(&w, r.AdventureName); err != nil {
			return nil, err
		}
		w.writeByte(r.Level)
		w.writeByte(r.Job)
		w.writeByte(r.Grow)
		w.writeUint16(r.Channel)
		if err := writeCurrentAccountFriendDstr(&w, r.CharacterName); err != nil {
			return nil, err
		}
		if err := writeCurrentAccountFriendDstr(&w, r.Remark); err != nil {
			return nil, err
		}
		w.writeUint32(r.NeutralTimestamp)
		if r.NeutralFlag {
			w.writeByte(1)
		} else {
			w.writeByte(0)
		}
	}
	return w.bytes(), nil
}

// 02008910 resets both pending lists, then reads each kind, name, message,
// expiration timestamp and dispatches kind0 to received requests (manager+28),
// kind1 to sent requests (+14), verified in the current v95 directory UI.
func buildCurrentAccountFriendPendingBody(rows []currentAccountFriendPendingRow) ([]byte, error) {
	if len(rows) > 255 {
		return nil, errCurrentSocialBodyInvalid
	}
	var w packetWriter
	w.writeByte(byte(len(rows)))
	for _, r := range rows {
		if r.Kind > 1 || r.AdventureName == "" || !nativeAccountFriendTextValid(r.AdventureName, 17) || !nativeAccountFriendTextValid(r.Message, 41) || r.ExpiresUnix == 0 {
			return nil, errCurrentSocialBodyInvalid
		}
		w.writeByte(r.Kind)
		if err := writeCurrentAccountFriendDstr(&w, r.AdventureName); err != nil {
			return nil, err
		}
		if err := writeCurrentAccountFriendDstr(&w, r.Message); err != nil {
			return nil, err
		}
		w.writeUint32(r.ExpiresUnix)
	}
	return w.bytes(), nil
}
func buildCurrentAccountFriendDeleteBody(name string) ([]byte, error) {
	if name == "" || !nativeAccountFriendTextValid(name, 17) {
		return nil, errCurrentSocialBodyInvalid
	}
	var w packetWriter
	if err := writeCurrentAccountFriendDstr(&w, name); err != nil {
		return nil, err
	}
	return w.bytes(), nil
}
