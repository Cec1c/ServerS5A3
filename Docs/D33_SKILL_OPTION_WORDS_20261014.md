# D33 技能选项（锁定 / CD 提示）字段取证与保存回放可观测性

2026-10-14，装备席。覆盖 DOC-060、DOC-114 与 DOC-053 里的三条技能设置症状（技能锁定重选重置、攻击类型物理/魔法重选变普通、CD 提示重选需再激活）。**不关闭任何 DOC 条目**：源码与受控回归不等于实机验收。

## 1. 已证字段（含当前客户端复核）

| 设置 | blob 位置 | 写入 / 恢复 |
| --- | --- | --- |
| 技能锁定 | word 128..191 = 树0、192..255 = 树1，每树 64 项；值 = 客户端技能槽号，空项 0xFFFF | 写 0x39A6BA0 / 读 0x39A6C00（index = (bank+2)*64+entry，越界返回 0xFFFFFFFF）；导出 0x2255AA0、恢复 0x2255880 |
| CD 提示列表 | word 2..21 = 树0、22..41 = 树1，每树 20 项，空项 0xFFFF | 导出 0x22555F0、恢复 0x2254FC0（先清全部 512 槽再按两树恢复） |
| 攻击类型（物理/魔法） | 未证 | 无 |
| 其它已知、非本席范围 | word43 = ExportScenarioDifficulty；word126/127 = 时装可见 / 称号不透明度 | 只读：不改写、不清理 |

取证来源是 43 批对 DNF.exe SHA256 bb42898c… 的只读反汇编（报告与 asm/bin 切片见调查资料 43 批目录）。本轮用 scripts/verify_client_skill_option_words.py 在当前客户端 DNF.exe SHA256 8bf1d39d…（2026-10-14 07:36）上离线复核，all_ok=True：

- 11 个相关函数仍在同一 VA；保留的整函数字节切片逐字节相同（CD 恢复 2176B、390 读取端 1616B、锁表导出 464B、CD 导出 672B、447 写出端 672B 等）。
- 语义检查同样通过：raw/bank 边界 0x7F/0x80、索引算法 (bank+2)*64+entry、blob 基址 +0x1CC、帧长恰好 0x200、CD 列表常量 2..41 与每树 20 项（步长 20）。

因此 43 批的字段结论对**当前**客户端仍然成立，不再把旧哈希当成现状证据。

真实捕获（45 批原生取证）：角色 q7a（character:31）点「锁定」后客户端实发 op447（body 516 = u32 512 + 512B）被逐字节落库，payload 只有四个非 0xFFFF word：0、1（锁表导出写）、43（难度=0）、128=5（树0 锁表第一项）。该 512B 是本席回归的固定 fixture（sha256 4fe7d38648f9d5a19c372a73c684c7d66bd473dc9db6245da8bc0e9a3d7e297b）。

## 2. 源码现状（基线 5fc6807 已含）

op447 逐字节落库（official-24.9 只接受 512，与客户端读取端同条件；进场窗口守卫挡住入场模板）；每次真实选角回放一次 class0/390 且先于 19；账号档 0x00AD/0x01C7 仍每连接一次（P55）。45 批实测的「同连接重进同一角色只有 19、没有 390」是旧的一次性连接闸门，已由 31e2b51 拆出选角闸门修复并合入基线。

## 3. 本轮改动（不改保存 / 回放字节）

- 新增 go-server/internal/services/dnfbridge/character_option_words.go：按上表索引只读解码（锁表 / CD 列表、word 差异、清空点名、payload sha256）。不做保存门禁、不参与写入决策；其余 word 不重定义、不清除。
- game_option_settings.go：op447 保存与 class0/390 回放事件接上字段证据 —— previous_saved、payload_sha256、option_words_decoded、changed_word_count、changed_word_indexes、lock_tree0/1_slots、cd_tree0/1_slots、lock_slot_count、cd_slot_count、option_word_encoding；并新增具名事件 game-character-option-skill-lists-cleared（上一份存档的锁定 / CD 列表在新存档里消失时）。
- 新增 character_option_words_test.go（见第 4 节）；新增 scripts/verify_client_skill_option_words.py（离线复核脚本，路径全部参数化、只读）。

