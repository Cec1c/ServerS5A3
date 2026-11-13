package dnfbridge

import (
	"errors"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Keep player text separate from repository/transport errors and account data.
// The member IDs belong to the admission snapshot, before any debit commits.
type raidStartRejection struct {
	message string
	members []uint16
	cause   error
}

func (e *raidStartRejection) Error() string { return e.message }
func (e *raidStartRejection) Unwrap() error { return e.cause }

func (s *Service) sendRaidStartRejection(session *gameSession, typ uint16, reason error) error {
	if err := s.sendRaidCommandFailure(session, typ); err != nil {
		return err
	}
	// Preserve other raid consumers and the existing negative command ACK.
	if session == nil || s.options.currentClientVersion != "official-24.9" || session.channel.Type != dnfenum.RaidChannelType {
		return nil
	}
	message := "请确认全员在线、已进入本团等候区，并满足人数、入场材料、前置任务及次数要求"
	var detail *raidStartRejection
	if errors.As(reason, &detail) {
		message = detail.message
		if state, found := s.runtimeRaidForMember(session.selectedCharacterID); found {
			var names []string
			for _, id := range detail.members {
				for _, member := range state.Members {
					if member.CharID == id {
						name := strings.TrimSpace(member.Name)
						if name == "" {
							name = fmt.Sprintf("角色%d", id)
						}
						names = append(names, name)
						break
					}
				}
			}
			if len(names) > 0 {
				message += "（" + strings.Join(names, "、") + "）"
			}
		}
	}
	// Chat text is a DSTR, not a roster name. The name encoder truncates at
	// the short character-name limit and would drop the reason/member list.
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("无法开始攻坚：" + message + "。"))
	if err != nil {
		return err
	}
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0xffff)
	w.writeByte(0)
	w.writeRawDstr(encoded)
	w.writeRawDstr(nil)
	return s.sendGameUpperRawClass(session, currentAreaChatNotifyOpcode, w.bytes(), 0)
}
