# R134 N3 超时空赛丽亚私人房间隔离

2026-10-21；独立目录 `D:/codex-build-r134/skill-source`，HEAD `7ca5fe751560118883416833edecad09182da40e`（主代理预备累计副本）。本轮没有提交、合并、部署或启动客户端，没有修改正式EXE/PVF、玩家数据、源仓库或source-continuation。

## 交付结论

已用指定真实PVF和完整Upper class1/op36入口复现并修复两个问题：

1. 超时空私人房间98/0的逻辑地图名不含seria，现有识别器只额外收录安徒恩和卢克，导致陌生角色与同队角色都以Instance0登记、相互可见，op24名册和op35/普通区域聊天跨房传播。
2. 仅修地图识别仍不足：同队角色虽然已经分桶，`sendTownRemotePartyActors`以及`rebuildRuntimePartyTownCoPresenceAfterReturn`会把不同私房的队友补建为actor，再以无instance字段的op23声明到相同town/area。新增窄守卫阻止同一物理赛丽亚区域内另一角色的投影，保留队伍名册、不同物理区域的原有队友投影及公共区域行为。

这些是可独立复现的代码缺陷与修复，不等同于已经确认用户本次操作的唯一触发链。主代理提供线上仍R133 image d20a4cf…、healthy且0重启；90分钟/最多60000行过滤仅27行无定位事件，原始包关闭。角色、频道、时刻、是否同队和实际进入方式仍待用户/实机。

## 真实PVF证明（不是按Gate名字猜测）

输入 `D:/codex-build-r129/container-baseline/Script.pvf`，SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。

- town98/area0逻辑路径：`GContents/180621_Fiendwar/Town/Gate_Fiend.map`；kind gate，level90，任务8641。
- 原生资源 `map/gcontents/180621_fiendwar/town/(r)gate_fiend.map`，`[import script] Common/(R)Gate_Seria.map`；解码文本SHA256 `0d754461bccb4b28c816ee4279db1f44ae8148997e97f78d9a468a918c1a9c2d`。
- 原生资源 `map/gcontents/180621_fiendwar/town/(f)gate_fiend.map`，`[import script] Common/(F)Gate_Seria.map`；解码文本SHA256 `6ff07b94b62d3f2810279f5507c6f7081eccd55c1df096ff6020dadf5265f5d7`。
- 两个资源的town movable area均指向98/1，名称“最后防线”。98/1是`Fiendwar_Main_Town.map`，98/2是`Fiendwar_Town00.map`，均不标为角色私房；等待区原团队归属门槛保持。
- 原始逻辑路径没有无前缀的同名资源，必须读取(R)/(F)条目。早期`provenance-red*`里的缺档/空文本SHA以及`handler-red.txt`的`handled=false`是取证/夹具错误，不作为模板或产品红测试。有效文本、哈希和入口红证据见下列回执。

## 精确增量与调用路径

| 文件（相对本副本） | 增量 |
| --- | --- |
| `go-server/internal/services/dnfbridge/town_prev_village.go:26` | `townMapPathLooksLikeSeria`增加一个精确逻辑地图项；复用原有大小写、斜杠与map/前缀处理。没有按channel type放宽条件，也没有扩大成整个fiendwar目录。 |
| `go-server/internal/services/dnfbridge/online_player_manager.go:898` | `sendTownRemotePartyActors`调用私房投影守卫。 |
| `go-server/internal/services/dnfbridge/online_player_manager.go:945` | 新`townPrivateRoomPeerProjectionBlocked`：接收者为当前登记会话、双方不同角色但同town/area，且实际town目录判定为赛丽亚房间时阻止补建；不把“不同桶”笼统解释为禁止所有远程队友。即使旧记录残留Instance0也不投影另一私房角色。 |
| `go-server/internal/services/dnfbridge/party_town_return.go:59` | 全队回城重建同样调用守卫；继续发自身队伍/实时名册。 |
| `go-server/internal/services/dnfbridge/fiendwar_seria_privacy_real_pvf_test.go:22` | 独立真实PVF测试：陌生/同队完整op36，op24名册与actor补发、op35、聊天、公共区域反例、登录发布/回城重绑、全队回城补发及等级/任务门槛。未配置真实PVF时t.Skip。 |
| `Docs/R134-N3-fiendwar-seria-privacy.md`、`changlog.md` | 本报告与独立N3变更记录。 |