日志负担：新字段只挂在 op447 保存与 390 回放两条事件上。线上只读统计（16MB 日志窗口）里这两条分别只有 13 与 20 次；每条最多多约 12 个字段，槽号列表最多打印 8 项后以 +N 收尾，没有逐包开销。

## 4. 测试

- 旧失败复现（临时切片，跑完按 SHA256 校验还原，ab5d9043… 前后一致）：把角色选项回放改回旧的一次性连接闸门后，返回选角、同角色重进、A→B/B→A、同一 entry 重试只发一次 390 四组既有回归全部 RED（390=-1，19 仍发），本席新增的端到端用例同样 RED（390/19 order: option=-1 skills=30）。
- GREEN：-run TestCharacterOption 全部通过（含 6 个新用例与既有投影 / 守卫 / 回放回归）；整个 internal/services/dnfbridge 包通过（20.3s）；gofmt、go vet、go list 干净。未跑全套重测试（主代理负责集成）。
- 命令口径：固定 go C:/Users/16ww/go-tc/go/bin/go.exe，GOTOOLCHAIN=local、GOPROXY=off、GOSUMDB=off、GOENV=off、GOWORK=off、-mod=readonly -buildvcs=false -count=1。

主代理审核补充：保存失败时不能记作已经清空列表。新增故障仓储回归先重现误报，再确认保护后旧存档保持且不再发 cleared 事件；保存和回放字节不变。源码 `556a2fb` 完整验证通过：gofmt/list/vet、Windows/Linux 构建；核心 9323 通过节点、859 跳过、零失败；固定真实 PVF 全部 146 个指定测试根通过，657 通过节点、2 个无选中测试的包跳过、零失败。BAT 预检因源码工作树缺 `channel_info.etc` 返回 1。主代理独立复跑当前 EXE 的 11 项离线布局检查均通过。

## 5. 现场 A/B（43 批留下的三条取证目标）

1. 让操作者做一次动作（点一次锁定 / 切一次 CD 提示），两次之间退出到选角再进同一角色。日志里 game-character-option-saved 的 changed_word_indexes 就是这次动作改了哪些 word（锁定落在 128..255 段、CD 提示落在 2..41 段），lock_tree0_slots / cd_tree0_slots 给出槽号，lock_slot_count / cd_slot_count 给出项数。
2. 若出现 game-character-option-skill-lists-cleared，说明新存档把上一份的锁定 / CD 列表刷没了 —— 这就是「被重置」的服务端证据（只记录、不拦保存）。
3. 同一角色冷登录或重进时，390 事件的 payload_sha256 必须等于保存时的 payload_sha256；同时记录 client version 与 DNFBRIDGE_CHARACTER_OPTION_NOTI / DNFBRIDGE_CHARACTER_OPTION_PROJECTION / DNFBRIDGE_GAME_OPTION_ENTRY_GUARD 的取值与 390 / 19 先后。

## 6. 仍未闭合

- 三条症状的原生 GUI 验收都还没在改后源码上做过；本轮没有任何实机结论。
- 攻击类型（物理/魔法）没有取得字段或包证据：不实现、也不用其它字段代替。
- 同一连接再选角时客户端是否重推 op447：45 批实测在重进同一角色时没有 447（只有服务端 390/19），因此不改保存门禁；若现场出现「被默认模板刷掉」，第 5 节的 cleared 事件会先抓出来。
- 冷连接（全新客户端进程）的 390 / 19 顺序与内容：源码回归覆盖，实机未验。
- DOC-053 的「时装右键掉线」「重选后武器栏空白」已由用户确认已修（仅未涂色），本席不再排查；用户未说明修复位于客户端还是服务端。

## 7. 交付边界

只在本席独立工作树 E:/90/bugs-round2-20261014/equipment（基线 5fc6807，分支 work/bugs-round2-equipment）改动；未部署、未推送、未改其它工作树 / 用户 DOCX / 原台账 / 生产资产，未改装备或任务领域。改动路径：

- go-server/internal/services/dnfbridge/character_option_words.go
- go-server/internal/services/dnfbridge/game_option_settings.go
- go-server/internal/services/dnfbridge/character_option_words_test.go
- scripts/verify_client_skill_option_words.py
- Docs/D33_SKILL_OPTION_WORDS_20261014.md（本文件）
- changlog.md（记录）
