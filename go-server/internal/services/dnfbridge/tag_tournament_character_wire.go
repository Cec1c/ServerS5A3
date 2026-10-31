package dnfbridge

import (
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"strings"
)

const currentTagTournamentCharacterNotification uint16 = 1389

// 现用14602A0先读已有主actor数及各自u16键，再读该主actor的备用队员数。
// 备用队员为u8已发列表序号、DSTR姓名、24F9150(full=1)正文；没有第二份
// 普通mode1外壳或备用对象键。当前只支持本账号一主一备的本机队伍。
// 本编码器不授予入场资格，也不把零值转换为尚未核实的运行状态。
func buildCurrentTagTournamentCharacterWire(ownerKey uint16, selector byte, name string, actor currentTagTournamentActorWire) ([]byte, error) {
	// 列表辅助函数会截断过长姓名，这里必须完整编码并拒绝越界，不能
	// 把备用队员悄悄改成另一个短名字。
	encodedName, nameErr := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
	if ownerKey == 0 || ownerKey == 0xffff || selector == 0xff || name == "" || strings.ContainsRune(name, 0) || nameErr != nil || len(encodedName) == 0 || len(encodedName) > 31 {
		return nil, fmt.Errorf("帝国竞技场主对象、备用队员序号或姓名不可表达")
	}
	body, err := buildCurrentTagTournamentActorWire(actor)
	if err != nil {
		return nil, err
	}
	var w packetWriter
	w.writeByte(1)
	w.writeUint16(ownerKey)
	w.writeByte(1)
	w.writeByte(selector)
	w.writeRawDstr(encodedName)
	w.writeBytes(body)
	if len(w.bytes()) > 65535-16 {
		return nil, fmt.Errorf("帝国竞技场双角色正文超过现用帧上限")
	}
	return w.bytes(), nil
}