原链路：`raid_fiendwar_location.go/currentFiendWarLoginLocation`选98/0；`town_move.go/handleTownSetUserAreaWithQuestNotice`通过真实等级/任务校验后，经`guild_hall.go/currentTownAreaInstance`→`town_move.go/townAreaInstanceForMapPath`→地图识别器取实例。op24用`GetAreaScopePlayers`枚举，随后`EnterArea`登记。`OnlinePlayerManager.UpdatePosition`和`online_area_chat.go/currentAreaChatPeerSessions`使用同一作用域。因此错误是私房识别遗漏导致公共桶合并，不是type42直接跳过已有Instance键。

op36的16字节请求没有由玩家指定房间owner的字段；owner由服务端selectedCharacterID取得。不同角色请求同一98/0应各进自己的房间。补发路径例外以前绕过这份可见性，NOTI23只携带actor/town/area/位置，无法表达两个同地图私人实例。

## 红绿与验证

证据目录：`D:/codex-build-r134/skill-source/runtime/r134-n3-evidence/`。

单项补丁：该目录`r134-n3-seria-privacy.patch`；文件SHA与补丁SHA见`delivery-manifest.json`。补丁只含3个业务文件、新测试、新报告和N3 changlog节，不夹带P67/P133；已在runtime导出的HEAD文件副本上通过`git apply --check`，没有应用到公共源仓。

| 回执 | 结果 |
| --- | --- |
| `handler-red-final.txt` | 未修业务的有效Upper入口红测试：陌生/同队均Instance0；op24含29和30；移动/聊天跨房。真实(R)/(F)模板原文与SHA一并留存。 |
| `map-only-party-red.txt` | 只修精确地图识别：map_identity、strangers通过；same_party仍收到mode0/mode1。证明不能只修地图名单。 |
| `party-return-red.txt` | 加入登录/回城和门槛对照后，登录分桶及门槛通过；同队普通补发、全队回城补发仍失败，后者两端均收到外国私房actor op2/op23。 |
| `green-final.jsonl` | 最终三业务文件补丁：24个顶层测试及51个子测试PASS，0FAIL；1个可选MySQL公会测试SKIP（disposable MySQL DSN not configured）；包耗时3.073s。 |
| `verification-summary.json` | 机器可读计数与指定Go资源限制。 |
| `default-skip.jsonl` | 未配置真实PVF环境变量时，新N3测试按约定SKIP，包PASS（0.056s），不因缺档Fatal。 |

新N3测试自身1个顶层、8个子测试全部通过。反例包括同频道98/1仍共享、移动/聊天继续发送；同队两名成员和PartyID不变；89级/缺8641不得进入；98/2没有改成单人私房。既有目标回归还覆盖普通城镇跨区队友名册、跨频道投影、公会成员/聊天/回退、安徒恩/卢克私房、团队等待区、公共回城重建及UDP认证定向腿转发/离队腿回收。

运行环境：Go `C:/Users/16ww/go-tc/go/bin/go.exe`；`GOMAXPROCS=3`；`GOFLAGS=-mod=readonly -p=2`；`GOCACHE=D:/codex-build-r104/main/cache`；`DNFBRIDGE_REAL_PVF_SMOKE`为上述固定文件；只运行目标测试，未自行跑全套或构建部署。测试角色/点位仅内存夹具，实际PVF的等级90/任务8641门槛没有关闭。

## 中继与待验边界

- 已验证城镇actor、op24名册、op35位置和普通区域聊天隔离。队友补发的两条入口同时收口。
- 队伍UDP按party/owner/peer/generation及认证地址建立，与town区域广播是不同机制。本补丁不关闭或重建UDP、不重复发op11；既有定向转发和离队回收测试通过。不能把本次结果表述成“已证明不透明UDP负载在私房的全部客户端行为”；这仍需客户端现场验证。
- 保留公共区域、跨物理区域队伍信息、公会规则与副本成员关系；没有修改N1团队accept/rebuild逻辑或N2黑白图入口。主线整合时使用下面的单项补丁，勿整目录覆盖。
- 正式全量/public/部署由主代理负责；未启动客户端，未宣称正式交付已实机验收。
- 现场复核：同频道陌生两角色、已同队两角色分别进98/0、再出98/1；房间互不可见，公共区域互可见，队伍框保持；登录/换角/回城后重复。原始包关闭时可留意既有`game-town-private-area-entered`、`game-town-set-user-area-committed`的map_path/area_instance及`game-dungeon-return-copresence-rebound`。这些事件是否实际输出仍依赖部署日志策略，本轮未修改线上配置或新增诊断器。

P133两文件SHA核对与交付时一致；P67旧报告/测试保留，新checkpoint为`Docs/R134-P67-skill154-checkpoint.md`，不纳入N3代码补丁。
