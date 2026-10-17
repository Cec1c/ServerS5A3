后续默认配置文本已补齐，当前范围和证据见 [CREATOR_DEFAULT_TEXT_20261017.md](CREATOR_DEFAULT_TEXT_20261017.md)。下文保留首次saved-type修复记录。

# 已保存缔造者键位的职业体系回放

原问题文档当前第90段缔造者WASD和第112段设置恢复默认仍有完整验收范围。本候选只修复已保存完整配置回放时的职业体系选择：角色job10应使用CreatorType=1，原Go代码却直接取账号hotkey_key_type（无生产写入，缺省0），可把缔造者WASD配置施加到NormalType。

## 依据

- 当前预览v119 EXE SHA256 009730b047c31675ba7db8f2b75a74b896ee9c47e4cd8c02f1d49c52aed98fc3：01457373后的调用经02254b90将输入10映射为1，随后02254b80写键位对象+1b4。01456210接收链临时切换该类型后应用配置再恢复前值。字段0对应解码字符串NormalType。这里只记录已读分支；并未穷尽Actor虚表getter来源或全部C2S类型传递。
- 本机参考H:/YKCN90工作区/调查资料/ServerS4A21-build-reference-20261011/Server/DfoServer/Game/Settings/CharacterKeyboardDefaults.cs明确CreatorMageJob=10、ResolveKeyType(job,account)=job10时1、其他账号值；Init/HotkeyConfigBodyBuilder及SqliteSelectCharacterDataSource调用该规则。本候选只借用该职业规则，不移植其150字节主设置、二进制默认槽或旧通知编号。
- 当前配套PVF a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290中clientonly/hotkeysystem_ver3.co确实存在，CreatorType移动I01/I02/I03/I04为87/65/83/68（W/A/S/D），NormalType为38/37/40/39；原定义和文件摘要存本轮hotkey-pvf.json。
- 454仍为当前u8类型+u32长度+原配置串，不另造协议。当前原生3752050读取长度/正文实际执行成功，3752120调用它后进入多字节转换。旧报告“只有1字节读取、其余正文不消费”不能跨间接调用成立。完整Unicode转换/最终解析器与GUI尚未证明。

## 行为

official-24.9已保存角色完整配置路径，在现有配置容量检查后，只读加载同一选定角色，核对账号及选择未切换；job10强制keyType1，其余职业沿原账号类型。角色缺失、读取失败、归属不符或切换时跳过本次回放。保存的配置正文逐字节保持，账号/角色设置均不回写。

不构造无存档配置，不改默认种子，不改C2S保存，不改其他profile，不改客户端/PVF。没有配置的缔造者首次入场及普通职业设置恢复默认仍需继续定位；本候选不是这两段所有症状已修的结论。

## 验证与交付

红用例在旧代码中证实job10缺省/0/2仍发0/0/2；角色缺失与其他账号也能发配置。回归通过实际op198保存→账号热键回放→454整帧比对，覆盖三个职业×四种账号类型、设置不被回写和两项归属边界，并保留原official设置/热键测试。

全部规定检查、累计运行版本构建与部署、MR、CI和用户实机验收另见本轮报告及回执；本说明不声称未完成的检查已通过。MR应保持未合并，等待用户验收。
