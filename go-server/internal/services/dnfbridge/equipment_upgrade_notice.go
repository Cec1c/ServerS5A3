package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/platform/eventloop"
)

// 当前频道按完整频道号隔离，使用各角色当前有效的主连接，辅助/旧连接不重复播报。
func (s *Service) currentEquipmentNoticeRecipients(source *gameSession) []boundGameSessionCharacter {
	if s == nil || source == nil || source.conn == nil || source.selectedCharacterID == 0 ||
		source.characterGeneration == 0 || source.loginRevoked.Load() || currentTownPresenceChannelID(source) <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gameSessions[source.selectedCharacterID] != source {
		return nil
	}
	channel := currentTownPresenceChannelID(source)
	var out []boundGameSessionCharacter
	for id, session := range s.gameSessions {
		if session == nil || session == source || session.conn == nil || session.selectedCharacterID != id ||
			session.characterGeneration == 0 || session.loginRevoked.Load() || currentTownPresenceChannelID(session) != channel {
			continue
		}
		out = append(out, boundGameSessionCharacter{session: session, character: id, generation: session.characterGeneration})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].character < out[j].character })
	return out
}

// 原生86依赖接收端角色对象，同屏使用完整原生播报；同频道其他场景采用
// 已有NOTI12系统消息携带真实角色名和装备名，不制造城镇角色或要求EXE补丁。
// 两条展示链互斥，操作者的原生提示已经由资产应答链发送。
func (s *Service) broadcastCurrentEquipmentUpgradeNotice(source *gameSession, notice *alignedcmd.EquipmentUpgradeNotice) {
	if notice == nil || len(notice.Body) < 10 || notice.ItemID <= 0 || source == nil {
		return
	}
	recipients := s.currentEquipmentNoticeRecipients(source)
	if len(recipients) == 0 {
		return
	}
	channel := currentTownPresenceChannelID(source)
	sourceID := source.selectedCharacterID
	sourceBinding := boundGameSessionCharacter{session: source, character: sourceID, generation: source.characterGeneration}
	textBody := s.currentEquipmentNoticeText(source, notice)
	for _, target := range recipients {
		target := target
		err := s.postCurrentEquipmentNotice(target, func() error {
			if !s.boundGameSessionCharacterCurrent(target) || currentTownPresenceChannelID(target.session) != channel {
				return nil
			}
			native := false
			if s.onlinePlayers != nil && s.boundGameSessionCharacterCurrent(sourceBinding) {
				info, found := s.onlinePlayers.PlayerForCharacter(sourceID)
				peer, peerFound := s.onlinePlayers.PlayerForCharacter(target.character)
				native = found && info.Session == source && peerFound && peer.Session == target.session && currentTownPresenceChannelID(source) == channel &&
					s.onlinePlayers.PeerInSameArea(sourceID, target.character)
			}
			if native {
				return s.sendGameUpperRawClassCodec(target.session, 86, notice.Body, 0, true)
			}
			if len(textBody) == 0 {
				return nil
			}
			return s.sendGameUpperRawClassCodec(target.session, 12, textBody, 0, true)
		})
		if err != nil {
			s.logGameEvent(source, "频道装备播报投递失败", "target_char_id", target.character, "channel_id", channel, "error", err)
		}
	}
}

// 装备播报是非关键通知。满队列时立即省略，不能在操作者事件中等待其他会话。
// 其余关键事件仍使用原来的阻塞投递；无事件循环的独立调用沿用同步契约。
func (s *Service) postCurrentEquipmentNotice(target boundGameSessionCharacter, run func() error) error {
	session := target.session
	if session == nil || run == nil {
		return errInvalidGameSessionEvent
	}
	if session.loginRevoked.Load() {
		return errLoginReplaced
	}
	if session.events == nil || session.events.loop == nil {
		return run()
	}
	return session.events.loop.Submit(eventloop.Event{
		Type: "频道装备播报",
		Data: gameSessionEventTask{
			source: "频道装备播报", characterID: target.character,
			characterGeneration: target.generation, async: true, run: run,
		},
		Priority: eventloop.PriorityMedium,
		Context:  session.ctx,
	})
}

func (s *Service) currentEquipmentNoticeText(source *gameSession, notice *alignedcmd.EquipmentUpgradeNotice) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(source.selectedCharacterID)))
	if err != nil || !found || character.AccountID != s.accountIDForSession(source) || strings.TrimSpace(character.Name) == "" {
		return nil
	}
	itemName := strconv.FormatInt(notice.ItemID, 10)
	if catalog, err := s.currentPVFItemCatalog(); err == nil && catalog != nil {
		if item, err := catalog.ResolveItem(uint32(notice.ItemID)); err == nil && strings.TrimSpace(item.Name) != "" {
			itemName = item.Name
		}
	}
	operation := "强化"
	if notice.Kind == "amplify" {
		operation = "增幅"
	} else if notice.Kind == "forging" {
		operation = "锻造"
	}
	outcome, level := "成功", int(notice.Level)
	if !notice.Succeeded {
		outcome, level = "失败", level+1
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(fmt.Sprintf("玩家[%s]的[%s]%s +%d %s。", character.Name, itemName, operation, level, outcome)))
	if err != nil || len(encoded) == 0 || len(encoded) > 255 {
		return nil
	}
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0)
	w.writeByte(0)
	w.writeRawDstr(encoded)
	w.writeRawDstr(nil)
	return w.bytes()
}
