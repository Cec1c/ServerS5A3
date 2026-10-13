package dnfbridge

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

const townQuestGenericText = "前往所选区域，需要先完成对应的前置任务。"

// 当前本机 EXE 的 NOTI12/0x2035370 读取 type:u8、sender:u16、channel:u8、
// message:DSTR、extra:DSTR。type=0 经 0x2a04000 -> 0x2a04106 显示原文，
// 不使用发送者名称；0xffff 沿用原生系统文字结构的无发送者标识。
// op36 的 error=0 消费 town/area 后走 0x1fd0f5f 的失败收尾，不生成地区名。
// 因此保持失败确认，同时通过已启用的原生文字接口展示任务，不修改 EXE。
func (s *Service) sendTownQuestTextFailure(session *gameSession, request currentTownSetUserAreaRequest, questID int64) error {
	message := townQuestGenericText
	// 只用启动时已加载的目录，提示路径不另开 PVF 或触发昂贵的目录重建。
	s.questCatalogMu.Lock()
	catalog := s.questCatalog
	s.questCatalogMu.Unlock()
	if definition, ok := catalog.Find(questID); ok {
		if name := strings.TrimSpace(definition.Name); name != "" && !strings.ContainsAny(name, "\x00\r\n") {
			message = fmt.Sprintf("前往所选区域，需要完成[%s]任务。", name)
		}
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(message))
	// 客户端文字缓冲区为 256 个宽字符；按字节收紧到 240，避免截断多字节字符。
	if err != nil || len(encoded) > 240 {
		message = townQuestGenericText
		encoded, err = simplifiedchinese.GBK.NewEncoder().Bytes([]byte(message))
		if err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUserArea),
		[]byte{0, 0, request.TownID, request.AreaID}, dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	var writer packetWriter
	writer.writeByte(0)
	writer.writeUint16(0xffff)
	writer.writeByte(0)
	writer.writeRawDstr(encoded)
	writer.writeRawDstr(nil)
	if err := s.sendGameUpperRawClass(session, currentAreaChatNotifyOpcode, writer.bytes(), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-town-set-user-area-quest-text-notice",
		"char_id", session.selectedCharacterID,
		"town_id", request.TownID, "area_id", request.AreaID,
		"quest_id", questID, "message", message)
	return nil
}
