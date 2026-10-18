## 2026-10-18 — GitGud 上游唯一权威与单向源码镜像

- 独立功能、修复、测试、文档和 CI 分支统一通过上游 MR 集成，冲突由同步 Agent 处理并验证；GitHub 只接收上游确切提交，不再建立同步合并提交或向 GitGud 反写。
- 移除旧 GitHub 上游 PR/自动合并流程；Repository sync 使用仅属于 GitHub 镜像的 Deploy Key，保留引用移动检查、非强推和最终 SHA 读回。CI 文件在上游 MR 审查后同步，不执行抓取代码。
- 补充单向快进、领先/分叉拒绝、并发引用变化和 CI 同步回归；更新 Agent 入口及迁移流程。源码镜像不构建、发布资源或部署游戏。

## 2026-10-18 — Client Release 静态 Python 源码附件兼容

- 客户端 Release ZIP 可附根级 scripts/ 内的 Python 源码，仅读取并校验归档，不解压或执行；其余路径与文件类型规则保持。
- 补充归档正反例、路径/重复/链接/大小/数量/EXE/CRC及下载摘要回归，修复带源码证据的新客户端包被镜像拒绝的问题。镜像本身不改变玩家通道或部署服务端。

## 2026-10-18 — 主线场景及已有修复复核

- 真实PVF核对八段主线场景，新增伦道夫剧情状态回包、进图/退出/重连难度保持，以及居民任务接取/父子列表与重复请求验证。
- 觉醒疑惑之村两种电梯图按真实机关与死亡handler完成后，连续八房到Boss；记录已有修复及原生任务交付待验范围，没有新增生产行为。
- 客户端提前结局由独立自动回顾候选处理；菜单画面未复现，完整对话/觉醒交付继续由用户实测。

## 2026-10-18 — QST-006 材料子任务进入权威结算

- 无奖励但有交付材料的子任务不再仅写任务状态归档；直接子提交和父末子提交沿既有结算事务检查、扣除材料并保存幂等回执。
- seek n meet npc读取全部材料对，保留末尾NPC语义。补真实梅娅女王和危险研究材料链、缺料、回滚、重复/重连回归；纯对话快捷路径保持。
- 公开方案见[QST-006](Docs/seeker/QST-006-queen-linked-objectives.md)。自动验证与原生任务验收分开记录。

## 2026-10-18 — QST-002 中途清房推进与父子任务状态一致

- 当前迷宫绑定的无奖励clear-map子项，在匹配的中途房间权威清空后同事务归档并重算父进度；稳定回执覆盖死亡重放、通知失败、队员独立资格及离房重试，不触发最终副本奖励。
- quest-clear 父任务 op33 不再因没有新归档的城镇子项而回退普通减一；领域层按已提交子项重算剩余数，未完成 clear-map 子项存在时父进度保持 1，历史父 0／子 1 会恢复到 1，且不补造子项完成。
- 保留合法对话子项归档；全部子项达成active/0时父任务可交，物品仍由op34统一事务结算。相同父进度的重复回调保持静默，子项随后变化继续推进。保留普通任务原op33路径。任务目录不可用仅在持久化记录存在父任务类型或 `main_quest_id` 关系证据时失败关闭，普通记录继续原路径。
- 真实 PVF 探针中 `parent-callback-must-not-finish-unresolved-child` 与 `target-credit-finishes-parent` 均通过，领域、bridge、control完整测试及list/vet/构建通过；末子奖励兼容、回调反馈循环与保存后回滚覆盖。真实PVF六怪死亡handler、父回调与结算三组通过；中途清房边界、队员资格及通知失败重试覆盖。

## 2026-10-18 — 父任务收尾结算末子任务物品

- 父任务零进度末子任务收尾时，复用现有 PVF 交任务材料扣除、金币和物品分配逻辑，在同一角色结算事务中提交父子任务状态、库存、返回结果及父任务持久回执；重复父任务请求只返回原回执。
- 子任务资产变化按任务号和独立 completion key 归属，普通无奖励末子任务仍可直接完成；已独立完成的子任务不重复发奖，物品不足、分配失败和保存失败继续由既有事务整体回滚。复杂职业或显式选奖子任务不在隐式父收尾中猜测选择。
- 专项覆盖真实 PVF Definition驱动的三扣四奖、净增一个与重放、同格奖励完整回包、跨父拒绝、物品不足、分配失败及最后提交失败回滚；完整领域／bridge／control、list／vet和Windows构建通过。数据库条件用例未配置独立库而跳过，正常流程游戏内验收待完成；无需EXE或PVF变化。

## 2026-10-18 — 合并上游更新并保留独立 GM 与同步边界

- 合入上游已合并的服务端更新，保留双方变更记录、客户端发布约束、私有管理 API 与注册福利事务；管理会员目录继续由游戏服务使用，不重新引入 GM 工具实现。
- 控制器接受旧配置字段用于读取，但仍要求显式迁移独立 GM；拒绝时不改写配置，不把 GM 环境传入游戏进程。合并相关回归覆盖旧配置读取和迁移后的投影。
- 审查上游 CI 只增加服务端边界检查，保留本方仅同步模式、手动 Verify 和独立 EXE Release 同步流程；源码合并不部署游戏或更改玩家资源。
- 验证合并结果的领域／bridge／control／doctor、list／vet、Windows/Linux 构建、同步及 Docker 回归与双方服务端边界检查；真实数据库、PVF 和原生玩法验收不属于本次仓库合并。

## 2026-10-17 — 魂大会对手缺失排查与真实资源回归

- 追加123/124名称、DGN/MAP、全候选池与出生点、386四轮路径和387大于u16地图编号的真实PVF回归；不按交叉文件名猜测副本身份。全资源枚举覆盖五种大会，124两只特殊怪物与动作文件存在。
- 当前服务器大会开关已为vendor；全日志及原始op16仅见普通120/121，尚无魂大会现场入场。无对手根因仍开放，本次仅测试和调查文档，不修改游戏行为、EXE/PVF、运行服或玩家数据，不以离线通过替代原生验收。
- 后续用户明确确认“魂黄龙大会正常”，更新为本次实测正常并撤销待复现请求；MR仅作资源映射与对手投影回归补充，不宣称部署了故障修复，不扩大其它大会验收范围。

## 2026-10-17 — 已验收魔封初始封装同步主线

- 用户追加验收!47后整合当前主线；冲突的道具定义同时保留魔封Rarity／RandomOption和礼盒历史期限字段，沿用各自解析／封装／续期逻辑。仅整合已验收范围，未部署服务器或改玩家数据。最终检查与合并回执独立保存。

## 2026-10-17 — 已验收悲叹结算同步主线

- 整合已验收主线与悲叹之塔候选，冲突处同时保留悲叹层数／奖励字段及通关经验药基础、加成与持久回执；不改变验收服和玩家数据。合并后的完整检查、真实PVF专项与BAT回执独立保存。
## 2026-10-17 — 公会认证区服0原生选择器

- 截图下拉空、现场156/反复739而无741；在服公告区服0，v123原生枚举1..249且邀请范围排除0。只将两处扩为0..249，继续缓存可用性/资格门，不迁移公告或改双人认证政策。
- 固定v123输入，三个指令字节和PE checksum局部候选；旧武器/公会等全部补丁和接收门保持。原生枚举/范围与741构造输入、全门/范围/覆盖保护及累计0区服双人认证/扣费回归分别验证，不冒充完整GUI验收。
- 独立master不借入MR33全部公会实现；组合Go测试仅累计树。完整规定检查、preview与EXE仓库Release及用户验收见R48回执。详Docs/GUILD_ZERO_REGION_AUTH_20261017.md。
## 2026-10-17 — 大会结果页返回城镇独立结算门

- test99/47真实121局两排奖励已落库，HKT14:50:11的op72 01 02却被普通settlementClearRewardSent挡住，之后菜单op42经大会门成功回城。大会不走普通result/card链，补其独立op72回城入口。
- 仅official24.9大会，保留当前角色/Completed/终局/奖励态并要求所有启用排已成功选奖；按既有ACK和完成局冻结原点路线回城。悬停不占动作，写失败续发不重奖，不伪造普通结算位，不改翻牌事务。
- 旧回归零回包；候选归属/未选完/动作边界、真实落金币后退出、legacy分派、ACK与op24失败续发通过。累计树真实PVF120..124完整四轮/两排/回城链见R45回执；原生UI待验收。详Docs/TOURNAMENT_RESULT_TOWN_20261017.md。
## 2026-10-17 — 入场只投影已保存的难度权限

- 当前v124 NOTI5真实handler两次回放确认，旧入场state1预占首次普通通关state1，使通关没有递增提示；只对official24.9默认改0，保留本角色持久值及PVF上限，缺归属/仓储/读失败不覆盖缓存。
- 保持既有选图交接与结算发包顺序、最高难度、legacy、重复通关和当前权限仓储；无EXE/PVF更新，不手工改玩家库。真实PVF与断线重建投影、入场/结算生产者测试及只读现场落库另存R49，原生UI仍待实机验收。详Docs/DUNGEON_PERMISSION_ENTRY_AUTHORITY_20261017.md。

## 2026-10-17 — 当前客户端大会翻牌命令分类与本局就绪状态

- 用户确认黄龙/青龙战斗和实际发奖正常，仅卡翻牌动画。01:07/01:08现场客户端发送bodyless457，服务端class0/457后3秒timer已落奖但不发458。固定v112 c82cb803的1fac980经25242a0将457→1fa5420、458→1fa5460注册至COMMAND表；2523cf6/251e1e0消费结果字节并走该表，class0走另一NOTI表。修正仅official-24.9的457/458分类为1，保留原9/7字节体。
- 当前457成功发出后在本run的tournamentReward记commandStateReady，timer仅该状态成立且非state回退档才投影已落奖卡。旧连接latch不授权新run，写失败不置就绪；其它客户端和显式旧档保持原行为。迟到点击投影原选择不重复给奖。没有更改奖励权重、倍率、资产事务或客户端文件。
- 新回归旧版真实复现class0错误与timer无投影；修正版检查class1顺序/精确投影、双排金币只发一次、迟到点击、写失败、未知profile、state回退、旧危险开关不污染新run。仍待完整验证、真实资源及用户原生验收，不以协议测试宣称动画已在实机播放。

## 2026-10-17 — 按PVF定向传送连接恢复遗忘之地跨房

- 现场4000/11251在8:6请求0:6与1:7被非相邻预检拒绝，真实DGN的warp map condition明确声明这两条有向连接。拓扑从完整闭合的source/dest坐标对增加WarpTargets，仅连接该迷宫中已存在房间，不造房间、不推断反向边，不将负坐标当通配；同格脚本标记不产生移动边。
- 非相邻移动只有命中当前房间的PVF传送目标才放行，沿用原清怪门槛、活动run、地图解析、预检/提交与重访缓存验证。失败不改变run；返回已访问房间保留绑定、死亡和清怪状态。未改包形状/EXE/PVF，不修改玩家数据。
- 旧实现真档8:6→0:6拒绝已复现；新真档两条连接及往返、声明方向/无反向、未声明/越界/闭格、损坏块、负坐标与重访状态专项通过。最初真实测试误要求构建完整layout，未合入主线的遗忘首领池问题造成无关失败，改用真实拓扑定位跨房入口；初版整段拒绝负坐标导致已证连接仍被排除，已按真档保留具体边而不授权sentinel，失败日志保留。仍待完整验证及原生验收，拾取另项保持开放。

## 2026-10-17 — 经验药与任务券 MR 同步最新主线

- 合并主线绝望之塔结算时，同时保留塔层数、奖励物品和经验药加成；结算重放读取原有物品与药水份额，避免过期后的重试改变奖励或重复到账。
- 补充药水与塔奖励共同结算、到期后重放及事务失败回滚回归；任务券行为保持。
- 合并候选的领域、bridge、control 全量回归 5512 通过／706 条件跳过；真实 PVF 药品与一觉券专项、list、vet、服务端边界检查和 Windows 构建通过。本轮仅更新 MR，未部署；保留此前玩家验收记录。
## 2026-10-17 — 账号金库事务按实际变化槽位写入

- 用户反馈账号金库存取延迟；现场52次金库操作未触发flag1等待，ACK中位77.8ms、最慢247.7ms。现有账号与角色事务每次整容器删除重插，在同一锁定读取和事务边界内改为比较已读取的原始快照，仅写实际变化的槽位及其extra；自动合并、拆分和删除不依赖客户端请求落点猜测。
- 优化只启用于WithinAccountCharacterItems新建的MySQL仓储实例；未先成功加载的保存继续整容器路径，普通保存与其它事务不变。保留父表时间更新、同事务提交与失败回滚，未更改协议或玩家数据。候选尚待验证与部署，不以SQL减少代替实机延迟验收。
## 2026-10-17 — 黄金蜜蜡再封装事务候选

- test99 02:06实际CMD81材料14被旧品级调整路径消耗并改变装备seed。新增独立蜜蜡事务，按当前PVF reseal.etc的等级、稀有度、次数权重计算费用，验证角色归属、封装资格、物品锁、期限、格子容量及材料；目标封装、次数与扣蜡同事务，保留品质、强化、耐久、附魔等原实例数据。
- 当前EXE CMD81成功体沿用u16材料槽/u32余量/u16结果，封装选择结果1。参考Go实现提供权重公式，旧C#近似表和12字节ACK不适用。本项仍是候选，完整验证、发布与用户验收尚未完成。
## 2026-10-17 — 疑惑之村电梯通知254配套客户端

- 用户反馈电梯一直下降无法进入下一房。固定v112消息过滤表未放行254，但当前EXE的NOTI处理器1fea260按两字节接收counter/state并更新1113电梯控制对象。配套候选v113只置254过滤位，111779840字节中仅一字节变化，保留此前客户端修复；未更改服务端定时器/清怪/移动规则。
- 全65536编号实际x86过滤函数对比只新增254，实际接收处理器/字段setter离线执行覆盖12组状态与无控制对象场景。真实PVF电梯资源保护补充任务3352的76384图；客户端二进制不入服务端源码仓库。尚待完整验证、配套签名发布与用户实机验收。
## 2026-10-17 — 史诗制作目标切换1416配套客户端

- 当前EXE已注册COMMAND1416目标切换处理器B210D0，真实日志角色47切换成功但v113过滤位未放行。配套v114仅新增1416接收位，保留v113电梯和之前塔修复。实际x86过滤/读包/赋值/刷新wrapper执行22场景，完整编号差异只有1416。
- 本MR只含客户端证据、可复验脚本及日志，不改服务端业务或玩家资产，不把已切换应答缺失当首次选择无请求的完整根因；GUI依赖桩掉、原生显示待验收。源码主线legacy编号与当前部署official profile明确分开记录，客户端EXE独立发布不入库。
## 2026-10-17 — 角色更名卡事务与当前客户端协议候选（验证中）

- 当前主线仅判读135、未处理509；按当前EXE真实builder/handler和选角双标志补齐候选。更名权与卡消耗同事务，新名/旧名保留与改名同事务；创建与改名共用数据库名字空间行锁，旧名保留跨账号生效。内存创建事务加入提交失败回滚，避免辅助存储失败留下半个角色。
- 当前EXE及真实PVF为协议与物品规则依据，旧参考代码只作对照；离线native与回归不等于用户验收。候选尚未部署、发布或合并，验证结果后补。
## 2026-10-17 — 2017劳动节随机称号附魔候选

- 拨云见日宝珠使用PVF auto enchant/random title enchant配置，旧普通宝珠路径要求怪物卡片而拒绝。新增真实配置加权结果，明确称号白名单、部位、锁定、期限及数量守卫；背包称号结果和宝珠扣除同事务，普通/随机附魔互斥并维护原始行缓存。
- 当前v115原生指令证明配置/行号位于119字节行+0x61/+0x63；合成与原始行共用投影，角色重建后仅为已附魔称号补发完整list3/op14。EXE/PVF、玩家历史资产与GM不变。本候选仅支持脱下称号后使用，完整验证与部署回执后补，用户实机验收仍待。
## 2026-10-17 — 更正剧情暂停候选的原报障范围说明

- 问题文档第150段已点名伦道夫任务、幽冥监狱、角色123及14:40时间。候选说明此前称“仅有副本名称与时间”有误，现区分已有任务名线索与缺失的现场连接/房间日志；不改变补丁、EXE、PVF、服务端或验收结论。

## 2026-10-17 — 剧情暂停通知接收门候选v116

- 当前v115发送COMMAND191，既有服务端返回class0/NOTI170，但接收门170位关闭。新增固定哈希的离线兼容生成工具，只将offset111747861由60改64；65536编号前后原生门执行131072次，允许数286→287，仅170改变。EXE与PVF不入库，服务端代码及BAT入口保持。
- 复核当前写点、class0注册、四字节回调与管理器初始化链；实际来源集合构造/增删8步通过，标准分配释放使用桩。最终暂停函数保护VM离线未执行成功，原用户现场未绑定；伦道夫剧情卡死仍开放，候选待实机验收，不据此推进任务或标记已修复。详Docs/STORY_PAUSE_GATE_20261017.md。
## 2026-10-17 — 悲叹之塔专属结算与逐层进度候选

- 接续完成局回城修复：悲叹通关现在把独立层进度、十槽PVF奖励、经验和回执接入原事务，满包/保存失败整体回滚，重放返回原奖励；下一层选本与入口守卫读取持久进度，第100层后按本机参考的悲叹循环回到第1层。绝望进度及奖励保持独立。奖励抽样采用本机2026-09-27参考GriefTowerRewards的每槽SHA-256/10000点策略，表与物品取自当前PVF，不把参考源码视为当前客户端证据。
- 当前v116的1263通知读u32时间/u16层/u8数量及物品数量对，UI按层+5114匹配悲叹资源、保留10槽并以FFFFFFFF标识空卡；op27末尾u16读入独立+e4控制器。新增class0/1263专属计划、槽位刷新与失败续发，完成后允许回城或其他选图，禁止重复挑战和普通69/70/71发奖链。
- v116门位1263为0，候选只改offset111747997的00→80。固定EXE输入0d470214…，候选269a7b6a…，PVF ad7d05c7…保持；65536编号共131072次原生门执行仅新增1263，实际reader/楼层映射/夹取与空卡分支另有执行证据。真实资源1/50/100层事务及重连选图、奖励/回城/重试守卫回归通过；完整验证、MR与部署回执后补，原生玩家验收仍待。

## 2026-10-17 — 悲叹之塔已完成局返回城镇

- 两次首层现场（5115，2026-10-16 06:20/06:23 UTC）均已提交通关、发送结果并接收46/123，随后42被普通翻牌链阻塞；这两局没有69/70/71握手。新增独立悲叹标记策略，在已持久化、结果统计完整且冻结计划的角色/完成键/来源一致时，允许42走既有完整城镇角色绑定与24路线。
- 已开始普通卡牌请求的局继续保留原卡牌完成门，普通副本与绝望塔不借用本策略。写24失败保留原运行时与冻结路线，重试只发送未成功部分；不合成翻牌或发奖，不更改层进度、门票、EXE/PVF及玩家历史数据。悲叹下一层、专属奖励与完整流程仍开放。
- 回归在旧代码复现结果已展示仍无法42回城；候选覆盖结果/统计/持久化/计划所有权守卫、重复请求及写失败重试。验证、部署及用户验收结果另记，离线测试不代表实机验收。
## 2026-10-17 — 礼盒绝对期限与历史实例兼容候选

- 按问题文档第158段，将既有日期工具和来源兼容单独抽成可评审候选；只扩展已识别礼盒到2028-12-31 06:00 CST，永久/更晚日期、奖励和相对使用期保持。工具与后端共用类型/精确路径判据。
- 独立来源清单记录精确旧日期，旧实例仅在来源已知且无GM/租用/相对/每日期限时续期；背包、个人仓库及账号金库沿用事务。原始行来源未知不猜续期。
- 已保存PVF候选13218脚本/13246日期，当前两档哈希已重新核对；六份身份待核脚本保留开放，尚不宣称所有礼盒完成。本轮未切换服务器/PVF或手工迁移玩家数据，验证与发布结果另记。
## 2026-10-17 — 普通增幅原生金币报价候选

- 现有普通增幅只读实例Extra费用而没有生产写入，新增当前专用PVF/原生计算器报价，沿用资产事务的金币预检、扣除和失败回滚。当前增幅表没有等级权重；原生独立对象清空两树、缺键跳过最终倍率，不借用强化表。
- grade、kind、rarity及逐步float32权重取整按当前EXE；损坏费用表与缺失装备元数据拒绝，不退回零费用。便携增幅器免金币/普通材料、券路径、强化公式及既有476转换保持；空环境仅增加普通增幅，显式非空关闭值保持。
- 真实PVF旧版失败和新候选回归、晚期事务失败/账号材料回滚及便携边界另记；完整验证、MR及部署尚待回执，未宣称原生验收或问题文档整段关闭。详Docs/NPC_AMPLIFY_GOLD_20261017.md。
## 2026-10-17 — 艾肯独立入场次数与原生周期通知候选

- 按用户“入场次数”报障，将独立entry消耗与既有成功clear进度分开，entry/门票/房间疲劳同事务且每run随机nonce幂等；单人/组队按当前原生Order共享额度，试炼继续要求成功通关前置。历史仅以已有clear证明最低入场数，不编造中止历史、不手工迁移玩家数据。
- 当前009 EXE实际656 reader/tuple/cache/聚合/UI证据明确独立周期模型，新增真实PVF全行快照在登录/选择/入场/通关同步；其gate仅单byte放行656，候选c8871a88…，全编号执行只新增该ID。B在已观察聚合/UI未用而发0；不声称官方服务器扣数时点已抓包证明。
- master缺少原有cycle基础，本MR补齐必要解析/clear/事务与nonce接线，累计分支保留其余在服修复。旧入场计数0红例、新入场/clear/frame/放弃/重放/日切、共享并发与末次保存回滚另记；完整验证/发布尚待回执，实机未验收，不合并。装备升级继续延期。详Docs/ECHON_ENTRY_COUNT_20261017.md。
## 2026-10-17 — 附魔师批量制作宝珠候选

- 最新段103多宝珠场景仍开放；旧单件附魔验收保留。当前EXE op238原生发送u32配方/u16次数/i16卡槽，服务端旧decoder和执行固定单次；现接通指定卡片堆的1..65535次独立判定、按尝试扣材料、按成功给宝珠与经验，整批沿用起始等级规则。
- 卡片/材料/产物/经验同既有资产事务；不足或晚期提交失败回滚。专属发放在合并前核对升级属性，匹配部分堆按PVF上限分格，保留不同升级与锁定宝珠。原op238结果和权威刷新继续使用，不改客户端/PVF、普通副职业或op601。
- 当前v120原生sender及ACK reader片段回放有明确夹具/UI边界；旧真实PVF batch3为0013拒绝，新回归覆盖批量、单件、数量/材料/账号/满包/升级身份与最终提交失败。全套检查、MR和部署状态以外部回执为准，实机待验收，不合并。详Docs/ENCHANTER_BEAD_BATCH_20261017.md。
## 2026-10-17 — 当前客户端公会成员菜单空对象候选

- 在已发布V118配套EXE中核实成员菜单单次调用仍读取空场景角色+3ac；独立适配MR33参考，仅此调用转向局部空指针保护，正常读取、寄存器/栈/flags保持。
- 工具绑定完整269a输入与16段布局、调用上下文；新增独立RX段，拒绝覆盖或未知输入。局部原生崩溃前后、等价读取、接收门/既有段/改动范围/校验和及输出保护验证另见Docs/GUILD_MEMBER_MENU_V118_20261017.md与本轮回执。
- 服务端与PVF不改，不引入参考分支其他公会功能；公会创建验收保持，成员菜单实机验收仍开放，发布与下载验签单独记录。
## 2026-10-17 — 魔界裂缝门票显示与入场预检候选

- 用户实际扣票2/4/6、选图始终2；当前EXE选图与预检真实算术执行证明是weight**(used+1)，PVFweight1使其恒2，旧native公式注释撤回。保持后端扣票与3次上限，配套精确v121客户端仅改两处算式为(used+1)**weight，既有公会/塔/接收门和PVF保持。
- 原生旧红、新候选4项无skip通过，两处算术无hook并核栈/寄存器/FPU、精确字节范围、全65536编号双镜像接收门、输入/覆盖守卫。隔离值和原生片段执行不等于完整游戏验收，完整检查/MR/发布以外部回执为准，未擅自合并。详Docs/RIFT_TICKET_DISPLAY_20261017.md。

## 2026-10-17 — 罗杰生产MR同步已验收主线

- !56/59/60/62已按用户验收合入master，MR58同步该基线并保留双方日志。NPC生产三份代码/测试文件与原2c04740完全一致，净差仍四文件；实机验收及用户合并继续待办。

## 2026-10-16 — 接通NPC生产配方ID模式（罗杰材料加工）

- 当前EXE op25支持7/9字节两种形状：UI+2a0为真直接发配方ID，槽位模式才调用背包索引解析。真实NPC29的product item20键控itemshop.lst的ItemProduct1.shp，表内31配方包含5072制作钢铁片（3027×1+1400金币→3150×1）。原服务端解码ID后固定拒绝，现接通菜单配方来源。
- 只允许当前已绑定角色的就绪城镇地图中NPC声明的product item菜单，表NPC归属及配方列表须对应；普通item shop不构成制作许可。ID模式不查找/消耗图纸，槽位模式仍消耗持有的图纸。沿用既有配方资格、材料/金币/产物规则与角色/账号仓库同事务，保留随机/升级等未证规则拒绝，未改当前客户端、PVF或玩家数据。
- 合成NPC/菜单场景与事务失败守卫覆盖无绑定/未就绪/错区域/隐藏NPC/错菜单归属/列表外ID/重复索引/缺料金币/锁料/事务内保存失败/ACK失败/换角；真实PVF31条菜单制作与缺料重放通过，5072材料金币产物有固定期望。原红用例证实ID模式被拒。首次保存失败注入误加在UOW外仓储，未命中事务，改为事务内注入后复测；未为夹具放宽产品。
- 用户已确认塔首层可通关返城；没有结算画面与再次选图仍首层继续开放。本批NPC制作候选不变更验收服或替代塔专属进度/奖励修复。

## 2026-10-17 — 剧情APC被动对象脚本死亡兼容候选

- 寻找阿甘左任务的真实PVF声明香料被动对象触发APC的SET HP 0。旧owner=ffff路径排除敌对APC，使该脚本死亡不能进入权威通知与既有clear-map结算。本候选仅在official-24.9严格请求形状、当前角色/run/房间/在册APC、活动任务目标和该APC的真实proc动作共同匹配时接收；无具体ID例外，不扩大教程和其它APC请求。
- 死亡后冻结本run/房间证明，写包失败重放原op38并继续未完成的原结算阶段；不再次发奖，不生成任务物品或改玩家资产。真实资源兼容回放完成3213进度持久化，边界与重试回归覆盖。原账号现场包仍缺，回放不代表原生验收；未合并或部署。细节见Docs/STORY_APC_PROC_DEATH_20261017.md。

## 2026-10-16 — 绝望之塔结果页按钮与配套客户端

- test99在23:18反馈结果页操作无效。实际op72返回城镇/选图请求被普通卡牌链挡住；塔现在在精确角色/完成键、持久化、46/123结果统计及348发送成功后处理town/selector，保留冻结路线和写失败续发，拒绝retry与未知操作。普通副本和原op42路线保持。
- 当前EXE原生重试状态/刷新定位完成；配套基于v111，仅塔348 kind1清理分支禁用retry，保存寄存器/flags并回原清理。用户追加的557跨界结果包放行配套同时完成，65536编号比较只有557改变。客户端不入服务端仓库；离线指令与服务端回归不算用户原生验收，557位开放不代表跨界事务已实现。细节见Docs/DESPAIR_RESULT_BUTTONS_20261016.md。
## 2026-10-16 — 绝望之塔专属奖励候选与同事务回执

- 接续!59的空物品结果体，按参考塔5次/镜像层9次+合成器/100层5次+奖章规则冻结候选。读取当前PVF的itemdropinfo_clearreward类型权重/稀有度阈值/grade窗口及装备creation rate，普通与avatar独立池，不复用普通翻牌的minimum-level选择或白装回退。1252/3314真实元数据核验，数量规则来自参考代码而非物品名称。
- 物品、经验/SP、层数与completion回执进入同一角色结算事务；保存原物品/list/slot，重试读原回执，不重新发奖。满包/保存失败整批回滚，真实分页容量和堆叠身份保持；avatar走既有list1创建投影，未知放置类别拒绝。原经验结果后先刷新已保存行再发348，写失败可续发。
- 真PVF1/10/100层冻结/落袋/回执重放与合成类型/grade/坏表边界、领域满包/保存失败、发送失败续发覆盖。本批为独立候选，未部署、未修改玩家库或补发历史奖励，!59结果窗口和层数仍待用户原生验收。规则来源和边界见Docs/DESPAIR_REWARDS_20261016.md。

## 2026-10-16 — 绝望之塔专用结果通知与角色层数持久化

- 用户确认首层可返城但无结算画面、再选仍首层。核证当前v111的class0注册表，NOTI348实际handler1c577a0读取时间/已通关层/奖励列表并打开塔专用UI；旧同号COMMAND回调不能作为通知协议。op27经实际NOTI接收器2039b30确认ContextValue1写绝望塔控制器，最小体offset15，非参考offset14。
- 通关最高层写角色Stats，进入既有经验/SP结算事务与completion回执；拒绝越层，单调保存0..100，重复不推进、失败回滚。选图按保存的下一层从真实PVF路径索引副本，prepare在门票消耗前复核每个角色；重连选图从仓储读层数。
- 塔在既有34/37/35经验状态结果后补发348专用窗口，保留角色/完成键/原始结果/统计/持久化与写包重试屏障；不移除可能承载客户端状态/123统计的原包链，普通翻牌保持。奖励列表本批为空，不把已拾取物品伪装为新授予，塔专属随机/固定奖励仍开放；未补写旧玩家通关记录。当前客户端/PVF无需改动。取证与验收边界见Docs/DESPAIR_SETTLEMENT_20261016.md。

## 2026-10-16 — 修复绝望之塔读条后落位超时断线

- 用户test99反馈21:51进绝望之塔读条后断线。实际21:50:45入场11008/map15130，op29已提交；背包快照写入超过3秒后，落位函数仍用已过期的短事务context读取装备/冒险团/任务，最终load clear quest list超时并关闭连接。通过日志错误长度与SHA256确认错误原文，没有把日志错误长度当下行包。
- 仅将post-start-map场景快照改用既有15秒仓储快照期限，并在独立背包快照发送结束后重建后续读取context，不让其写包时间消耗角色/任务读取期限。保留真实仓储错误和写包失败，维持包顺序、教程/组队屏障、门票及塔结算规则。
- 新回归注入成功但超过3秒的op13写入，并让任务仓储实际检查context；旧版复现同一任务读取超时，新版完整落位与原包序通过。1/2/4按用户编号登记已验收，第3项塔入场继续等待修复后原生复测，未据此合并MR。

## 2026-10-16 — 主动TP技能不自动占用普通快捷栏

- 真档中主动TP技能（job11/id220 强化-招架反击）新学/补全布局时会自动占用普通快捷栏。学习与布局重建的共用选槽助手、初始前三主动技能布局都排除IsTPSkill，仍给技能页展示槽；普通主动技能自动填栏、已有布局和op28手动拖动保持。
- 实际ApplyBuy回归及固定PVF的781支TP布局检查旧红/新绿。原反馈点名的强化-上挑（job0/id215）是passive，本来就不进该分支，未复现该技能现场症状，继续开放；本补丁仅关闭可证的主动TP自动填栏缺口。
- 子代理最终口述曾引用历史PVF哈希369d072而非指定文件，父已要求纠正并以实际ad7d05c7固定文件独立补跑。此记录不把子代理口述哈希当验证来源，最终结果以本轮回执为准。

## 2026-10-16 — 悲叹之塔独立资源标记

- 当前真实PVF的悲叹之塔100层（5115..5214）在maze之后声明空段tower of grief，过去归入maze未知段，领域元数据丢失塔身份。将该段登记为地下城整数元数据，裸标记保留1，显式值原样；无段不补标记，非整数/纯符号继续诊断。仅保留资源身份，不赋予绝望之塔身份。
- 合成段归属及固定真实PVF全部100层检查旧红/新绿；既有绝望之塔标记边界保持。新增解析登记会被isDungeonMetadataSection使用，因此即便声明位于maze之后也正确归地下城；子审查关于“登记后仍会被maze先截住”的一句推断已由实际红绿更正。
- 真实首层5115/map100247/APC470回放确认死亡、46/123结果可完成，42仍等待普通翻牌。这是请求夹具回放，不证明真实客户端不发送翻牌链；当前日志未找到该塔入场，参考树也没有grief协议判据，因此本批不改返城、进度或奖励规则，悲叹之塔原反馈继续开放。

## 2026-10-16 — 主线接取、塔退场及进图难度权限候选

- 依据20:00更新的问题文档，阻止已转职角色接取必定无法结算的新转职任务；对于PVF双向声明互斥的同族转职任务，只允许持有一条。接取与可接列表共用判据，已有任务保持可见、幂等及可放弃，不批量清理玩家记录。该补丁包含原!31前置规则及本轮同族互斥增量；旧主线错序和已卡历史任务仍待原生验收。
- 绝望之塔按真实PVF空段标记识别。在通关持久化、角色/完成键对应及46结果/123统计均就绪后，允许42返城，不再等待该客户端不发送的普通翻牌链；写包失败可重试。未实现塔专属进度/奖励。新反馈悲叹之塔保留独立开放，不能只按名称套用。
- 进图op5原来固定发送状态1，可覆盖已保存的高难度状态；现在取角色已保存权限与入场最低状态的较大值，再将已存权限裁剪到PVF上限。读取失败时不发送潜在降级通知，继续已经提交的进图，不写回权限。旧状态4被发为1已由测试复现；与伦道夫退出后难度消失的现场因果仍待复测。
- 本候选基于远端master42eb931，仅含上述服务端改动；不带累计工作树中的单人卢克、活动、客户端或其他候选。已验收和延后项保留；新增问题及旧/新段落映射见Docs/ROUND7_MAINLINE_DUNGEON_20261016.md。验证回执在本轮外部工作目录，未合并、未部署。

## 2026-10-16 — 修复一次觉醒与当前等级主线任务完成券

- 接入当前客户端一觉 118、主线 169 和按等级 179 动作，使用原生成功／失败确认包；重新校验归属、物品、期限、等级和职业，无可完成任务时不扣券。
- 一觉券只完成匹配职业的一觉任务并更新成长阶段与技能，保留已学技能和 SP/TP。主线券按角色当前等级、道具上限与 90 取最小值，关联子任务同样检查等级和职业。
- 任务、角色、技能与扣券在同一结算事务提交，不补发任务奖励。现有 10301410 可用于当前等级主线；10164630 仍要求 PVF 规定的 90 级。二觉及指定任务清单券不在本次范围。
- 补充事务回滚、重复使用、等级边界、原生包与刷新顺序回归；真实 PVF 已核验 14 个常规职业的 56 个一觉分支。游戏内交互与真实数据库待验收。
- 公开基线验证：领域、bridge 与 control 全量回归通过，list、vet、服务端边界检查和 Windows 构建通过；数据库相关条件跳过，未部署。

## 2026-10-16 — 修复经验药实际结算

- 按 PVF 通关／任务经验标签持久化倍率、有效期与冷却；扣药和效果写入共用角色资产事务，并校验归属、期限、等级与职业。
- 通关与任务结算按基础经验增加药水份额，保留成长契约等奖励口径；结算回执保存已提交加成，过期后重试不重复发经验。
- 补充倍率、有效期、冷却、普通／无限药、失败回滚、合约叠加与回执重放回归。真实 PVF 已核验 580 种通关和 2 种任务经验药定义；游戏内经验到账待验收。

## 2026-10-16 — 史诗图鉴制作用户验收通过

- 用户在v108（基于EXE107）明确验收史诗图鉴制作，并要求提交合并。解析真实PVF配方，788按输出装备ID提交碎片、两种灵魂扣除与装备发放，账号物品/碎片在同事务提交；失败回滚，成功刷新账本和背包。
- 仅整合已验收图鉴功能及必要仓储/分派，不包含单人卢克、图片活动、塔/大会或完整史诗生产领取等未验收内容。客户端788接收位须使用已发布配套v108或其后继，保留107改动。完整史诗生产另项开放。
- 独立当前master集成检查：领域、桥接、control、vet、双平台构建及图鉴真实PVF专项通过；一次性MySQL用例无DSN跳过，既有真实SQL事务证据保留在本地报告。

## 2026-10-16 — !50 修复70级异次元首饰精炼误拒绝

- 用户test99报告00:38对70级装备精炼提示材料不足。只读日志定位00:37:58 op370，装备425064/槽34、蓝色气息1255/槽98、选项14；错误哈希对应ErrTarget而非实际缺气息。真实PVF的裂魂之异次元戒指没有durability字段，旧解析器误将无耐久部位全部拒绝。
- 对PVF未声明耐久的项链/手镯/戒指/辅助装备/魔法石沿用既有投影的零耐久语义；武器/防具缺字段、显式非法耐久仍拒绝，事务满耐久检查不变。没有补造装备、材料或PVF规则，不修改玩家数据。
- 隔离内存按实际请求、物品元数据和已记录扩容重放，原版本目录与handler均红，新版成功扣一气息并保存选项/刷新；覆盖11部位×5种字段边界，保持六种气息与既有真实PVF行为。!50仍待用户原生复测验收，不合并。

## 2026-10-16 — !49 / !48 / !2 用户验收后合并

- 用户明确验收技能快捷栏、复活币道具和!2游戏服务端修复，授权合并。!50的70级异界精炼反馈单独继续修复，不纳入本次合并。
- !2同步已验收主线与!49/!48，解决转职身份/任务通知、幸运星事务回执和已移除内嵌GM的冲突。保留验收服的活动技能页、同图任务门槛刷新与唯一金币行发布，不恢复旧内嵌GM。
- 保留旧独立幸运星领域API的账号事务保护；当前通关采用!2结算回执，避免重复发奖。真实PVF购买、任务、转职与回归/构建/BAT证据在外部验收合并目录，不将远端CI标作成功。

## 2026-10-15 — 双技能页：任务完成后刷新当前页

- 普通任务及转职/觉醒任务完成后，技能快照使用已提交角色的当前技能页布局，避免第二页激活时错误发送第一页快捷栏。该补丁已随本轮验收版本部署，现补入 !46。
- 增加实际任务完成发送链路回归，覆盖升级、SP 奖励、觉醒三种情况，检查两页技能、SP/TP、当前快捷栏和仓库存档不变。

## 2026-10-15 — 兼容已观测的 594 字节登录握手

- 将完整 class1/op1 的 594 字节请求体加入上行分流与端点握手长度识别，避免 607 字节首包被归入旧协议、无法进入现有令牌绑定和握手流程。
- 只增加已观测的精确长度，保留现有令牌解析、账号绑定与握手单次响应行为；不扩大相邻长度或改变账号认证策略。
- 回归覆盖分片重组、593/595 字节拒绝、594 字节生命周期及端点应答，并验证有效、未知、缺失和过期令牌的账号绑定边界。新增场景已在修复前复现失败；自动化验证与玩家原生登录验收分别记录。

## 2026-10-15 — 修复90版金库升级上限与即时刷新

- 个人金库合法容量扩展到264格，账号金库扩展到240格。个人升级目标优先读取工具脚本[explain]，修正safe_upgradekit2及8/9等旧路径后缀与真实档位不一致的问题；账号档位、商品和道具身份读取etc/accountcargo.etc的[upgrade info]，价格来自同一PVF的etc/cerashop.etc，支持移出旧目录的后续升级工具。
- 旧商城窗口重复购买时顺延到实际下一档并按该商品价格扣费；满级、余额不足、配置缺失或事务中容量变化时拒绝，保留原子扣费和回滚边界。成功结果确认原请求商品，保持客户端购买状态对应。
- 个人扩容在商城op64前发送class0/op66子类型1的u16容量通知，更新档位而不重建整份物品列表，修复换角色后未打开过金库直接购买出现闪退的复现路径及下一档商品不立即刷新的问题。账号扩容在op64前发送class1/op306成功通知和权威list12容量，使已打开金库立即显示新增格。未修改客户端或接收开关。
- 回归覆盖真实PVF全档商品/价格、普通库存保留、新格边界、旧档位计价、满级/余额不足和陈旧计划拒绝。原补丁基线db11236的独立纯服务端完整领域/桥接/控制器9622 PASS、883 SKIP、0 FAIL，真实PVF专项5 PASS；gofmt/list/vet、仓库边界和Windows/Linux编译通过，BAT配套预检明确跳过数据库和运行中端口。原生验证在已安装配套上通过个人换角色首次/连续升级与商城即时换档、账号144到240连续升级及新增格存取；新主线整合以本轮自动化检查为准，不扩大为所有库存操作已验收。
- 合入主线7d820f631e53的594字节登录握手修复，保留双方新增记录；本次仅处理该记录冲突，11份金库业务源码及测试逐字节保持。


## 2026-10-15 — 保留已部署排序锁用例的金库容量前置

- 合并MR26与已验收MR41时，旧cargo_move夹具未解锁目标槽10，核心回归被现有容量守卫正确拒绝。恢复已部署2f0d20b中同一测试的24格金库前置，不改产品守卫或锁标志断言；首轮失败记录保留。

## 2026-10-15 — 合并用户确认的第三/四轮修复，保留失败子项

- 用户已验收乔纳森、晶体切角、金币上限、黄锁、蓝锁和锻造窗口；附魔材料及幸运星金币扣除子项通过。只集成这些实现及回归。
- 幸运星点数即时显示、附魔错窗和菜单入口仍失败，组队返回按钮未反馈；未将相关候选整体推入主线。保留主线已验收金库容量与放置校验。
- 客户端v102已独立发布，乔纳森仍显式candidate开关；现网本次不重启。完整边界见Docs/R4_ACCEPTANCE_20261015.md。验证以本次最终组合回执为准。

## 2026-10-14 — 觉醒任务接取复用结算的阶段检查

- 真PVF的部分觉醒奖励任务按转职分支筛选，但接取时未核对觉醒阶段，已觉醒角色仍可能接到再次一觉的任务，最终结算被既有阶段规则拒绝。
- 将结算已有的阶段检查提取为profession.CheckAwakeningPrecondition，供新接取和可接列表共同使用。合法一觉/二觉保留；存量active任务继续可见、可放弃，不清理玩家任务数据。
- 补充真实PVF回归，包括不合法阶段接取零写入、合法阶段对照、存量任务放弃及虚空行者2695入口保持可接。核心9294通过节点/862跳过/零失败；真实PVF 144个指定根全过（657通过节点/2未选中包）；gofmt/list/vet、Windows/Linux构建及GM边界检查通过。BAT缺源码包未含channel_info.etc，不计部署通过。
- 用户补充确认DOC126原始“任务列表缺失/无法接受”已正常，登记已验收并停止排查。历史线上日志有char94/2695列表构建和发送事件，离线记录重建也包含2695；这些证据不证明客户端显示正常，本项阶段保护尚未部署，用户验收不能归因于本候选。
- 基于server-only master9de2ecb；GM不进入在线仓库，未部署生产。详见Docs/D33_AWAKENING_PREFLIGHT_20261014.md。

## 2026-10-14 — 转职接取候选主代理完整验证

- 源码e086018：核心9317通过节点、863跳过、零失败；固定真实PVF的144个指定回归根全部通过（655通过节点、2未选中包）；gofmt/list/vet及Windows/Linux构建通过。BAT预检因源码树缺少deploy/assets/channel_info.etc失败，未部署。
- 仅阻止新接取无法结算的转职任务，已有任务及其他DOC-124症状保留验收边界。

## 2026-10-14 — 已转职角色不再能接取永远结算不了的转职任务（DOC-124）

- 缺陷（真档固定 PVF，sha256 369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06）：[reward type] [grow type] 的老式转职任务只有 [job] 与 [grow type] -1，没有转职阶段门槛。审计全部 114 条 grow type 任务 × grow {0,1,2,3,4,0x11}：285 个组合能通过接取预检（DecideAccept=AcceptNew）却必然在结算被 profession.PlanTransition 以 ErrTransitionOutOfStep 拒绝，涉及 57 条任务——16 个职业的整条老式转职家族（例：7814 阿修罗、2633/2634/5761/5762 魔枪士、7803/7807/7810/7814 剑魂/鬼泣/狂战士/阿修罗、2908-2911 女圣职、2684/2685/2686/4065/4068、4621/4622/13101/13102、6881-6883/6926、1687/2616/12757/12770 等）。玩家接下后得到一条永远做不完的 active 任务，与 DOC-124「转职和主线任务反过来了 / 任务不能完成」的服务端形态一致。
- 修复：把 PlanTransition 的转职前置抽成 profession.CheckClassChangePrecondition（同一条规则，不再两处各写位掩码），PlanTransition 自身改调它；quest.classChangeRewardAcceptable 在接取预检 definitionAcceptable 里对 [reward type] [grow type] 且 KindClassChange 的任务复用该前置，让接取预检、结算与 op21 可接投影三者一致（bridge 的 op21 直接使用 Catalog.QuestList，因此投影同步收敛）。
- 范围：只阻止**新接取**（已 active 的行继续留在可接列表/快照里，玩家仍能看到并放弃或结算它，避免把「仅阻止新接」误报成「存量卡任务已修复」）。觉醒任务（[awakening type]）与 15 级分支主线（[job change quest] 1 但 reward type 为 [item]）不受影响；grow 0 的合法链路保持可接。
- 存量卡任务：真档回归确认已经 active 的此类任务仍能正常放弃（questGiveUpNeedsAssetTransaction 对这些 [clear map] 转职任务为 false，CantGiveUp 未置位），放弃后行被移除；本修复不改任何玩家数据、不批量清理存量行。DOC-127（接了任务无法放弃）是另一条报告，未在此处理。觉醒家族另有一处同型残留（176 条 awakening type 任务在该审计口径下有 5 个组合可接不可结算），按「保留觉醒行为」未改，DOC-126 保持开放。
- 回归（旧源码失败、修复后通过）：quest TestRealPVFJobChangeQuestRefusedForChangedCharacter（阿修罗 7814 与魔枪士 2633/2634/5761/5762：grow 0x01/0x02/0x03/0x04/0x11 必须 AcceptRejected 且不进可接投影；grow 0 仍 AcceptNew 且在投影内）、quest TestRealPVFJobChangeAcceptEntryRefusesChangedCharacter（走 Owner.ApplyAccept 接取入口：grow 0x01/0x11 被拒且零写入，grow 0 对照正常接取落库）。基线上这两条分别失败于「decision=1 err=nil, want rejected」与「ApplyAccept err=nil, want ErrQuestNotAcceptable」。
- 守护（两个版本都通过，证明改动没有扩大范围）：quest TestRealPVFAwakeningQuestStaysAcceptableAfterClassChange（已转职未觉醒 0x01 仍可接剑圣觉醒 977，计划侧仍产出 awakening 请求）、quest TestRealPVFStuckJobChangeQuestCanBeAbandoned（存量 active 行为可正常放弃）、profession TestCheckClassChangePreconditionMirrorsPlanTransitionState。
- 验证：见提交说明；本候选只动 profession/transition.go、profession/profile.go、quest/catalog.go 与两个测试文件。

## 2026-10-14 — 女圣职者 15 级主线/转职链路真档回归（DOC-123）
## 2026-10-15 — Check rental wallet send failure after committed purchase

- Add a real-PVF/memory regression injecting zero-byte and seven-byte-prefix errors on the wallet op14 write after the successful op1000/op985 frames. The original connection error propagates; gold and lucky-star points remain committed exactly once, without a compensating failure ACK or automatic repeat purchase.
- A fresh backend service/session reloads and explicitly projects the committed values without another asset transaction. Both cases pass. This manual backend projection is not a full login test, native display acceptance, or proof of the original incident's cause.

## 2026-10-15 — Preserve exact enchant target identity through handler serialization

- Add a pinned real-PVF, independent-memory 601 handler regression for belt100220137/card3621/recipe10015129 at target28/card237, with weapon28309 in decoy slot9. Both peer-store and registered-self-store requests must produce one successful ACK before the main op13, retaining each slot identity, target enchant and card count; a short-material request preserves all fixture assets and sends no success refresh.
- This fills the gap left by the older direct-commit test with a dynamically selected target/card. Deep-copy boundaries and actual serialized rows were independently reviewed. Three cases pass; legitimate three-byte empty non-main container pages are not misclassified as malformed main pages.
- No product behavior change or client-window fix is claimed. The regression cannot reconstruct the historical callback global, object lifetime or icon display, and does not implement an unverified personal self-entry protocol.

## 2026-10-15 — Replay Jonathan payment through the real request handler

- Add an isolated memory/real-PVF request-handler regression using observed target28309, target slot54, normal-material slot358 and NPC476/material slot135 trailer fields. The name and character are synthetic; this is not a replay of historical player state.
- Default-off control leaves conversion stones unchanged. Candidate mode consumes one stone and ten ordinary materials without gold; ordinary NPC mode consumes gold84975 and the same ordinary materials. Missing conversion material returns the existing rejection with all assets unchanged. Check ACK-before-op14, target identity, committed material quantities and the ordinary-NPC wallet balance on the serialized wire.
- The whole handler replay passed together with the party candidate and other backend trials. It does not enable the default-off fee candidate or establish native consumption/display acceptance.

## 2026-10-15 — DOC075 后续：独立最小双位准入候选与既有候选查重（离线，非生产）

- 查重（第三轮既有白名单候选）：client-candidate/DNF.exe（sha256 6e61c3615f37fff6b6b9ad7b6e2f7878ee0d33bc2ed8f505ca6af5ebbfe30bcf）只放行 438/713/714，client-candidate-v2/DNF.exe（sha256 1be9466fb7a76611ea4894608c4601f018462253e9293ab8f85bd9e94161a4e3）追加 947/978；两者分别只改 2 与 4 个位图字节，**都不含 0x382(898)/0x217(535)**，也没有对 .cryssel/.dshcard 加任何东西（这两段本就在基线镜像里），因此本次晶体两位不重复。生成器与格式见 E:/90/repair-round3-20261015/prepare_client_candidate.py 与 prepare_client_candidate_v2.py（断言基线 sha → 只 OR 位图位 → 断言改动偏移集合 → 全量 65536 opcode 仿真对比）。
- 新增独立最小候选（仅供评审的离线副本）：crystal-evidence/prepare_crystal_gate_candidate.py + DNF-crystal-contract-898-535-candidate.exe（sha256 4020530dbdcb910d42d79229fc3d8481316e685286da7561247450a32f8e9274）。脚本严格校验基线 sha256 1ac12645…fd4331 与位图原字节（0x6E92342=0x20、0x6E92370=0x08）、只 OR 目标位；改动文件偏移恰为 111747906 与 111747952 两处、每处 1 位（即两个 bit、两个字节）；全量仿真显示放行集合 223→225 且变化的 opcode 恰为 {535,898}；清位还原后 sha 与基线逐字节一致（可恢复）。
- 幂等：同一输入重复执行不重写（第二次 pristine 运行 idempotent_no_change=true 且 mtime 不变）；把候选自身当输入时走 already_patched_input 分支——判定规则是“清掉目标位后等于基线 sha”，不靠猜测，同样不重写。
- 独立复验：修正版门控探针以 --exe 指向该副本重跑（probe_gate.py --exe … --out-json v101-gate-probe-crystal-candidate.json）→ op535/op898 admitted=true、放行计数 225、锚点 14/251 放行与 252/713/714/947/978 丢弃保持不变。
- 组合方案（只说明、不生成）：若最终需要一个候选，可在 client-candidate-v2 基础上再置 0x6E92342 bit7(0x80) 与 0x6E92370 bit2(0x04)，预计改动 6 个位图字节，放行集合 = 438/713/714/947/978/535/898；本脚本支持改 --opcodes 自行产出，但本轮未产出组合件。
- 更正上一条描述：v1 探针的仿真输入本身是正确的（在 0x6E83610 执行真实谓词、位图已在 0x6E92300），错在把位图地址当成 gate 函数标签、并把 res[0]=0 当“接受值”导致极性反转；留档已更名为 probe_gate-v1-mislabeled-gate-va-and-polarity.py.bak。
- 边界：产物仅为 crystal-evidence 内的离线副本，未安装、未部署、未运行 UI、未改生产 EXE/PVF/服务端；静态位图丢包＋客户端进程级缓存仍只是**机制证据**，不是已验收的实机根因。

- 报告（固定真档 Script.pvf，sha256 369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06）：玩家报告「女圣职者15级主线完成后没有转职任务也没有主线任务」。当前主线上的真档走查（job 14 = character/character.lst 第 14 项 `Priest/ATPriest.chr`）为：完成 3151→3152→3153 后出现该职业自己的「守护森林的战斗」分支（占位 6929 与真实分支 6945–6949），完成任一分支解锁 3155「赛丽亚的决心」，再由 3155 开放 2908–2911「转职 - 圣骑士/异端审判者/巫女/诱魔者」。真实旧源码（`2da7982^`）按 `[target character]` 元组精确比较，真实分支携带的 -1（该分量不限制）永远等不上打包的成长半字节，于是只留下既无奖励、也不在 3155 前置里的占位任务 6929，链路在 3153 之后断死——重现了与报告一致的「无后续主线、无转职任务」症状。
- 2da7982（DOC-116 同批）把 -1 视为不限制该分量，且 2da7982 已是 9de2ecb 的祖先；本次不改运行逻辑，只补上此前仅覆盖剑魂（job 0）的真档回归。当时在线版本与角色存档未经证明。
- 回归（固定真档，旧红新绿；旧源码基线 `2da7982^`＝78f90b6，catalog.go 仍为精确比较）：
  - 新增 `TestRealPVFAtPriestLevel15BranchReachesClassChange`：钉死 6945/6946 的名称与 `[target character]` 元组、3153 后 6929+6945–6949 必须可接（6945 DecideAccept=AcceptNew）、完成分支解锁 3155、完成 3155 后 2908–2911 可接。基线失败于「at-priest level-15 branch quest 6945 not offered after the mainline; listed=[3105 … 3145 6929 8581]」。
  - 新增 `TestRealPVFEveryJobLevel15BranchReachesClassChange`：对 character.lst 的每个职业跑同一条链（分支 → 3155 → 转职）；黑骑士(9)/缔造者(10) 按真档留在普通转职家族之外。基线 14 个职业失败于「no level-15 branch variant」。
- 局限：本项依据真档目录、前置与目标元组推导，未取得该玩家角色当时的 quest_states（现网只读快照中没有 job 14 角色），因此未复现该角色的具体存档状态；本条只声明当前主线（9de2ecb）上该链通过，DOC-123 仍待原生验收。DOC-124（已转职角色仍可接永远结算不了的转职任务）属另一候选，未包含在本次改动内。
- 时间线：报告为 10.13 14:30；包含该判据的 D33 已知问题分支在 10.14 15:52(HKT) 才并回 master（5fc6807）。报告当时线上是否已含 2da7982 无法从本仓证明。
- 验证（本树，改动仅测试文件头注释与本文档）：gofmt 无差异；`go list -buildvcs=false ./...` 211 包；AGENTS 指定的 vet 目标（cmd/server/control、cmd/server/dnf90、cmd/server/doctor、internal/app/dnf90、internal/services/dnfbridge）0 输出；核心 `./internal/modules/dnf/... ./internal/services/dnfbridge ./cmd/server/control` 79 个包 ok、0 失败、2 包无测试文件（dnfbridge 22.2s、control 6.1s，本机未设真实 PVF 环境变量故真档用例跳过）；Windows 构建 `go build -buildvcs=false -o ..\runtime\bin\DNF90Server.exe ./cmd/server/dnf90` 通过并产出二进制；固定真实 PVF（sha256 369d072b…）两根 `TestRealPVFAtPriestLevel15BranchReachesClassChange`、`TestRealPVFEveryJobLevel15BranchReachesClassChange` 全部通过。BAT 预检 `control.bat check --skip-database` 在本机需先把 Go 加入 PATH（否则报 DNF90Control.exe/Go 缺失，exit 10），加 PATH 后仍因源码包缺少 `deploy/assets/channel_info.etc` 失败（exit 1），故未计部署通过。
## 2026-10-15 — DOC075（原 P62）晶体契约跨角色串用：客户端准入位图复核与换角重放回归

- 现场：同一账号角色 1 选了晶体契约属性后，其他角色也显示同一属性；重登/换角后选择还会“消失”。服务端把选择持久化在角色自身 Stats[premium_crystal_selection]（crystalcontract/owner.go），角色作用域本身正确，问题不在持久化。
- 客户端复核（真实 v101 DNF.exe，sha256 1ac1264553f8f762e64f2c637c4c26d3bf5ff0fdc60a090ca8b077d452fd4331，只读反汇编+仿真，未改二进制）：接收准入谓词在 .rebuild 段 0x6E83610 = bt dword [0x6E92300], opcode + setb al，即 **EAX=1 才放行**；位图在 .dshcard 段 0x6E92300，每 opcode 一位、无 class 维度。上一轮探针把位图地址 0x6E92300 当成函数入口（实际是数据），又用 res[0] 当“接受值”，结论整体被反转。修正后对全部 65536 个 opcode 求值：**op898(0x382) 与 op535(0x217) 的准入位都是 0**，即晶体契约状态通知与选择 ACK 都在分发前被丢弃；同时复现上一轮锚点（14/251 放行；252、713/714、947、978 丢弃），并给出最小放行改动位置。
- 客户端处理器（同一 EXE）：class0 注册块 0x4454FDE 把 535→0x212C4B0、898→0x212D6F0；898 处理器只读报文第 2 字节作为选择值（0xFF=未选），末尾跳进新增的 .cryssel detour（0x6E97000）写入进程级全局缓存 0x5590CF0。535 处理器以 class1 信封字节为 ACK 标志，负载为 {sourceFlag, selection}——这正是上一轮包读取日志“B ack 标签错位”的成因。缓存是**一个客户端进程一份**（服务端驱动的写入点只有 898 detour，另有客户端自身选择/清空写入点），服务端无法按角色刷新它，这就是跨角色串用的机制。
- 服务端不改产品代码：客户端在分发前丢包，本仓库无法修复；按“可证实才改产品代码”的要求只补测试。新增 go-server/internal/services/dnfbridge/crystal_contract_doc075_test.go，读取一律走会话自己的连接、断言按包型（class+msgID）而非读取顺序，覆盖：两角色不同选择的首次登录与重登隔离、同连接 op7 换角后按当前角色重放一次（不重复、不带上一角色选择）、换回原角色后重放原值、换角不篡改对侧持久值。旧的 zz_scratch_crystal_isolation_test.go（角色 21 选择时误读角色 19 连接）已删除。
- 验证：go test -buildvcs=false -count=1 ./internal/services/dnfbridge -run "TestDoc075|TestCurrentCrystalContract|TestLegacyCrystalContract|TestTownAreaReadyReplaysPersistedCrystalSelection|TestDeferredSelectSceneTail|TestPremiumGameplayModuleRegisters|TestCurrentLimitedCube|TestCeraShopPurchaseCrystalContract" 全绿（23 根用例、0 失败、0 跳过）；go fmt 无输出；go vet ./internal/services/dnfbridge 通过。按父代理要求单进程运行（GOMAXPROCS=2），本轮未跑完整回归。
- 边界：未修改任何 Go 产品代码、PVF、客户端二进制、部署或线上数据；未部署、未推送、未合并远端。证据目录 E:/90/repair-round4-20261015/crystal-evidence（probe_gate.py、probe_898_handler.py、v101-gate-probe.json、v101-crystal-handler-probe.json；旧探针与其错误结论分别留档为 *-v1*.bak / -v1-invalid.json）。
- 父代理待决点：v2 客户端白名单候选若要让晶体契约可见，需在白名单位图额外放行 0x382 与 0x217（当前最小改动：0x6E92370 bit2 = 0x04、0x6E92342 bit7 = 0x80；不可只放行 898，535 的 class1 ACK 同样被丢）。原生 GUI 验收仍未做，本次只有离线反汇编/仿真与服务端回归证据。

## 2026-10-15 — Preserve native upgrade NPC context and isolate cost arithmetic

- Decode the optional v101 op50 trailer as NPC template u16 + conversion-material slot i16, after the counted name. Captured dc018700 decodes to (476,135); ffffffff and legacy absent trailers remain distinguishable. Reject partial/unknown trailer lengths and oversized names. These are lookup hints only; no client-supplied price or material identity becomes authoritative.
- Parse current-level cost-weight triples without dropping fractional tokens or shifting keys. Add a separately tested native arithmetic helper: base table index, type, rarity, then current-level weight, with float32 weights and integer rounding at every stage; type 24 aliases the weapon type weight while type 25 consumes the final PVF entry. This helper is not wired to payment yet.
- Evidence: current EXE native instruction slices pass 42 rounding and 7 conversion boundary probes; decoded native PVF parser tags confirm NPC conversion fields and the two level-weight maps. Go tests cover trailer formats, malformed lengths, stage rounding, malformed triples, and fixed real-PVF values. Baseline overlay rejects test is red (old parser accepted all four malformed trailers); new parser is green.
- Still open: resolve the native equipment cost-index field to its exact PVF source; reconcile display ceil versus preflight floor-plus-one conversion, and then connect validated fees/materials to the atomic owner path. This commit does not claim Jonathan material settlement or native acceptance.

## 2026-10-15 — Preserve fractional upgrade cost-table weights

- Read rarity and equipment-type cost weights through the PVF numeric accessor. The integer-only accessor dropped decimal tokens and shifted subsequent indexes (real upgrade.etc starts rarity weights 0.50/1.50 and type weights 1.20/0).
- Regression fails on the original parser and passes after repair; pinned real-PVF weight and existing upgrade-material checks pass (3 roots, zero skips). This repairs the parsed input only: Jonathan fee settlement and native material consumption remain open.

## 2026-10-15 — 新段102 购买幸运星后的已提交金币钱包投影（租赁充值 op1000）

- 现场：租赁武器系统用金币购买幸运星（op1000 充值）成功后，背包金币显示不变，只有重选角色（整容器重建）之后才减少。服务端事务本身是对的——金币已经落在角色仓储里，缺的是成功路径上的即时钱包投影：改动前成功只回厂商 13 字节应答 `01 <i32 mode> <i32 quantity> <i32 totalLuckyStar>` 加一条 op985 租赁点面板刷新，没有任何一条报文携带已提交的金币，客户端本地金币行因此保持旧值。
- 权威来源：`purchaseCurrentRentalPoints` 走 `dnfrental.Owner.Charge`，在同一个 `RentalAssets` 事务内扣金币、加幸运星并把 `character.Stats["gold"]` 写回，返回的 `WalletResult.Gold` 就是提交后的值；投影用的就是这个值，不重算、不读缓存、不从请求推导。
- 服务端修复：成功提交后追加一条 class0/op14 的已提交钱包行（`buildCurrentGoldStateBody`，list0/slot0、itemID 0、余额在 stack_count），顺序固定为「op1000 应答 → op985 面板 → 钱包行」，是严格增量：不改任何既有报文的字节、顺序或 sequence 分配。事务前拒绝（解析失败、未选角、PVF 目录不可用、owner 不可用）与事务拒绝（金币不足、幸运星越限、仓储失败）一条都不发，失败仍然不扣。
- 形状依据（非新发明）：与既有 carry-gold `sendCarryGoldWallet`、掉落拾取、普通 NPC 商店、登录与整理的钱包投影逐字节同形（3 字节头 = listType + u16 行数 + 0x77 行），既有 `TestCurrentRentalProtocolBodies` 已钉住同形；当前 v101 客户端门控位图 0x6E92301 bit6=0xFF 允许 op14，DOC029 现场已观察该钱包行被接收并更新金币显示。
- 开关联动：新增 `DNFBRIDGE_RENTAL_CHARGE_WALLET`（空/vendor = 开启，off/0/false/no/legacy/disable/disabled = 逐字节回退）；既有 `DNFBRIDGE_RENTAL_REJECT_ACK=off` 同时抑制本投影，保证那条开关“op999/op1000 整条线逐字节回到今天”的承诺继续成立。
- 验证：旧红新绿——只把 `gameplay_module_rental.go` 回退到 HEAD（测试与新文件保留）时 `TestRealPVFRentalChargePublishesCommittedGoldWallet` 失败（`game upper packet too short: 0`，即应答+面板之后没有第三条钱包报文），恢复后 7/7 全过、文件 SHA256 逐字节还原为 dc5eb008…。真实 PVF 租赁专项 19 项全过（含既有 `TestRealScriptPVFCurrentRentalCatalog`，0 跳过）；全量 dnfbridge 3309 通过 / 0 失败 / 536 环境跳过；gofmt、go list、`internal/modules/dnf/...`、`cmd/server/control`、scoped vet 与 Windows 服务器构建全部通过。原始日志见 rental-evidence/。
- 边界：未部署、未推送远端、未合并主线，未改客户端/PVF/生产玩家数据，未使用游戏 UI。原生游戏内“购买幸运星后金币立即刷新”仍未验收，不得据此关闭现场问题；`rental_state.go` 中“旧登录刷新写入合成金币行曾污染客户端物品对象”的历史注释与本条的区别（本条只在提交后的独立 op14 更新里发，不改登录/op985 组合）仍待原生复验确认。

## 2026-10-15 — DOC029 金币持有上限重复升级（已提交档位重新发布）

- 现场：首次点击扣费后，再次点击不再扣费、上限显示也不动。当前 v101 客户端（DNF.exe sha256 1ac1264553f8f762e64f2c637c4c26d3bf5ff0fdc60a090ca8b077d452fd4331）的金币持有上限完全由客户端档位字节 [0x057B7008] 决定：0x032EA592 在档位 0 时取 0x17D78400（4亿），否则取档位表 0x4CF88D8（0x4CF88DC..E8 = 5/6/7/8亿）。
- 根因（已由父代理现场复核）：该字节只有两个写入点——op978 回包处理器 0x01FD4B5A（class1 成功位 + 1 字节档位 + u32 结果；仅 flag!=0 且 result==0 才写入）与档位通知 op947 处理器 0x0203AE86（读 1 字节）。当前 v101 客户端在 COMMAND 分发前有接收门控 0x06E83610：`bt dword [0x6E92300], opcode`（>0x5C2 直接 0），位为 0 的 opcode 在 0x02523C96 被丢弃，即使已登记处理器也不会分发。位图解码：op978 = bit2@0x6E9237A(0x00)、op947 = bit3@0x6E92376(0x80) 均为 0（父代理复核确认 947=0、978=0），而 op14 = bit6@0x6E92301(0xFF) 为 1。因此客户端档位永远是 0：上限不变，每次点击都发同一档位，服务端按重复请求拒绝（不扣费）；钱包行 op14 被接收，所以现场只看到“首次扣金币”。重登也不恢复，因为登录档位投影同样走 op947。
- 服务端修复：新增 publishCurrentCarryGoldStage，在一次升级请求被处理完后（成功，以及档位已知的拒绝：重复请求、金币不足）都通过 op947 重新发布已提交档位（A21 参考 GoldLimitHandler.cs 同样每次都发档位通知），回包内容与顺序保持“op978 回包 → op947 档位 → 钱包行 op14”；成功路径的扣费、档位推进与事务回滚语义不变，重复请求仍不二次扣费。仅涉及金币上限代码。
- 验证（证据目录 gold-verification-20261015，v1 日志不覆盖）：新增 DOC029 回归在基线 c0e275a 为红（doc029-red.log：TestCarryGoldDoc029CommittedTierIsPublishedAfterUpgrade 与 TestCarryGoldDoc029RepeatUpgradeAfterLostReply 均 FAIL，原 4 个 carrygold 用例仍 PASS），修复后为绿（doc029-green.log：6/6 PASS）；另附 v2 门控证据 gate-opcode-bitmap.json 与 REPORT-v2-gate.md；gofmt 无输出、go list/vet、cmd/server/control、internal/modules/dnf/... 全通过，真实 PVF 专项见同目录记录。
- 边界：客户端位图属父代理“统一客户端白名单候选”，父代理将制作 v2 额外放行 947/978；本次未执行/未修改客户端二进制，也未新增/更换服务端其它 opcode，未改动完整物品表、锻造/附魔/锁定代码，未部署、未推送远端、未改动生产玩家数据。

## 2026-10-14 — 服务端与本地 GM 边界验证

- 干净检出验证：核心9293通过节点、858跳过、零失败；固定真实PVF 143个指定回归根全过（656通过节点、2未选中包）；gofmt/list/vet、Windows/Linux构建通过。BAT预检缺源码包未含的deploy/assets/channel_info.etc，未计部署通过。
- 原GM文件保留本地，独立本地工具不进入在线仓库；CI边界正反对照通过。

## 2026-10-14 — 按用户要求将在线仓库与本地 GM 工具分离

- 在线版本移除 GM Web 工具包及嵌入服务端的启动/停止绑定；本地原始 GM 文件和用户正在使用的工程全部保留。隔离清理树采用取消 Git 跟踪，未删除本地工具源码。
- 控制器不再生成 GM 环境配置，doctor 不再要求 GM 端口随游戏服务器启动。旧 gmListen 配置兼容读取后忽略；服务端游戏及通用管理接口保持原职责。
- 添加忽略规则、仓库边界检查及协作规范，防止后续 GM 更新重新进入在线仓库。验证与在线清理结果后补。

## 2026-10-14 — Keep skill-setting diagnostics consistent with committed state

- Only log skill/CD lists as cleared after settings persistence succeeds. A regression injects a repository save failure, confirms the previous frame survives, and rejects a misleading cleared event; it fails before the guard and passes afterward. Save/replay bytes and error handling remain unchanged.
- Full validation at 556a2fb: gofmt/list/vet and Windows/Linux builds pass; core records 9323 passed nodes, 859 skipped and zero failures. All 146 required real-PVF roots pass (657 passed nodes, two packages without selected tests). BAT preflight remains blocked by absent source-worktree channel_info.etc. Parent independently rechecked all 11 current-client layout probes. Native skill-setting acceptance remains open.

## 2026-10-15 — 修复双技能加点页及技能类型扩展券

- 第二套技能加点默认锁定，商城821技能类型扩展券按实际PVF身份和价格购买后即时开启，重复购买拒绝；购买资格、第二页初始配置及点券在同一事务保存。历史默认页码不作已购买凭据，已有第一页加点保留。
- 两页独立保存技能等级、SP/TP及快捷技能位置，切换原子更新当前活动配置和页码，冷却共享。加点、重置及快捷技能修改检查已解锁且属于当前页，重置保留另一页；升级点数与转职觉醒同步两页。
- 新增技能页状态及非活动页技能两张关系表，启动增量建表；旧页码镜像支持-1。当前角色mode0按购买资格发送0xff或0/1，购买后用当前版角色信息及双页op19刷新，不修改客户端或PVF。
- 回归覆盖未购买拒绝、真实PVF扩展券购买与重复拒绝、独立配置切换/冷读取、点数同步、重置隔离及事务失败回滚。用户已在本地配套实测确认修复效果；原生验收范围以实际反馈为准。

## 2026-10-14 — 技能选项字段在当前客户端离线复核与收敛（装备席）

- 用 scripts/verify_client_skill_option_words.py 对当前客户端 DNF.exe（SHA256 8bf1d39df74c6ca6fdbea30a95e055398aad9729305c7ea510ce01af2141e387，2026-10-14 07:36）离线复核 43 批字段结论：11 个相关函数仍在同一 VA，保留的整函数字节切片逐字节相同（CD 恢复 2176B、390 读取端 1616B、锁表导出 464B、CD 导出 672B、447 写出端 672B 等），raw/bank 边界 0x7F/0x80、索引算法 (bank+2)*64+entry、blob 基址 +0x1CC、帧长恰好 0x200、CD 列表 2..41（每树 20 项、步长 20）均未变，all_ok=True。旧参考 EXE 为 SHA256 bb42898c…（111763456B）；不再把旧哈希当作现状证据。
- 收敛注释与文档：character_option_words.go 头部压缩为字段表 + 复核结论；删除未被使用的 empty() / summary() 方法与两个未用常量；Docs/D33_SKILL_OPTION_WORDS_20261014.md 去掉重复叙述并补上复核结果与日志负担说明。
- 仅新增只读复核脚本 scripts/verify_client_skill_option_words.py（capstone 目录与客户端路径全部参数化）；保存与回放字节、保存门禁、账号档闸门均未改。
- 未部署、未推送；未改其它工作树 / 用户 DOCX / 原台账 / 生产资产，未改装备或任务领域。DOC-053 的时装右键掉线与重选后武器栏空白由用户确认已修（仅未涂色），本席不再排查；用户没有说明修复位于客户端还是服务端。
## 2026-10-14 — 技能选项（锁定 / CD 提示）字段取证与保存回放可观测性（装备席）

- 按固定客户端 EXE（SHA256 bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531）已证字段表记录每角色选项 blob 的编码：技能锁表 = word 128..191（树0）与 192..255（树1），每树 64 项，值 = 客户端技能槽号，空项 0xFFFF，写入 0x39A6BA0 / 读取 0x39A6C00，导出 0x2255AA0、恢复 0x2255880；CD 提示列表 = word 2..21（树0）与 22..41（树1），每树 20 项，导出 0x22555F0、恢复 0x2254FC0。攻击类型（物理/魔法）仍无字段或包证据，不实现，也不用其它字段代替。
- 新增只读解码 character_option_words.go，并把 op447 保存与 class0/390 回放事件接上字段证据：previous_saved、payload_sha256、option_words_decoded、changed_word_count、changed_word_indexes、lock_tree0/1_slots、cd_tree0/1_slots、lock_slot_count、cd_slot_count、option_word_encoding；另新增具名事件 game-character-option-skill-lists-cleared（上一份存档的锁定 / CD 列表在新存档里消失时）。落库与回放逐字节不变，保存门禁不变。
- 用 45 批原生取证的真实锁定帧（角色 31 点锁定后落库的 512B，sha256 4fe7d38648f9d5a19c372a73c684c7d66bd473dc9db6245da8bc0e9a3d7e297b，word128=5）做固定 fixture，新增六个回归：真实帧解码、四张列表按树且槽号 0 合法、非 512 一律不认、word 差异、清空点名，以及真实 op4 选角 + 真实 447 分发 + 冷进入与同连接再选角的 390 逐字节回放与日志字段。
- 旧失败复现：临时把角色选项回放改回旧的一次性连接闸门后，返回选角、同角色重进与 A→B/B→A 三组均 390=-1（19 仍为 30），本轮新增端到端用例同样 RED；按 SHA256 校验还原后全部 GREEN（-run TestCharacterOption）。未跑全套重测试。
- DOC-060 / DOC-114 / DOC-053 的三条症状未关闭：锁定与 CD 提示的数据载体在基线上已整帧存取并按每次选角回放（45 批漏 390 的连接闸门已由 31e2b51 修复），但原生 GUI 验收、攻击类型字段与冷连接时序仍开放。现场 A/B 步骤见 Docs/D33_SKILL_OPTION_WORDS_20261014.md。
- 只在独立工作树 work/bugs-round2-equipment（基线 5fc6807）改动；未部署、未推送、未改其它工作树 / 用户 DOCX / 原台账 / 生产资产，也未改装备或任务领域。
## 2026-10-14 — Correct equipment-lock success ACK framing

- Current-client class1 op267/268/269 expects a success byte before list/slot (unlock also carries remaining seconds). The itemlock owner responses omitted that byte, so main-list zero selected the failure branch and other lists shifted the location fields. Add the success prefix only to ACKs; preserve the class0/op252 location-only notification and existing persistence semantics.
- Add a bridge regression covering all four supported list types and three operations through the real handler, repository and send path. All twelve cases fail on the old bytes. Native validation and the previously unsupported equipped-list owner remain open; see Docs/BUGS_20261014_EQUIPMENT_LOCK_ACK.md.

## 2026-10-14 — Clear position-lock bytes on equipment transitions

- Parent review found that deleting only the sort_lock Extra key left raw row +0x60 set on equipped items, and historical equipped records could return the old key on unequip. Clear both the key and the current 0x77 row byte in both directions; clear the worn-row projection independently for existing records. Preserve shorter legacy row layouts and unrelated attributes.
- Two new regressions fail on 56d101d and pass after the fix, covering equip/unequip conversion, historical worn rows, projection and input immutability. Full final validation follows this source change; earlier 56d101d results do not validate it.

## 2026-10-14 — DOC-067 位置锁：真实桥接端到端与失败回包边界（sort-lock 席，复核轮）

- 新增 sort_lock_bridge_integration_test.go：legacy cmd=1/op713 经 handleGameStreamPacket → handleAlignedGameCommand → Owner.ApplySortLock 落库，回包**只有**一条 class1/713 `01 00 0e 00`；随后整表/冷登录 op13 容器页里该槽 `0x60=1`，同一页的钱包行（0:0 金币 123456）、账号共享晶块行（0:359 = 5000）、品级种子 999999998、强化字节 6、属性/附魔块逐项不变；714 解锁后增量 op14 单行 `0x60=0` 且这些字段仍在。
- 复核对齐两个帧形状：桥的 upper 头是 16 字节（splitAllCurrentGameServerUpperPackets 按 13 字节旧头切帧会把头部尾 3 字节并进 Body，测试改用 splitGameServerUpperPacketWithHeader + GameServerUpperHeaderSize16）；list 0 整表 op13 容器页头 = listType + u16 容器档位 + u16 行数（5 字节，行 0x77），取页时需按长度校验与同 msg id 的容器状态帧区分。
- 失败回包边界取证（回答「不回包会不会卡住」）：0x1faee80/0x1faeee0 的失败分支只把 error 字节读进栈上局部（[ebp+0xf]）后返回，该局部此后无任何读取；两个处理器没有全局/UI 写、没有 pending/超时标记；上行 713 分支（0x32d3767）写体前只调一次 UI helper 0x1a685c0(1,3,0xd)，与回包无回调关联。因此失败不回包不会让客户端停在等待状态，最坏只是没有错误提示；90CN 也没有已证 sort-lock 错误码枚举（A21 的 17/19/21/22 属 267/268/269 资产锁），保持 fail-closed，不臆造 `0 + error`。
- 空槽判据按仓储契约收紧：ApplySortLock 由原先 `found && ItemID==0 && Count>0` 会放行的写法改为 `!found || ItemID <= 0`（与 CurrentInventoryItemProjects / item_stack_contract.go 同一口径）；新增用例钉住「无物品行（ItemID=0、Count>0）必须按空槽拒绝」与「永久装扮（ItemID>0、Count==0）必须允许并投影 0x60=1」。
- 回归：./internal/modules/dnf/...、./internal/services/dnfbridge、./cmd/server/control 全包通过；gofmt/go vet 干净。未部署、未推送、未改用户 DOCX/原台账/生产资产。
## 2026-10-14 — DOC-067 物品栏位置锁定（位置锁持久化 / 整理跳过 / 行投影）

- 当前客户端（DNF.exe SHA256 8bf1d39df74c6ca6fdbea30a95e055398aad9729305c7ea510ce01af2141e387）已证：C2S 713 = 置 1、714 = 置 0（不是 toggle），体 = u8 list + u16 slot；客户端收到 713/714 分别写 item+0x2aa=1/0，所以成功回包必须用同一 opcode 的 class1 `u8 1 + u8 list + u16 slot`；旧 C# 的 toggle 与 714/715 方向在本客户端会翻反，未照搬。
- 0x77 物品行的 0x60 字节经 0x1fd824c（读 row+0x60）→ 0x1fd8c38（struct+0xaa）→ 0x1d45fcc（item+0x2aa）链进入客户端物品对象；本席复核 op13 处理器 0x2024cf0 与 op14 处理器 0x2025a90 都用同一 0x77 行缓冲调用同一组行解析 helper（0x242d4a0 调用点 0x20257e3 / 0x2025c76）与已证解析器 0x1fd80e0 一致。
- 新增 internal/modules/dnf/inventory/sort_lock.go：sort_lock Extra 键、支持列表/账号共享格边界、Sort 跳过锁定物品与被锁槽位、搬运携带规则（穿戴/跨 list/主背包快捷槽 3..8 清锁；个人金库内部与同分段保留）与 Owner.ApplySortLock（校验→幂等→事务落库）。handler 接入 713/714 并只在 durable 提交后回同 opcode class1 成功包；失败一律不回包（90CN 错误码枚举无证据，不臆造）。
- 行投影在 dnfbridge 显式写 0x60 = 0/1（setByte 会跳过 0，必须直接写才能清掉 raw 脏值，保证重登/整表刷新恢复锁定图标）；穿戴路径进场时删除位置锁，装备资产锁 267/268/269 与宠物/账号仓库未改动。
- 新增 inventory/sort_lock_test.go 与 dnfbridge/sort_lock_row_projection_test.go：落库与回包形状、其他 Extra 键不被清、幂等不重写、非法 list/账号共享槽/空槽/长度不符不回包、跨账号拒绝、落库失败无半截状态、整理跳过、搬运规则矩阵、行字节显式 0/1。
- 旧代码复现：把 handler/owner/aligned/行投影/account_cargo_sort 临时还原到 5fc6807 后，713 未登记无回包、整理搬走锁定物品、快捷槽与跨容器搬运保留锁、行字节保留脏值 1；按 SHA256 校验还原后以上用例全部 GREEN，`./internal/modules/dnf/...`、`./internal/services/dnfbridge`、`./cmd/server/control` 全包通过，gofmt/go vet 通过。
- 未部署、未推送、未改用户 DOCX/原台账/生产资产；原生 GUI 验收与失败回包错误码仍开放。详见 Docs/D33_SORT_LOCK_20261014.md。
## 2026-10-14 — 锻造候选主代理完整验证

- 源码4e8a429：核心9378通过节点、860跳过、零失败；固定真实PVF的160个指定回归根全部通过（719通过节点、2未选中包）；gofmt/list/vet及Windows/Linux构建通过。BAT预检因源码树缺少deploy/assets/channel_info.etc失败，未部署。
- 普通锻造、便携炉、券、失败回包、事务回滚、装备等级窗口均纳入验证；DOC-087/092原生验收保持开放。

## 2026-10-14 — 锻造审查补充：耗材等级窗口强制 + 438 失败码逐码核对（forging 席续作）

- 关闭边界：耗材的 `[check usable itemlevel]` 不再忽略。真档实测 315 个 stackable 带这一段，而 125 张锻造券与 3 张幸运锻造券**都不带**（交集 0）；便携式锻造炉 10164035 = `1 90`、10090052 = `1 85`、10094127 = `1 86`；同族 1阶..5阶强化器 = 1..29 / 30..39 / 40..49 / 50..59 / 60..69，客户端物品名（`凯丽的强化器 (Lv1~90)` / `一次性增幅器 (Lv1~90)`）逐字写着同一窗口。
- 语义按厂商同族口径：窗口比的是**目标装备的 `[minimum level]`**（`ItemUpgradeRestriction.AllowsItemLevel` 的入参就是 `targetMetadata.MinimumLevel`，不是 `[grade]`、也不是角色等级）。在域事务内、掷点与扣料之前强制；失败按客户端"限制"码 19 拒绝且材料/等级不动；目标 PVF 没写 `[minimum level]` 而窗口写了 = 失败关闭。
- 该选择在真档上可区分：101040493 的 `[minimum level]` = 90、`[grade]` = 96 ⇒ 1..90 的炉子边界放行、1..85 的炉子超限拒绝（新增真档用例，旧红新绿）。
- 438 失败回调 0x2483b60 逐码核对（本机读表，脚本/原始输出在 E:/90/_forging-scratch/forgeerr/）：先无条件 `push 0/0/0/4; call 0x12030a0` 复位锻造 UI，再读码；> 0xd6 走 default（文案键 0x67e + 码），≤ 0xd6 走 0x2483d38 字节索引表 → 0x2483d0c 的 11 槽跳转表。实测 4→槽1/0x67a、7→槽2/0x4e20、10→槽3/0x67c、13 与 95→槽4/0x676、17→槽5/0x8904、19→槽6/0x6a1a、22→槽7/0x67d、175→槽8/0x5338、214→槽9/0xa4f8、0/118→槽0 不显示、其余 203 码 → 槽10。本实现用的 7 个码**全部命中专用槽**。
- 删除未证实的泛化断言（"未列出的码也会落到通用文案"）：0/118 根本不显示，且显示前还有闸门 0x35ca060 可跳过文案；能保证的只有复位 UI 无条件先执行。也不再称厂商同名 enum 为"当前客户端官方枚举"，改述为"厂商同族实现 + 本 EXE 专用槽实测覆盖"。
- 验证：真档 `TestRealPVFForgingCommandAppliesOrdinaryPortableAndTicket`（含窗口边界放行与 1..85 超限 0+19）通过；合成档新增窗口回归（边界/超限/下界/未写 minimum level 失败关闭/无窗口放行）；core 套件 0 失败。未部署、未推送。
## 2026-10-14 — DOC-087/092 锻造（op438）域事务与桥接回包落地（forging 席，续 615ba52）

- 桥接：438 在 `Registry.Route` 之前用 `forging_codec.go` 解包（**畸形帧也回失败体**，客户端因此必定走完"复位锻造 UI"的回调）；领域结果组 class1 438（成功/掷点失败 13 字节、校验/存储失败 `0+u8 error`），ACK 先于逐槽 op14 增量。新增 `forging_pvf.go`（`etc/upgrade_separate.etc` 表 + 材料三种身份 + 目标武器/品级/稀有度/耐久），`dnfenum/aligned.go` 登记 438。
- 域：`inventory/owner_separate_upgrade.go` 在同一个角色物品事务里完成材料消耗 + 锻造等级写入（**0x77 行 0x57 + Extra `byte_57`/`value_57`**），覆盖主背包 list 0 与穿戴 list 3；校验目标归属 / equipment / 模板一致 / 资产锁 / 武器 / 满耐久 / 等级上限 / 品级材料 / 稀有度 / 券白名单 / additional 起始等级；错误码取厂商 `SeparateUpgradeResult.Error*`（4/7/13/17/19/22/95）。校验失败与存储失败都回失败体让 UI 结束，事务回滚保证资产不动。
- 真档语义：便携式锻造炉 10164035 只消耗炉子 1 个、**不消耗 3326**；锻造券 125 件的 `fixed`/`additional` 档 + 券自带 `[reinforcement usable item list]` 白名单；幸运锻造券 10007408 失败**不掉级**。普通锻造材料 = `max(1, int(BaseCount×rarityWeight×levelMaterialWeight + 1e-7))`。
- 测试：合成 + 真档（PIN 369d072b…）全链路 —— 普通锻造消耗 67 个强烈的气息、耐久门槛 `0+7`、便携炉 3→2、+5 券 0→5、白名单拒绝 `0+4`；另有存储失败回滚、额外属性保留、12 条校验拒绝与畸形帧回包。分节与真档命令见 `Docs/D33_FORGING_20261014.md` §4。
- 验证：`gofmt -l` 干净；`go list` / `go vet`（control、dnf90、doctor、app/dnf90、dnfbridge）/ `go build` 通过；core 套件（`./internal/modules/dnf/...` + `./internal/services/dnfbridge` + `./cmd/server/control`）0 失败。
- 未完成：原生 GUI 验收（锻造动画 / 窗口关闭 / 锻造器）仍待主代理；`[check usable itemlevel]` 未在服务端强制（与既有 op50 portable 档口径一致）。未部署、未推送、未改 PVF/客户端/生产库/用户文档。

## 2026-10-14 — DOC-087/092 锻造 438 线协议复核修正与真档取证（forging 席）

- 复核并修正主代理留下的 forging_codec.go / forging_codec_test.go：gofmt；样例注释改为实际 hex 对应的 item **101011368**（原注释 101010856 与 hex 不符）；去掉**没有证据**的 confirmation ≤ 2 限制（发送端把它当函数参数传入，全 EXE 扫描找不到直接调用点，取值域不可证；误拒会让客户端拿不到回包），保留有证据的结构校验；测试补成功/失败两套 ACK golden（含穿戴目标 list 3）、截断与畸形帧拒绝、confirmation 任意字节必须解析通过。
- 线协议取证：C2S 438 写序 = u8 list / u16 slot / u32 itemTemplateID / u16 materialSlot / u32 长度前缀名字 / u8 confirmation；class1 438 回包注册 0x447a740 → 回调 0x24836b0，成功体 13 字节读取顺序 = u16 materialSlot / u32 materialRemaining / u8 oldLevel / u8 outcome / u8 newLevel / u8 targetList / u16 targetSlot（outcome 0=成功、1=掷点失败）；失败回调 0x2483b60 先 0x12030a0 复位锻造 UI。class0 438（0x200e590）是另一条只读 u16 通知，不得混用。
- 真档参数（同一份运行时 Script.pvf，SHA256 369d072b…）：etc/upgrade_separate.etc 的 [separate upgrade max]=8、[table]=10 档×4（第 3=10000 基成功率、第 4=材料倍率）、[item weights by grade]=三元组（grade、材料模板 3326、基础数量）、[item weights by rarity]=7 权重；参考 InventorySeparateUpgradeService.cs（普通锻造 :175-270、券 :60-160）的材料公式 max(1, BaseCount×rarityWeight×MaterialWeight)、掷点 roll<SuccessWeight、失败掉级仅当 FailureDecrementLevel>0。便携炉 10164035 的 action `[portable genuine damage upgrade upgrede]` 与「不消耗强烈的气息」已记入证据文档。
- 已证持久化落点：锻造等级 = 0x77 行 0x57 + Extra byte_57/value_57（GM 工具已有读写与真档测试；alignedcmd 已明确便携炉不属于 op50 强化族）。
- **未落地（明确分界）**：域事务（材料消耗+等级写+回滚）、桥接 438 登记/handler/op14 刷新、便携炉消耗、券参数与目标白名单、失败体 `0 + u8 error` 错误码枚举；原生 GUI 验收（动画/窗口关闭/锻造器）待主代理。证据、公式与下一步清单见 Docs/D33_FORGING_20261014.md。
- 验证：`go test -run TestCurrentForging ./internal/services/dnfbridge` 通过，gofmt 干净；固定 PVF 与失败资产未改动；未部署、未推送、未改用户 DOCX/原台账/生产资产。
## 2026-10-14 — Reconcile MR !4 with the merged gameplay fixes

- Merge master 61036f9 into the independent D33 draft, retaining both histories and all existing changelog evidence. Normalize this changelog's line endings to avoid duplicating shared history during reconciliation.
- Preserve independent command-result snapshots for authoritative list refreshes and committed stackable-consumption receipts together; neither result may alias the handler's mutable storage. Add a regression covering handler reuse and consumer mutation with both fields populated.
- Keep exact-instance removal for equipment recipe upgrades, then consume remaining materials through the shared account-warehouse-aware recipe consumer. Preserve transaction rollback and inherited equipment attributes.
- Validate the resolved source: gofmt, go list, go vet, Windows and Linux builds pass; core tests record 9778 passed nodes, 876 skipped nodes and zero failures. The pinned real-PVF run passes all 137 required regression roots, recording 648 passed nodes, two packages with no selected tests and zero failures.
- The isolated BAT preflight stops because deploy/assets/channel_info.etc is absent from the source archive. This run does not establish deployment or native-client acceptance; retain the existing draft and open native validation items.

## 2026-10-14 — 已部署候选的原生验收与独立草稿同步（920）

- 919组合源完成9479通过节点、854环境跳过、0失败；固定PVF473通过、0跳过、0失败，gofmt/list/vet、双平台构建和实际BAT预检通过。Linux候选22a67ab64a0fb8b01b9a1685f22145eb44a16b585efb0080b919d2780a49ab56已部署，健康检查与部署前后完整采集资产比对通过。本条及验收文档只更新证据，所有Go文件与919验证快照逐字节相同。
- P38三种原生操作及新进程冷登录通过：主副手实例、强化、品级和锻造2/4与持久化结果对应。两张陈旧绑定截图已剔除，不作为通过证据。锻造显示成立依赖918 mode1 state120补齐，不把早先单独op14补发误写为已解决。
- 四件90A→90B按真实设计图完成升级，逐次与累计资产严格比对、完整Extra和119字节实例保留通过。新进程中鞋100%、腰54%、肩100%、裤49%正常；上衣仍为90A对照100%，两孔为空，已有徽章继承及第五件升级尚待验收。
- 自有角色账号仓库合并和单件拆分取出后，未重选角色，金币6000992、共享晶块1000及五件品级持续正确。两次成功ACK之后均有权威列表发送事件；往返资产除两处更新时间外逐项相同，对照角色保持。此前918金币显示0的失败截图及纠错均保留。P61/P68请求空格但自动合并另一格的原生端点覆盖仍未完成。
- P33/P39/追加9城镇跨角色持久卸武器与P56空白导航的客户端修复已另行完成验证并发布client-v99-d33-912；客户端补丁不在本服务端MR。上衣徽章与第五次升级、P61/P68重定向端点、P23合法已开启团本一阶段原生验收仍保留开放项；普通卢克解锁仍取消，MR !2继续由作者处理。
- 原定时迁移对话已停止，全部电脑操作由主代理承担。三个本轮离线子代理已关闭。独立MR保持Draft，尚不合并；当前测试通过不代表尚未覆盖的原生场景已完成。详见Docs/D33_NATIVE_VALIDATION_20261014.md。

## 2026-10-14 — 金库与整理后的钱包、品级刷新候选（919，待验证）

- 918已部署。P38原生单主手、单副手、双武器中心按钮交换及完整新进程冷登录均完成，强化、品级、锻造2/4与实例保存对应；原MR !2继续由作者处理。
- 自有106原生金库合并及单次取出后金币显示为0、背包装备品级变成数量1对应的低品级；只读SQL仍保留6000992金币及原品级种子。重新选角后金币和品级恢复，明确是操作后的刷新错误。918金库截图文字中“金币正常”已纠正，P61/P68不据此关闭。
- 对物品移动实际发出的主背包整容器刷新，标记由桥侧复用会话权威投影，保留钱包、共享材料、品级和孔位。主背包整理沿用相同处理；ACK端点与先后顺序保持。读取权威钱包失败时复用现有跳过刷新机制，不发缺钱包快照。
- 新增真实inventory handler到bridge下行的五场景回归，覆盖账号金库直接合并、自动合并、拆分取出、个人金库自动合并与主背包整理；已在旧代码重现缺钱包、品级被写成1、孔位丢失。候选完整验证与部署后原生复验尚待完成，不能宣称已修复上线。

## 2026-10-14 — 原生附魔及四件90B升级验收；冷登录锻造候选（918，待完整验证）

- 已部署916的原生附魔复验完成：自有106四次附魔与四次打孔均收到成功回包，金币6000992、共享金色晶块5000及肩部最上级100%显示保留。完整SQL核对附魔、双空孔与原实例属性；在线奖励490006576自动增加1件另有事件日志，不算附魔资产差异。
- 主代理以原生设计图完成鞋、腰、肩、裤90A→90B四次升级，每次op25成功ACK及committed日志、逐次完整资产快照均保存。界面剩余精炼时空石10、魔岩石34、金色晶块1000；四件强化、增幅、附魔与双空孔显示已观察。精确实例差分仍在校验，上衣与已有徽章继承样本、冷重登仍待验，不能据空孔证明镶嵌徽章继承。
- 916中心按钮交换后的第二次完整冷重登显示主手+7/100%、副手+9/26%，槽位与SQL一致；锻造2/4仍未显示。op14补发不作为修复通过证据。
- 为mode1装备创建行的state120增加已归一raw[0x57]回退，保留显式mode1_state_120/state_120（包括0）的优先级。通过真实仓储到mode1生产构建入口重现旧代码三组锻造4/2均投影0的失败，新增八个回归子用例限制只有两处锻造字节改变；修复后的测试、组合检查及原生显示仍须独立完成。
- 当前线上仍916 b5954891…，918候选未部署或同步MR；MR !2保持作者处理，电脑只由主代理操作。

## 2026-10-14 — 冷重登主副手槽位与锻造交付、附魔钱包失败边界（916）

- 自有角色119实机中心按钮已完成单主手、单副手及两把不同属性光剑交换；SQL保存了交换后的槽位和实例，但完整退出重登后UI按旧槽位标记还原，锻造2/4均未显示。此问题仍待修复后原生复验，不能凭SQL一致关闭。
- 主副手12/24互换时同步当前槽位标记、0x77原始行槽位及已有十六进制镜像，保留其余实例数据和事务出口。桥侧对有运行时移动标记的12/24行优先采用持久化SlotIndex，使已受影响存档无需直接改库即可还原；未移动的初始装备保留旧槽位转换。
- 冷登录场景完成并发送装备容器后，沿用已有op14行更新补发带0x57..0x59尾部状态的装备；只读权威仓储，成功发送后记录事件，可用DNFBRIDGE_LOGIN_EQUIPMENT_TAIL_REASSERT=off回退。主代理添加真实登录边界回归，保留未接入补发时的失败证据；实际锻造显示仍须客户端验收。
- 附魔完整主背包刷新必须包含同次构建的权威钱包行；角色金币读取失败或缺失时跳过该刷新，保留已提交附魔的ACK。未标记操作保留既有行为。
- 主代理完成槽位、冷登录边界和钱包读取失败的先失败后通过检查，正在执行组合完整验证。915候选已单独完整验证并保留备份，但未部署；916检查不沿用915结果。MR !2继续由作者处理。

## 2026-10-14 — 附魔成功后的主背包完整刷新（915，待部署验收）

- 自有角色106真实附魔后，客户端金币6000992显示为0、金色小晶块5000显示为0、上衣品级100%显示为12%；只读数据库确认资产和品级种子均未改变。op272成功前置的class0/op13整表刷新只使用角色物品快照，遗漏钱包和账号共享格，并将装备品级编码为数量1。
- 成功的非宠物附魔结果标明需要完整主背包投影；桥侧在原响应位置复用会话物品列表构建器，沿用原信封及刷新先于ACK的顺序，保留钱包、账号共享格和装备实例字段。投影不可用则跳过该刷新并保留已提交操作的ACK。其他未标记操作和宠物附魔路径保持原行为。
- 主代理保留代理原始交付并修正测试字符串及未使用导入，先在旧发送逻辑上重现三项失败，再确认同组五项回归通过。完整检查和真实客户端复验尚未完成，本条不代表已部署或已关闭问题；原MR !2仍由作者处理。

## 2026-10-13 — D33 掌握技能审计按职业判读（871）

- 保留 `[required job skill]` 的职业标签与技能编号配对，仅用本次技能页所属职业的要求判读。技能编号在不同职业中复用，不能将其他职业的同编号技能当作本职业的掌握技能。
- 固定 PVF 的原代码回归已重现两处误判：job11 仅下发33且缺少本职业要求95、job0 仅下发95且缺少本职业要求33，均曾错误记录为 all_requirements_served。新增真实 PVF 跨职业回归，并保留本职业掌握已下发的成功对照。
- 不支持的职业记 skipped；无法完整解析的职业/技能配对记为未完成审计，避免把未知状态记录为全部满足。此改动仅修正技能页下发处的日志判读，不改变协议字节、仓储、卸装策略或装备规则，不能作为 P33 城镇跨角色卸武器已修复的证据。
- 此节对应本地独立源码后续修正；截至开始验证时，MR !4 仍为已验证提交 0a5bf3c4，本地871尚未提交、部署或原生验收。完整验证及同步结果另行记录，旧 CI 数字不覆盖本地改动。原 MR !2 留给作者自行修复。

## 2026-10-13 — D33 独立已知问题修复候选（待原生验收）

- 基于主线 f30f50a5，仅纳入设计图属性继承升级、剑豪中心按钮、金库自动堆叠实际落点 ACK 和卢克奖励画面防重修复。
- 设计图确定性升级覆盖 90 张配方及全部 25 张卢克 90B 防具，其他 197 张属性继承配方仍保留原有拒绝条件；原生属性、扣料及重登效果待验。
- 原 MR !2 留给作者自行修复；本候选不继承其账号工具、主线/转职、幸运星或金币罐改动。历史组合候选测试不作为此独立版本的验证结果，主代理重新验证。
- P56 空白网页导航仍在客户端定位中，本次没有客户端 EXE 或资源改动；香港 2026-10-14 02:00 后再执行游戏操作。
- 登记 2026-10-13 20:40 高优先级城镇跨角色卸武器反馈，关联 P33/P39/追加9；新增掌握技能武器触发条件；用户确认受影响角色重登后仍未装备，按持久化状态变化优先定位，尚未修复。
- 草稿源码尚未合并、部署或发布客户端 Release。
## 2026-10-15 — 同级合成池缺失时按PVF概率重建来源模板

- 仅在同级合法候选为空时，使用启动缓存中一致且合格的来源模板补齐同级分支，保留PVF原等级权重与单件权重。抽中同级重新生成同ID新装备，材料照扣，旧强化、附魔、耐久不继承；有正常同级池时继续从原池随机，不改粉装与圣物共池规则。
- 来源未加载、定义不一致或被排除时仍拒绝；PVF显式零同级权重不凭空加分支，缺合法升级候选仍只产出合法同级装备。新增粉装/圣物/传说的概率边界、配置变更、缓存准入和同ID新实例扣材/库存包顺序回归；未修改客户端或PVF。
- 本轮重新执行两个源码环境的规定完整检查：本机9267 PASS、843 SKIP，独立分支9585 PASS、868 SKIP，均0 FAIL；跳过不计通过。gofmt/list/vet、Windows候选编译、真实PVF桥接通过，独立分支另通过Linux编译及仓库边界检查。独立副本BAT配套预检通过，数据库与游戏占用端口明确跳过；未安装本轮候选或重启当前游戏。

## 2026-10-15 — 按90版PVF实现装备合成与启动预载

- 新增粉装、圣物、传说的装备合成领域事务及407桥接入口。普通粉装与圣物共用品质3等级/大类池，武器、防具、首饰、特殊装备只限定大类；核对来源实例封装、锁定、期限、归属和实际开放容量，材料及新装备在账号/角色原事务中保存。
- 启动监听前从PVF预载目录和规则，请求阶段复用完整缓存。等级升级权重、特殊装备相对权重、明确排除ID、部位组及分级材料均读取PVF；仅有同级合法候选的稀疏目录按同级处理。拒绝魔法封印粉装生成模板，不按名称旧字或Force Result Item Rule单独筛除正常装备。
- 结果先更新权威库存，再返回407结果槽；产物封装，不继承来源强化/附魔。领域和桥接回归覆盖共池双向、跨细类、事务回滚、封装拒绝、配置变动和概率区间；原生客户端结果与动画仍按实际配套验收。

## 2026-10-14 — Preserve equipment quality after sorting

- Use the same durable quality-seed precedence for inventory refreshes and login: dedicated metadata, equipment-only legacy aliases, then a matching complete current-client row. Sorting no longer replaces quality with the equipment stack count.
- Keep legacy quality aliases out of the independent item-state field. Preserve persisted item instances and stackable quantities; do not generate replacement quality or change assets.
- Regression coverage includes first/repeated sorting, bag/personal cargo, ordinary/top quality, legacy and raw-row sources, quantity isolation and invalid/foreign raw data. The new handler regression fails before the repair and passes afterward. Native in-game quality display remains pending acceptance.
## 2026-10-15 — 新生成魔法封印紫粉装备初始化交易封装

- 装备目录缓存PVF的[rarity]和[random option]。保留原[attach type]=[sealing]规则；对品质2/3、[random option]>0且[attach type]=[free]的新装备补齐seal_flag=1，读取实际模板字段，不按名称或物品ID写名单。普通白蓝装、非魔封自由模板及其他绑定类型不套用此特例；不改产出池或抽取概率。
- 常规新装备创建与翻牌沿用现有0x77库存行0x0D字段，礼盒和魔盒奖励解析同步采用相同条件，保留原有非装备封装语义。首次穿戴按既有流程永久解封，库存投影不重新封装旧实例；系统模板邮件仅在原有来源和无旧实例证据的边界内初始化，玩家邮件及明确解封附件保持。
- 新回归在旧实现复现魔封紫、魔封粉及另一组PVF正值配置的封装缺失。真实PVF以现场密制乌金护腿100130169核对免费/付费/双倍/黑钻/锦标赛、正常入包/快捷栏回落/满包转邮件领取；原普通封装紫装及现场白蓝装保持。固定候选仅验证实例状态，不声明实际抽取概率；原生客户端显示与交易仍待部署后验收。
- 独立分支完整领域、桥接、控制器共9653 PASS、883 SKIP、0 FAIL，跳过不计通过；封装专项、真实PVF双样本翻牌专项、格式/list/vet、仓库边界、Windows候选编译及隔离BAT配套预检通过。预检明确跳过数据库和运行中端口；未安装候选、重启游戏或修改玩家物品。

## 2026-10-14 — Use current equipment definitions for ordinary clear cards

- Build a separate ordinary-card equipment pool from list-backed PVF minimum level, rarity and positive creation rate. Legacy dictionary gaps no longer substitute crafting recipes for high-level equipment; monster and abyss policies remain unchanged.
- Preserve existing rarity rolls and level windows, exclude cosmetic/special equipment, honor original/revised asset precedence and reject incomplete scans. Stream and warm the cached pool during startup to avoid a first-clear scan delay.
- Regression coverage reproduces the former recipe fallback, checks high-level candidates, weights, level limits, special/disabled items and failed scans. Optional real-PVF sampling verifies varied equipment at low and high levels. Native card display/claim remains pending acceptance.
## 2026-10-14 — Preserve legendary souls in equipment disassembly

- Read disassembly soul rows as counted PVF records, retaining the disabled chronicle row and its constant placeholder. Legendary equipment now uses its own rarity row instead of a truncated or shifted entry.
- Reject malformed or partial soul tables before consuming equipment. Preserve the existing reward quantities, item transactions and account soul-warehouse projection; no asset or balance rewrite is required.
- Regression coverage includes the real-PVF missing-soul failure, all rarity rows, malformed tables, source-item preservation on failure and committed warehouse rewards matching the acknowledgement. Native disassembly display remains pending acceptance.
## 2026-10-14 — Restore gold-cap upgrade purchases

- Handle the current client's upgrade request using its verified 14-byte request and stage/result reply. Charge the advertised 5,000,000 gold once per stage, require level 60 and sequential stages, and stop at stage four.
- Persist the debit and purchased stage in one character asset transaction. Reject foreign owners, malformed requests, insufficient funds and corrupt stages; a duplicate request repairs the client's stage and wallet without another charge.
- Publish the saved stage during actor initialization, including zero on character changes. Existing reward, trade and auction policies are unchanged; this repair restores the purchase and the native client's cap selection.
- Regression coverage includes both transports, concurrent duplicates, transaction rollback, native failure messages and login ordering. An opt-in disposable MySQL test checks reload and rollback across the wallet and supplementary stat tables. Native UI acceptance remains pending.
## 2026-10-14 — Consume profession crafting materials from their durable owners

- Reuse the existing recipe material projector for profession crafting, including enchanter products and card-to-bead crafting. Shared crystals and souls are debited from the account warehouse; ordinary materials remain in the character bag. Historical character copies cannot substitute for account balances.
- Persist warehouse changes in the same settlement transaction as materials, products, gold and profession experience. Preserve recipe requirements, success rates and the verified request/reply layouts; refresh the committed container balances after acknowledgement.
- Matching-PVF regression reproduces the prior insufficient-material rejection and covers all eleven enchanter product recipes, batch quantities, bead metadata, wrong/locked/insufficient shared rows, foreign ownership and late transaction rollback. Existing equipment-recipe behavior is retained. Native crafting UI acceptance remains pending.
## 2026-10-14 — Charge lottery jar opening prices atomically

- Read explicit lottery gold prices independently from NPC acquisition materials. Paid hero jars no longer require their exchange token a second time. Preserve explicit opening materials and the existing material-only legacy jar policy; reject malformed prices instead of treating them as free.
- Settle gold, source jars and rewards in one account/character asset transaction. Inventory-only magic-box entrypoints reject paid jars; lottery and booster entrypoints refresh the committed wallet after success.
- Regression coverage includes matching-PVF hero and legacy jars, complete reward references, both lottery reply layouts, duplicate confirmations, insufficient gold, full bags, foreign ownership and rollback after all writes. Disposable MySQL validation covers competing opens, durable reload and rollback on a late database error. Native opening animation and reward display remain pending acceptance.
## 2026-10-14 — Cover Grandine room transitions with passive drops

- Add matching-PVF coverage for both ordinary Grandine routes, including the fifth-room exit. Before the passive-drop allocator fix, actors and drops shared identifiers and a later room was rejected for reusing an earlier drop key.
- Assert scene identity separation, allocator progress, stable room rebuilds and arrival at the boss. The previously introduced allocator repair covers these routes without additional runtime changes; native gameplay acceptance remains separate.

## 2026-10-14 — Restore Gust Zone boss-room transitions

- Accept repeated identical passive-item group definitions across maze declarations without multiplying rewards. Continue rejecting malformed or conflicting definitions.
- Reserve planned actor identifiers before allocating passive drops, and retain the highest allocator position when committing a room transition. Preserve shared party allocation and prepared drops across transport retries.
- Regression coverage includes conflicting definitions, solo/shared identifier allocation and matching-PVF traversal of both ordinary Gust Zone boss routes. Verify the existing start-map payload, distinct actor/drop identifiers, failed-write retry and exactly-once pickup. Native in-game traversal remains pending acceptance.
## 2026-10-14 — Avoid repeated friend online notifications after room loading

- Keep the last successfully delivered adventure-friend projection for each selected character generation. Unchanged room-loading and presence refreshes no longer resend the native online-notification packet; changed rows and pending invitations still refresh normally.
- New selections and connections bootstrap their lists again. Commit the projection only after successful information writes, before sending pending invitations, so a pending-list retry does not replay an online notification.
- Regression coverage includes repeated finish-loading, offline/online changes, notes, character switching, reconnects, renamed/deleted rows and transport failures. Current-client disassembly confirms the repeated-notification branch; native UI acceptance remains pending.
## 2026-10-14 — Give adventure-shop purchases their PVF use periods

- Resolve a product's relative PVF use period at purchase time before granting it. Adventure-shop potions now receive a durable expiration date instead of a zero deadline; use the same purchase clock for the grant and account settlement.
- Preserve permanent items, reject unrepresentable deadlines before charging, and retain the existing account/inventory transaction. Existing instances are not assigned invented purchase dates or renewed by later purchases.
- Regression coverage includes the matching-PVF seven-day potion, inventory refresh fields, relogin projection, separate purchase deadlines, insufficient points, full inventory, foreign ownership and late transaction rollback. Native tooltip and use acceptance remain pending.
## 2026-10-14 — Serialize dungeon lucky-star awards with account wallet changes

- Settle each lucky-star award inside the existing rental account/character transaction instead of an unprotected account read/overwrite. Concurrent awards and wallet operations now share the account-row lock.
- Validate the selected character's account ownership before writing and retain the suitability rules, cap and per-run replay guard. Transaction failures remain retryable without reporting an award or changing account metadata.
- A deterministic regression reproduces two successful awards losing one increment. Coverage also includes ownership, missing transaction support, rollback and the existing settlement replay/notification-failure paths. A disposable MySQL check verifies eight concurrent awards alongside a gold purchase, durable reload and late rollback. No protocol, schema or historical balance changes.

## 2026-10-14 — Prevent duplicate lucky stars on settlement replay

- Remember when a completed run's lucky-star award has been resolved. Exact settlement retries no longer grant additional stars or resend gain notices, including after a best-effort notification write fails.
- Resolve capped awards as well, so spending later cannot make an old clear grantable again. Repository failures remain retryable and a fresh dungeon run retains its own award.
- Regression tests reproduce repeated grants before the change and cover duplicate requests, account-save failure, notice failure, caps and unsuitable levels. This does not change lucky-star purchase protocols or establish native UI acceptance.
## 2026-10-14 — Restore equipment-based catalyst crafting

- Accept complete PVF equipment recipe predicates instead of rejecting every recipe without an explicit item-ID list. Check type, part, rarity, level range, one-item instances and sealing capability; incomplete recipes remain rejected. Sealed and previously unsealed resealable weapons follow the same PVF eligibility rule.
- Use the PVF minimum/maximum output counts and bounded per-unit maximum-output probability, retaining the existing atomic material/output settlement and reply format. Validate probability ranges and abort on random-source errors.
- Regression coverage includes matching-PVF weapon selection, material rejection, account soul costs, spent-input replay, ownership/locks, output probability boundaries and transaction rollback. Native crafting animation acceptance remains pending.

## 2026-10-14 — Charge fixed catalyst ingredients for every crafting unit

- Scale each recipe's mandatory ingredient by the completed crafting-unit count, including carried contribution. Batch crafting can no longer produce many catalysts for one unit's fixed material cost; output multipliers remain independent.
- Preserve the account/character asset transaction and committed balance refresh. Refuse insufficient stock and arithmetic overflow without consuming contribution materials, changing carry or granting products.
- Regression coverage includes the matching-PVF dimensional catalyst, single/batch account and bag costs, carry, output multipliers and rollback. Native crafting animation acceptance remains separate.
## 2026-10-14 — 按 PVF 阵营识别怪物型友方 NPC 的击杀贡献

- 友方协战贡献同时覆盖 AI 角色和原生怪物两种对象类型；怪物型贡献者必须是已宣告存活、绑定匹配地图行且被 PVF [monster team] 显式设为玩家阵营的对象。跨房跟随沿用本次副本来源房间的身份与阵营证据，不以 NPC、地图或技能 ID 放行。
- 保留当前房间目标、未知类型、敌对/未宣告/退役对象及教程边界；拒绝将友方对象本身作为此兼容路径的击杀目标。重复死亡和发送失败重试仅补发已提交的死亡通知。
- 新增针对性失败回归及匹配 PVF 的怪物型协战全链路验证：五个房间共 23 次击杀、重复请求不重复结算并进入第六房；既有 AI 协战回归保持通过。画面与真实玩家复测仍单独验收。

## 2026-10-14 — 修复友好 APC 击杀后怪物残留、房间无法清空

- 兼容当前官方客户端的死亡报文：击杀归属为 `0xffff` 时，允许携带服务端已宣告、仍存活的友好 APC 战斗贡献项。继续验证当前房间的普通怪物绑定；未知/敌对/未宣告 APC、跨房目标、教程及旧协议不因此放宽。
- 复用既有死亡、经验、掉落与清房流程；重复死亡或通知写入失败后的重试仅补发权威死亡通知，不重新结算击杀奖励。保留跨房跟随 APC 的原对象标识。
- 增加原生报文边界、拒绝路径、重复结算及传输失败回归，并用匹配 PVF 重建阿甘左协战房间，验证六只阻挡怪死亡及后续过图。游戏内画面效果仍需实机验收。

## 2026-10-14 — 同步上游传送提示与道具入包更新

- 将上游 master 的 f30f50a（含 6004771、b524132）合入本地贡献分支，保留传送前置任务不足提示、普通道具分类入包规则及玩家更新日志；保留此前任务/幸运星/金币罐等修复，不混入升级券和 GM 职业未提交候选。
- 核对 MR !2 仍开放且未合并。昨日派生库流水线 165295 的两项构建因 stuck_pending_no_matching_runners 失败，未开始代码构建；core-tests 跳过。派生库共享 Runner 已启用，但仅可见的共享 Runner 暂停且离线，当前账号无可分配 Runner。此项是 CI 执行资源阻塞，不用关闭检查或改业务代码掩盖。
- 本次独立合并源码通过修改文件 gofmt、go list、完整 DNF 领域/桥接/控制器/GM 回归及约定 vet、Windows/Linux amd64 服务端/控制器/doctor 六项构建与配套 PVF 专项。实际 BAT 预检因在线保护拒绝，未停服绕过；代码验证与云端无 Runner 阻塞分别记录。本次仅更新仓库，不部署、替换客户端、重启或改数据库。

## 2026-10-13 — 整理本地 Windows 测试的服务端修复

- 整理本地注册反馈、频道下载编码、GM 本地账号映射、翻牌后主线刷新/完成后接续、回城旧场景状态抑制、任务手册快照顺序与旧协议正常放弃。
- 纳入幸运星结算/GM 钱包读写/购买余额刷新、金币罐原子发奖与结算拾取槽位修复，并保留最新上游 NPC 商店期限合并和购买数量更新。
- 改动附领域/桥接/控制器/GM 回归及配套 PVF 专项，独立源码执行约定检查与 Windows 构建；客户端依赖、用户已验收项目及未完成边界见 `Docs/LOCAL_FIXES_20261013.md`。不包含升级券、GM 职业候选或客户端/PVF 二进制，不改运行环境或数据库。

## 2026-10-16 — 同步客户端 Release 并约束 EXE 依赖上线

- 独立同步客户端 Release：在上游 GitGud Release 与本方 GitHub Release 之间双向复制精确标签与 EXE ZIP，GitGud fork 仅参与源码贡献；保留来源、预览状态、EXE/附件完整摘要，同名冲突停止且不覆盖历史。
- 增加默认只读计划、写前来源复核、目标实际下载读回及周期工作流；凭据按目标隔离，附件不执行、不构建服务端，源码分支与运行服务保持各自发布流程。 GitHub 新标签仅经可信 CI 的短期令牌创建，GitGud 标签推送跳过流水线，避免标签内历史构建被触发。
- 明确服务端依赖 EXE 时须先 Release/镜像发布、再核验玩家实际更新源与配套 profile、最后部署服务端；未更新通道或摘要不一致阻止上线。

## 2026-10-14 — 将 GM 源码迁往独立仓库

- GM 页面、进程入口、扫描与批量福利任务及构建配置迁往 `Cec1c/DNF90GM`，该仓库可自行构建、测试和部署。
- 游戏只保留私有管理协议与自身邮件/资产事务；注册和角色初始化提取为 `signupbenefits`，原表名、注册额度快照和入账回执保持兼容，不删除 GM 数据。
- 服务端部署生成器不再创建 GM 服务；旧启用 GM 的 profile 在写入前拒绝并提示迁移，私有游戏管理密钥可以独立配置。
- 增加仓库与可执行文件依赖边界检查，验证独立 GM、持久去重、初始化事务和服务端构建。代码迁移不重启线上服务。

## 2026-10-14 — 独立部署 GM 与持久去重物品邮件

- 将 Web GM 从游戏生命周期中拆出为独立进程/镜像，自行连接 MariaDB、读取校验后的 PVF、运行福利扫描；游戏只保留鉴权的私有运行时管理接口。
- 按物品 ID 建立默认停用的邮件福利规则，预览/补发已有角色并可自动覆盖后续新角色。邮件与规则/角色创建身份回执在同一事务提交，跨进程并发、删除邮件及重启后不重复投递。账号初始点券与角色初始金币仍属于创建事务，零配置保持既有行为。
- 独立 GM 读取在线状态失败时拒绝受保护的写操作；晶块行编码失败完整回滚。Docker 单独生成受保护配置并支持只更新 GM，首次迁移需协调游戏管理接口与端口移交。
- 验证覆盖完整领域/桥接/control、GM/API、隔离 MariaDB 并发/晚失败回滚、真实 Linux GM 与无界面浏览器。发布环境/原生游戏验收单独记录；不附带私有玩家接入、资产、凭据或运行配置。

## 2026-10-14 — 建立公开客户端函数与协议索引

- 按完整模块 SHA256、架构和 RVA 建立版本化索引，首批覆盖死亡报文、金币携带上限、好友提示、报文写入封装及 DLL 导出导航；区分函数入口、内部片段和仅有导出信息的条目。
- 附代码窗口摘要、离线核对方法、固定公共提交及回归入口，明确语义和验收边界。客户端文件、原始反编译、私人日志和环境资料保持本地忽略。
- AGENTS.md 增加索引入口和维护规则。本次只补文档与索引数据，不改变协议、运行配置或部署。

## 2026-10-13 — Allow automatic code synchronization without builds

- Add an explicit upstream sync-only mode that skips source tests, database jobs and binary/image builds. Keep exact-commit, conflict, CI-review and concurrent-update guards, and identify untested synchronization in the PR and result.
- Make the standalone verification workflow manual-only. Document disabling CI/CD on runnerless GitGud forks without changing the original project's CI or SSH synchronization. These workflows do not publish releases or deploy a server.
- Real-Git regression coverage includes sync-only promotion and retained conflict/CI-review guards, alongside the existing mirror and concurrency checks.

## 2026-10-13 — Validate and integrate original upstream updates

- Separate upstream intake from bidirectional fork mirroring. A scheduled/manual workflow opens an upstream PR, validates its exact merge commit with read-only jobs, rechecks base/head/merge identities, and promotes only that tested commit using a non-force push. Conflicts, failed tests, CI changes and moving branches leave the update unmerged. Successful promotion explicitly dispatches fork synchronization; it does not deploy a server.
- Keep privileged prepare/promotion code on the trusted base; do not pass mirror or deployment secrets to incoming-code jobs. Document repository variables, PR permission, protected-branch constraints, conflict handling and the limits of source-only CI.
- Real-Git integration tests cover no-op, exact-commit promotion, merge conflicts, CI review, moved refs, racing pushes and branch divergence. All 19 synchronization tests and workflow static validation passed locally.
- Validate the upstream integration with retained local extensions: default Go suite 9,208 passing test nodes and 849 explicit skips, 12 focused actual-PVF tests, isolated account/benefit transaction checks, scoped vet, Windows build, Linux Docker build and 65 launcher checks passed. Windows local-profile preflight remains blocked by its missing bundled channel asset; this does not validate or invalidate the independent remote profile. Proprietary client/PVF resources are unchanged and native gameplay acceptance remains separate.

## 2026-10-13 — Count merged NPC purchases by this purchase's quantity

- Own normal QA account10071/character108 bought two talismans490010214 on server703. Native inventory merged to slot3/count2, gold fell from1000992 to600992, and read-only SQL retained count2/week2962. Full normal client restart716 preserved the inventory, gold and expiry and displayed2/70. The live second purchase displayed3/70 and shop reopen retained3, so instantaneous display remained a real defect.
- Current signed client c3ea63ec's BuyItem handler VA0x1FD80E0 reads the item quantity and passes it to the additive purchase counter at0x1FD8E51/0x1C61450. Project stackable BuyItem ACK entries using this purchase's quantity; keep equipment instance bytes, stored RawEntry and following class0/op14 inventory totals unchanged. Apply the same projection to material exchanges and retain consumed-material balances.
- Private validation724 preserves three baseline failures and passes the same three regressions plus related NPC-shop checks with the fixed PVF. Required combined validation and native post-deployment verification are still pending. This change does not reset quotas or alter prices, expiry normalization or repository transaction rules.
- P2/P37/P50 remain excluded; ordinary Luke unlock remains cancelled. Raid/reward private candidates remain outside this release. Their independent reviews still identify open recovery and collision boundaries.

## 2026-10-13 — Deploy validated expiration merge and verify the own-account restart

- Source970aaf5f0ef7c20e67cc556170b381690ad909e0 passed all four jobs in pipeline165161: Linux571828, Windows571829, core571831 and Semgrep571830. Semgrep itself succeeded; its allow-failure setting is not used as evidence of a pass. Shipping audit710 verifies all seven required expiration tests and the exact committed3475-file source snapshot against validation699.
- Deployment703 installed the validated Linux SHA25681172a8de42e830a1823e6394fa3ba605a6e33380073513b3edeedbf20073226 with backup /opt/dnf90/backups/d33-resume-970aaf5f-20261013-703. Independent704 confirms PID1177881, zero automatic restarts, ready and118 game listeners. Configuration, PVF, signed v97 client feeds and relay are retained. Own-account selected assets match the pre-deployment snapshot; SQL707 still reads purchase count70/week2962 without writes.
- Normal full own-client login706 starts PID32656 and returns character106 to the activity shop. Native display remains70/70, gold6000992 and existing materials remain. Read-only708 verifies the live counter70 and enabled715 bit without memory writes, pause or injection. This is deployment/relogin preservation evidence; the existing capped account cannot yet supply a new successful purchase to demonstrate native expiration merging. No quota reset or historical row consolidation was performed.
- Maintenance source705 matches the validated3475 files after backing up the previous3471; runtime settings are retained. Four private raid/reward candidates continue, with unavailable-read unknown-commit callers and full trace persistence still open. P2/P37/P50 remain excluded and ordinary Luke unlock remains cancelled.

## 2026-10-13 — Merge purchased stacks using their persisted expiration

- Normalize retired absolute PVF expiration dates before the shared product grant compares an existing row and its stack identity. Repeated purchases now compare against the same renewed date written to inventory. Future absolute dates, daily-delete expiry, distinct legitimate instance expiry and transaction boundaries retain their existing rules.
- Preserve the private unmodified-source RED evidence for the second purchase and the 70-item row count. Consolidate seven shipping regressions, including a post-mutation repository Save fault that leaves inventory, gold and account purchase count unchanged. Parent combined validation699 passed: default8813 passing nodes/830 environment skips/0 failures, fixed-PVF240 passing nodes/0 skips/0 failures with all required tests present, gofmt,210-package list, scoped vet, Windows/Linux builds and actual BAT preflight. Source3475 files stayed byte-identical through validation. Deployment is not yet performed.
- Record completed P29 native evidence: own account10070/character106 purchased to70, actual attempt71 returned opcode21 error0x85, shop reopen and full client restart retained70. Earlier capacity error0x04 at56 was resolved by normal sale of QA items. This establishes the tested purchase-count boundary; the new expiration merge is a separate server change awaiting deployment and native verification.
- Client delivery695 published signed preview/stable version24.9.12.42-v97-d33-715. The only EXE change is opcode715's allow bit at file offset111747929 (VA0x6E92359): 0x00 to0x08. New EXE SHA256c3ea63ecbc8df88697aba1e3b0c3314ff6fbdf4a6d0a6c21cbe04f70e1700056; old82e0c6a392387ed428a39770f89bc67754ee208b5caa81c6d3c959d11c2e7c7c remains in rollback. Protocol profile90cn-decode-bypass-v1, PVF369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06 and dstr ee8904c8540ef63807dc984604a3ea0f5cc4040228e5ac2e95d490c03d21ec08 are retained. Independent postpub695 verifies shipped UpdateEngine.Fetch/Check and the actual signed blob; all other7711 manifest rows are unchanged.
- P2/P37/P50 stay excluded; ordinary Luke unlock remains cancelled. Private raid recovery and reward candidates are not included in this change. Earlier dated statements below retain their historical scope.

## 2026-10-13 — Project one top-level row per friend adventure group

- Project each accepted friend account once. Prefer its durable last-selected character while online; otherwise choose the least online character ID using a total order. Preserve the existing offline fallback, row fields, pending relations and 255-row batching.
- Keep the existing projection admission/error behavior. The representative helper only orders admitted candidates; it adds no rejection gate. Numeric ties have a stable spelling tie-break.
- Preserve decoded 1373 deletion followed by 1371 upsert and 1372 completion on representative changes. Current formal-client handlers 1371/1373 use account-friend manager 0x56A3774; character-friend 291 uses separate manager 0x56A37D0.
- Candidate baseline target-rule RED and candidate regression receipts are preserved under p57-group-projection591. Parent validation641 passed: gofmt, 210-package list, default core, fixed-PVF related tests, scoped vet, Windows/Linux builds and actual BAT preflight. Audit646 confirms every new group test passed; skipped tests remain skips. Native populated-friend grouping and the small red-box control remain open; this source change is not full P57 acceptance and is not deployed.
- P29 independent own-account read626 proves count1/week2962 persists; ack627/639 sends item490010214 count1 in list0. Native display remains0/70 and current own client cache reads635/637 find no key in either counter. Continue actual opcode715 consumer/codec investigation; do not attribute this to lost persistence or Tuesday reset.

## 2026-10-13 自主继续工作检查点618（香港 2026-10-13T06:20:15.064345+08:00）

容器修复ad246477cf1ac12d965de33d15b2e200f8b0ee82已完成597联合验证、165066四项云CI success、608标准服部署及609独立复核。线上PID1166774，自动重启0，二进制SHA2561d303d010d0ee607ac24b5ca48209b6b5344b293a9e40daa91b30c002fcd53d6，118游戏监听，配置/PVF/更新清单/relay保留。回滚备份/opt/dnf90/backups/d33-resume-ad246477-20261013-608。612备份旧3468文件维护源码并同步3471文件，四个路径变化，运行配置未改。团本571私有候选未部署。

既有自测10070/106正常退出、正式启动器重新登录并重选入城；609只读完整选定资产快照等于部署前，金币19800992、417总5600、3277×7、490010214×1主栏121保留。没有重置账号/SQL写或重复发放。

P56原验收重点是页面关闭与商城退出回城；about:blank允许但非必须，op773不是关闭验收门槛。正式客户端82e0c6a3/当时线上4d7b1b2b的583和606两轮充值X关闭、黑钻确认关闭、商城退出回城均成功，无闪退；此自测范围通过。保留about:blank未出现的旧证据，不用此现象否定关闭成功，也不扩张到其他客户端版本。

P29出现真实新失败：购买后及同会话重开1/70，在服务重启、重新登录/重选后实际显示0/70，金币和已购物品仍保留。此次恰跨周二06:00，周策略应周四06:00；实际count请求shop_id3136/body_len4，不能归因查询用了itemshop ID。尚未取得持久化两键，桶差异、完整Account.Save覆盖、Redis缓存或客户端投影原因仍在查，不宣称购次已持久化或数据库确已归零。70/71原生边界仍待验收。

P61/P68新版本个人金库8格打开，600堆叠双击与拖动未产生op19，不能计存入通过。P57原生三页空占位行数角色好友7/冒险团好友6/黑名单6，仍见小三角；冒险团好友页没有自身角色行，已纠正此前空列表/自身行预测，真实好友分组和按钮功能未验收。

P2/P37/P50保持排除，四个现有代理无子代继续。团本571g单人恢复5例私有通过，父独审要求补配置/布局/清关journal篡改与真实非零进度；Aquinas事务门与窗口测试通过但真实生产handler失败ACK/全hook仍未完成。C盘闲置Go缓存清理约28GiB完成，活动缓存/模块/工具链保留，06:17附近可用25056137216字节（约23.3GiB），编译自测持续。整项任务ACTIVE，历史失败保留。

证据：container-cloud-ci603-c/receipt603.json、resume-standard-deployment608/receipt608-deploy.json、container-deployment-verification609/receipt609.json、maintenance-source-sync612/receipt612.json、native-qa-account574/native-mall606-observed.json、native-qa-account574/own-shop-events614-shop614.json、native-qa-account574/social611/。以下旧段落均为各自时点事实。

## 2026-10-13 自主继续工作检查点600（香港 2026-10-13T05:09:40.699077+08:00）

用户已明确授权使用自测账号并自主完成后续工作，四个现有子代理继续并发，P2/P37/P50退出修复队列，已验收关闭项保持关闭。

容器初始化及保存失败控制已完成597联合验证：默认8805 pass节点/825 skip节点/0 fail，固定PVF相关233 pass节点/0 skip/0 fail，required专项齐全。gofmt、list210包、scoped vet、Windows/Linux构建及真实BAT check --skip-database全部通过。源码3471文件验证前后哈希相符。本批尚未部署，当前线上仍4d7b1b2b；跳过与历史全PVF失败、超时分别保留，不能计为通过。

自测账号10070/角色106 qa57590级继续原生验收。正常任务5578“急切的青龙”和“被破坏的祥瑞溪谷”均通过对话完成，正常金币奖励共992；第二任务ID只读回读待补。随后正常进入祥瑞溪谷，打开海流商店，购买490010214一张，单价200000，客户端周限由0/70变为1/70。只读回读确认物品主栏121数量1、expireTime1830268800、金币19800992。没有GM完成任务或重置购次。70/71边界、重开及重选持久化仍待验收。

P66已见真实op20消费品按类型整理（category2/condition3/response_count2），417×600从主栏66移到65，整理时钱包20000000保持一致；没有独立op13日志，晶体按钮路径仍开放。P61/P68金库打开成功但拖动未见op19，不能计存入。P56两页打开关闭及两次op65确认，about:blank效果和op773仍未通过。原生邮件已领取417总5600、3277×7；发放未重复，无其他玩家改动或外部支付。

团本571b资源摘要/恢复占用补丁冻结，571c真实活会话截止回城4 PASS，移除重绑分支的行为负例1 FAIL；完整Settings恢复和所有生产持久化接线仍在四代理候选区，未混入共享源码。C盘清理约28GiB已完成，当前主Go缓存、模块及工具链保留，验证继续通过。

证据：container-combined-validation597/receipt597.json、continuation-checkpoint600/native600.json、native-qa-account574/own-events583-epicroad598.json、native-self-test543/gm543-purchase598check.json。以下旧段落均为各自时点历史。

## 2026-10-13 — Initialize missing move-container state and preserve honest native acceptance

- Before routing a valid selected-character move between known character containers, establish missing main/avatar/cargo state through the settings owner, then read it back. Existing valid state stays unchanged; load or save failures reject before inventory mutation or ACK. Wrong class, malformed requests and unrelated containers retain their original guards.
- Preserve baseline failures and verify the integrated owner/handler controls: inventory 543 and related bridge 118 passing nodes, plus four passing save-failure test nodes without skips. Scoped vet and build passed. Full combined validation and deployment of this new batch remain pending.
- Continue native testing under the user's own-QA authorization. Normal login, creation, town entry, virtual gold and mail claims are confirmed for the QA character. Mall pages open and close, but about:blank is not accepted; no op773 was observed. Empty-tab sort and vault drag attempts do not prove op13/op19 acceptance. Keep P2/P37/P50 excluded and all unverified cases open.

## 2026-10-13 — Verify all four cloud jobs for the deployed resumed source

- Independently GET pipeline165000 and jobs for source4d7b1b2b: Linux571472, Windows571473, core-tests571475 and Semgrep571474 all succeeded on Runner1615 with complete timestamps. Semgrep itself succeeded despite its allow_failure configuration.
- Preserve previous CI/resource failures and keep native acceptance separate. No private job trace was read or exported.

## 2026-10-13 — Deploy and independently verify the resumed batch

- Publish source4d7b1b2b after validating identical committed bytes; deploy its Linux artifact with a verified backup and rollback path. Independently confirm process1159493, zero restarts, ready, binary SHA25692ab4a57 and all118 game listeners.
- Preserve runtime configuration, resource PVF, client feeds and relay. Match formal v96 to both feeds; synchronize3468 validated source files to the maintenance tree after backing up its previous3452 files.
- Record default8791 pass/824 skip nodes and related actual-PVF218 pass/0 skip nodes, scoped vet and both builds. Preserve initial resource-allocation failure and prior full-PVF failures; native acceptance remains pending after02:00.

## 2026-10-13 — Preserve account-vault stack identities and known PVF caps

- Compare the repository's explicit stack identity fields before merging, while retaining normalized PVF contract comparisons. Reject conflicting stackable type and expiration metadata.
- Reconcile a missing cap with the matching item's recorded cap and preserve that cap on the merged destination without mutating caller snapshots. Explicit conflicting caps still reject. Mail construction now records the positive resource cap.
- Preserve red baseline and default missing-PVF failures. The final actual-resource mail/deposit and identity controls pass 13 cases without skips; default resource-dependent controls explicitly skip when no PVF is provided. Native P61/P68 acceptance remains pending.

## 2026-10-13 — Suppress ground items for admitted team raid runs

- Freeze team mode on the legal raid entry and suppress six ground-item creation routes for all admitted participants, including expired/retired entries. Ordinary and single raid drops retain their positive controls.
- Integrate only the verified six production files and three tests. Preserve and reject the earlier mixed comparison patch; preserve full real-PVF timeout and baseline resource failures separately.
- Luke Light Altar quest essence grants are a separate direct-inventory route; extra7 and native player acceptance remain open.

## 2026-10-13 — Load sort-wallet projection before committing inventory

- Independently reproduce the first gold candidate's transient-read boundary: it sorted and persisted inventory before failing its later Character load and reporting the request blocked.
- Load and clamp the authoritative wallet before owner.Sort. A read error now rejects before any inventory write or ACK; after commit only the prepared value is used. Keep optional state and non-wallet slot conflicts unchanged.
- Reject the proposed fallback that would send a full main-list refresh without the wallet on read failure. Preserve both earlier candidates and their failure evidence; native crystal-button acceptance remains pending.

## 2026-10-13 — Enforce resource-declared NPC purchase windows

- Read the current three-token purchase-limit form, including the real Epic Road account weekly70 policy for490010214, and retain the legacy two-token form's cumulative/daily-marker behavior.
- Correct the first candidate's explicit-daily reset gap: an explicit daily period uses the existing06:00 service day without requiring a separate reset marker. Weekly uses the server's existing Thursday06:00 operating policy; the weekday is not a PVF fact.
- Retain transaction quota consumption, cross-character account scope, concurrency and rollback controls. Both the original omission and the first-candidate daily defect have preserved RED/GREEN evidence. Native purchase/acceptance and deployment remain pending.

## 2026-10-13 — Correct native mall web-ticket replies

- Match the current op773 consumer's success/type/string-length/string/code fields, correcting the old6-byte short reply. Supply the user-authorized about:blank address in op773/op65 with the proved ASCII length limits.
- Keep failure replies, ticket request limits and the registered handlers intact. The protocol mismatch has static and RED/GREEN evidence; the effect on subsequent parsing and the player's inability to close the mall still requires native acceptance.
- Integrate only four web-ticket production/test files after a clean patch check. Candidate bridge/vet passed; combined-source validation and deployment remain pending. Do not close P56 from an offline reply test.

## 2026-10-13 — Preserve authoritative gold during inventory sort refresh

- Reproduce four missing/stale wallet failures through the real Sort handler and native full-list response. Character.Stats retains4321 while the old response omits the wallet or emits9; the prior516 fixture already contained the wallet and did not cover this projection gap.
- Project Character.Stats gold into a cloned list0 snapshot, matching login limits and preserving non-wallet slot conflicts, absent optional state and other containers. Do not persist a synthetic gold inventory row.
- Full inventory package, projection boundary controls and merged controller/protocol retry pass. Preserve531's initial startup timeout; GOTELEMETRY=off is used for the completed retry. The exact player crystal-button request and native acceptance remain unconfirmed; P66 stays open. Deployment has not occurred.

## 2026-10-13 — Resume four repair lanes with updated player scope

- Resume D33 at the user's direction, exclude player issues P2/P37/P50 from repair, and preserve accepted closures and the canceled ordinary Luke sequence work.
- Assign isolated account-vault, team-ground-drop, mall-close and Epic Road limit candidates. Permit native testing and test item/attribute grants after Hong Kong 02:00; before that continue source and offline validation only.
- Remove raw provider stderr from repository synchronization failures and add synthetic git/CLI error-output regression checks while preserving operation and exit metadata.

## 2026-10-13 — Verify the merged master pipeline on the restored cloud Runner

- Independently read pipeline164976 at2026-10-13 00:30:45 Hong Kong: exact b181abd1 source, all four Linux/Windows/core-tests/Semgrep jobs success on Runner1615, complete timestamps, runner online and unpaused.
- Preserve historical path-test failures and the user-requested stop. The game service remains18393; the merged Go source and pending game candidates are not deployed by this documentation update.

## 2026-10-13 — Stop the current section and preserve an acceptance handover

- Publish the current standard-server retest scopes and remaining reports after explicit player acceptance; retain P66 gold loss separately and stop superseded ordinary Luke sequence work.
- Fast-forward the local repository to the player's merged MR1 b181abd1. Keep the running game at validated18393 and the maintenance Go tree unchanged; repository merge is not deployment.
- Restore cloud Runner1615 and execute actual Linux/Windows builds and SAST. Preserve old core-test failures and the running merged-master verification pipeline rather than declaring full CI success prematurely.
- Save the four workers' completed evidence and unfinished steps, pause the02:00 test heartbeat at the user's stop request, and record restart conditions. No game/SQL/client operation or local Docker installation occurs.

## 2026-10-12 — Reconcile explicit player acceptance and stop superseded work

- Close P80 forest main-quest progression from the player's additional acceptance; retain unrelated activity admission issues.
- Close the player's accepted volcano barrier, raid return, waiting-area access, training admission, skill-option persistence, sound and awakening-reset reports. Preserve earlier accepted results and distinguish the new crystal-sort gold loss from the accepted crystal display.
- Accept mechanical-throne prerequisites by the reported behavior, retain the P18/P19 numbering alias, and stop unshipped sequential-unlock candidates at the user's explicit direction. Keep the unrelated light/dark reconnect report separate.
- Accept the current adventure-friend character display. Investigate the small button from actual client evidence, replacing the unproved collapse/expand assumption. Reassign the same four agents; no runtime, client or database operation occurs in this record.

## 2026-10-12 — Deploy the validated source to the actual standard test server

- Apply the user's new direction: the existing online service is exclusively for testing, replacing the earlier keep-r98 policy. Use the standard launcher channel for acceptance; both update channels launch the same live game service.
- Deploy the exact validated18393b1d Linux amd64 artifact and3452-file public source archive to the existing dnf90.service. Independently verify the new PID, running binary, readiness,118 game listeners and bootstrap/auth/GM ownership. Retain the actual prior binary backup for rollback.
- Keep the client manifests, PVF, effective configuration and relay state byte-identical. No GUI or manual SQL operation occurs. Publish only the completed Anton town-return and activity-origin subscopes for retesting; keep all reported broader failures and accepted closures accurate.

## 2026-10-12 — Preserve owned completed Anton returns and ordinary Seria origins

- Freeze an internal return owner on legal normal Anton map entry and record only locally authenticated map completion before global clear guards. Allow completed town return after another squad, timer or phase changes global progress; preserve the existing reward, clear-count and retry/selector boundaries.
- Exclude the complete actual PVF town28 Epic Road identity from ordinary Seria origins, preserving town50 behavior, ordinary saved origins and existing room-door fallback. Do not claim the player's native target or full relogin/Abis behavior is proved.
- Validate the exact combined3452-file source: all DNF domains/bridge/control8712 pass nodes,0 fail,783 environment skips; fixed-PVF155 pass,0 fail,0 skip; required vet and Windows/Linux amd64 server builds exit0. Preserve the initial missing-root-template control failure and repair only its exact public fixture before the full control rerun. No runtime deployment or GUI acceptance occurs.

## 2026-10-12 — Correct deployment provenance and acceptance wording

- Freshly verify the running isolated server PID, startup time and binary hash. Its installed 3443 source files exactly match20b0c911; the published3449-file tree only adds controller/diagnostic changes relative to that instance.
- Clearly distinguish source candidates, repository publication, installation/startup and player acceptance. Keep402/398/409 unintegrated and undeployed. Do not attribute all failed player retests to missing updates: prior game changes present in the installed tree remain unresolved where the player reports failure.
- Retain the daily r98 deployment, four-worker limit, local Docker/Runner stop and native test pause until Hong Kong2026-10-13 02:00. This checkpoint changes documentation and state only.

## 2026-10-12 — Record ordinary Luke sequence and Seria bottom-door regressions

- Keep team-raid ground loot prohibition separate from ordinary Luke map unlocking and the two independent Light Altar boss quest grants. Remove the previous throne/essence subscopes from the current candidate queue after the player's failed retest.
- Accept only the Epic Road NPC return to Seria. Keep the bottom door still reaching Auspicious Valley, cold relogin and Abis admission unresolved. Record nine candidate and49 open scope groups while retaining every earlier accepted closure.
- Retain source-only four-worker assignments and native pause until Hong Kong2026-10-13 02:00. Preserve417's failed push and418's fresh old remote head instead of claiming publication.

## 2026-10-12 — Review Linux/remote deployment merge request against current master

- Inspect MR1 source ca254089 in an isolated object store. Its exact combined tree merges cleanly with f894304 and passes protocol/bridge/control regression, deployment and sync tests, scoped vet and Windows/Linux server builds. Preserve environment skips and unexecuted Docker/native checks.
- Prepare a bounded synchronization error-output correction with synthetic RED/GREEN and all seven synchronization integration tests passing; recommend it before merge. Source pipeline details return403, so keep its failure cause unconfirmed. No online merge, runtime change, Runner registration or local Docker operation occurs.

## 2026-10-12 — Reconcile latest player regressions and accepted sound setting

- Close P55 from explicit player acceptance. Retain P56 click-crash acceptance as partial while mall closing remains unresolved; the player permits a blank-page route. Move the nine failed scopes out of the current acceptance-candidate queue without deleting prior source-test evidence.
- Prioritize Anton reference comparison and in-dungeon progress/other-squad return failures. Expand P20 from material filtering to every floor item and gold drop in team raids. Preserve free-fatigue, skill-lock/CD selection, account-vault stacking, crystal sorting and automatic first-awakening reports as unresolved.
- Keep the four-agent cap and the native pause until Hong Kong2026-10-13 02:00. This checkpoint records player evidence only; daily r98, installed QA and stopped local Docker/Runner remain unchanged.

## 2026-10-12 — Clarify P57 adventure-group friend layout scope

- Record the player's screenshot contract: top-level entries are adventure-group friends; characters of the same group belong to its expandable content. Keep the online-only and last-online-character fallback rules, but classify grouping and collapse/expand behavior as unresolved until the current-client consumer contract is verified.
- Preserve the native-test pause until Hong Kong2026-10-13 02:00, the four-agent cap, accepted items, daily r98 and stopped local Docker/Runner. This checkpoint changes documentation only; no protocol field, product source, runtime, database or client operation is introduced.

## 2026-10-12 — Pause native tests until02:00 and reconcile current acceptance scopes

- Honor the new Hong Kong2026-10-13 02:00 native-test deadline while continuing code work. Update the existing one-shot recovery, candidate scope list, unresolved original-player reports and accepted closures. Keep source/runtime/deployment distinctions and the unconfirmed357 docs push explicit.
- Preserve the failed independent worker evidence and replace the closed slot within the four-agent cap for offline review. Keep native launches, daily r98, local Docker and Runner operations stopped as directed.

## 2026-10-12 — Record normal Anton prerequisite completion and reconcile remote CI state

- Observe the second native role completing the supported Lv89 story sweep, ordinary Anton side quest12829 acceptance, native claim of100 golden cubes from one normal GM mail, and ordinary material submission/reward settlement. Authenticated readonly checks confirm12829/3779/3757 completed and all100 cubes consumed; before/after runtime identities remain intact. Keep normal channel admission and the whole-volcano acceptance open.
- Correct the earlier unconfirmed347 push reference with successful ordinary353 source commit1de3be47, and retain355's final canceled pipeline164782 with all four jobs unstarted and unassigned. Read failed-pipeline notifications disabled without claiming restored Runner or remote-CI success.
- Preserve installed QA20b0c911/3443, reviewed source3449, daily r98, accepted player items and the stopped local Docker route. Publish this evidence as a separate docs-only commit.

## 2026-10-12 — Add fixed raid-create rejection diagnostics and narrow QA prerequisites

- Add reviewed fixed rejection events at the current create handler and its outer op664 authentication dispatcher. Preserve authentication, short-circuit order, original failure frames, successful metadata-before-ACK and other commands; log only fixed stage enums and existing safe connection metadata.
- Integrate four exact production/test paths into repository and maintenance source, with3449 source/module/fixture entries matching the sealed candidate. Complete bridge default-core regression with5635 passing nodes,0 failures and654 retained environment skips. Build Windows amd64 and Linux amd64 servers; preserve the separate Windows386 server's existing constant-overflow failure and do not replace the field-tested386 client controller.
- Observe bounded unbound auth metadata from the immutable full capture. Identify the normal local launcher's15-minute in-memory token lifetime and the later channel-hop token-unbound event, without declaring canonical cause or changing token lifetime or raid gates. Record that a normal fresh process is required for a new launch token.
- Read the second role's actual quest prerequisites through normal authenticated GM GETs:12829,3779 and3757 are not started or acceptable, with no3262 inventory rows. Preserve normal quest settlement and all accepted player reports. Keep installed QA20b0c911, daily r98, whole-volcano GUI acceptance, stopped local Docker/Runner and remote-CI limits explicit.

## 2026-10-12 — Narrow ordinary raid-create failure and record second-role prerequisites

- Publish reviewed native startup source12650e3f and confirm failure-pipeline notifications disabled. Cancel only the new pipeline whose four jobs were unstarted and unassigned; keep Runner and local Docker stopped, with no remote-CI success claim.
- Validate the observed op664 legacy request/meta pair and native header privately; its route0, ordinary UI flags000, two-byte ASCII name and current length shape pass existing source gates. Observe one normal same-role reselect and same-name creation still rejected; preserve session-auth/runtime-membership uncertainty without lowering any gate.
- Observe the second role's ordinary Anton-channel prerequisite rejection. Identify the real PVF side quest12829, prerequisite3779 and material3262x100; record the interrupted source worker and unfinished normal-completion route instead of modifying quest data or claiming a raid formed.
- Preserve all previously accepted player items, daily r98, installed QA source20b0c911 and open whole-volcano animation/damage/kill/reset scope. New rejection diagnostics and bounded auth projection remain external work in progress.

## 2026-10-12 — Integrate owned native multi-instance startup and observe two roles

- Add the reviewed Windows386 controller route for the fixed native client: hold the new child identity, restrict single-instance call interception to declared native call sites, restore the startup gate, bind the normal authenticated PID and detach before the existing five-second check. Preserve first-client/server/database identities and explicit unsupported-architecture refusal.
- Integrate seven exact control production/test paths into repository and maintenance source. Confirm the complete3447-file source/module/fixture map, formatting, package listing, scoped vet and Windows amd64/386 plus Linux amd64 controller compilation. Reuse184 control/Doctor passes with4 expected skips from identical reviewed inputs; retain domain/bridge/PVF results at their original scope.
- Execute one independently reviewed external386-controller trial successfully and observe two native role windows. Keep installed/running isolated source20b0c911 unchanged; newly compiled controllers are not installed or executed. Default amd64 explicit multi remains unsupported.
- Observe a normal native raid-create failure and confirm source-declared accepted channel type23. Preserve complete logs and safe metadata without exporting packet bodies, credentials or original log digests; raid authentication/binding and the failure cause remain unproven. Keep ordinary volcano animation/damage/kill/reset acceptance open.
- Record native Epic NPC return and same-role reselect subscenes only. Preserve accepted player items, daily r98, the four-agent cap and stopped local Docker/Runner route. Do not claim restored remote CI or full original13 acceptance.

## 2026-10-12 — Observe native Epic NPC return and preserve unresolved volcano scope

- Handle the latest ten-minute GUI delay once after its13:31:27 Hong Kong deadline and a fresh identity check. Observe the ordinary isolated native NPC entry and return to Seria; keep relogin/town-exit and Abis admission open rather than closing original13.
- Preserve complete frozen service/packet inputs and the actual external after guard. The prior parser refused association; correct the new adapter's unsupported login-binding attribution and identify four duplicate grow_type diagnostics using source-declared event/key counts only. Keep new adapter and native multi-instance candidates outside deployment pending finite independent review.
- Retain validated/running Go source20b0c911, daily r98, accepted reports and the four-agent cap. Keep local Docker/Runner stopped and remote CI execution unproven while awaiting the user's cloud server.

## 2026-10-12 — Switch normal isolated QA and confirm native material delivery

- Complete the reviewed normal BAT/controller switch with private backups, keep the external database running, install the exact3443-file source map and verified programs, pass the real endpoint preflight and prove READY with121 TCP/3 initial UDP. Enable GM through the normal loopback instance configuration; independently verify the new process/state and retained database.
- Observe normal account login and native entry to Seria. Send one entry-material attachment through normal GM mail, claim it in the native mailbox, and independently confirm main-inventory count1 with the previously accepted necklace unchanged.
- Execute one new supported additional-client diagnostic using the integrated held-handle facts. Record genuine native exit1 during the5-second check, retain the first client and unknown inner cause, and preserve the sealed115 history. Ordinary two-member volcano damage-shield acceptance remains open.
- Keep daily r98 and accepted items, the four-agent limit, disabled project failure mail and stopped local Docker/Runner route. Remote CI has not executed; preserve all gates and await the user's cloud server.

## 2026-10-12 — Preserve native additional-client exit diagnostics and portable regression sources

- Capture liveness, true exit status including259, and100ns birth facts through the held Windows process handle before Release. Keep unknown facts explicit and retain the existing registration, environment,5-second guard and successful-start behavior. Do not infer the cause of the earlier live client failures.
- Integrate the four production files reviewed by202 and five permanent tests approved by214. Replace the rejected author-absolute formatter source with the current package source; assert the exact nil enum for each platform and reject fabricated zero exits or known birth facts.
- Verify the complete3443-file candidate with144 control/Doctor passes,0 failures and3 expected skips, gofmt/list/vet and Linux control-test compilation. Reuse209 Windows/Linux program builds after proving every non-test source file unchanged; compilation is not Linux runtime execution. Retain173 domain/bridge/scoped-PVF results at their original scope.
- Keep runtime QA on31e2b51/3433 source while preparing the normal GM switch. A fresh read-only check confirms the former client absent and no related DNF processes, with server and retained database identities unchanged. Preserve unknown exit cause, player acceptance conclusions, daily r98 and the stopped local Docker route.

## 2026-10-12 — Enable explicit normal loopback GM configuration and reject endpoint reuse

- Add optional server.gmListen, disabled when absent/blank. Require a valid127.0.0.1 port and reject numeric aliases of admin/channel endpoints. Generate GM environment values only through normal instance configuration; preserve inherited bridge-env scrubbing and default legacy behavior.
- Reject duplicate role endpoints in the standalone Doctor path too. Integrate exact independent161-v04 production files and independent181 permanent tests; make two Doctor regressions require the explicit collision error rather than passing on a previously occupied listener.
- Validate snapshot173 with9042 core passes and791 environment skips,396 scoped actual-PVF passes and one separate-asset skip, list/vet and all three Windows/Linux BAT builds. After the test-only correction, validate all control/Doctor tests with136 passes under shuffle, preserve actual v03 two-test RED, and vet the final changed packages. No further production or build change. Earlier broad real-PVF failures remain historical evidence.
- Preflight an independent runtime copy: normal check --skip-database rejects occupied QA endpoints; check additionally skipping ports passes assets/client/configuration. Do not label the ordinary preflight or remote CI passed. Keep running QA source31e2b51 and daily r98; optional GM has not been enabled in either running instance.
- Record the latest GUI pause to2026-10-12 10:44:03 Hong Kong, preserve accepted items, and continue four source lanes. Keep unsafe159/163 capture revisions rejected pending corrected independent reviews. Keep local Docker/Runner work stopped awaiting the user's cloud server.

## 2026-10-12 — Record real raid creation and bounded follow-up evidence

- Record an ordinary native UI raid creation after normal token-bound login: one actual role in phase0, insufficient entry material and no raid start. Supersede the pending manual-name assistance state.
- Execute the independently reviewed second-client115 preparation once. Preserve SECOND_CLIENT_EXITED_AT_5S_CHECK, the retained first client and the unknown inner cause; seal that run and do not retry it.
- Independently verify the original Epic-return rollback counterexample passes with both errors exposed. Keep the external candidate unintegrated while its native NPC request contract remains open; partial socket writes and failed persistence rollback remain explicit limits.
- Verify four actual-PVF Abis calendar/admission/preflight controls without finding a production contradiction or introducing a candidate. Retain native request and player-quota acceptance as open.
- Record the user-requested GUI pause until09:35 Hong Kong and one scheduled resume, while four source/review lanes continue. Keep Go source31e2b51, daily r98, closed acceptance items and the stopped local Docker route unchanged.

## 2026-10-12 — Stop the local Runner setup route at user request

- Record the completed per-user Docker Desktop installation and failed WSL VM engine starts separately. WSL3.0.1/default2 and Windows virtualization were already present. Do not claim a usable container engine or Runner.
- Stop Docker, verify Desktop/backend processes are absent, and disable its newly created current-user login startup entry. Retain the installed software and avoid a system reboot. No Runner was created or registered and no CI job started.
- Move the next Runner setup to the cloud server the user will provide. Preserve CI job definitions, notification preferences, running source31e2b51, daily r98 and player acceptance status.

## 2026-10-12 — Limit repeated CI failure emails and documentation pipelines

- Set this user's project notification level to custom with failed_pipeline=false, then read it back; leave global notification settings untouched. Keep other event fields at their existing/default values. Cancel the pending documentation pipeline164597 after verifying no job has started. Previously queued emails may still arrive.
- Add workflow rules that skip ordinary existing-branch pushes changing only root documentation. Preserve the complete Go subtree, build/CI/deploy inputs, tags, new branches and explicit pipeline entrypoints. Keep all existing build, core-test and SAST definitions intact; new build modules outside the watched paths must update the rules with their introduction.
- Validate the proposed configuration with the site's CI Lint and pipeline simulation. Both baseline and candidate simulations retain the same two builds, core-tests and semgrep-sast. Preserve the stricter static comparison's implicit-only normalization difference separately; this is configuration validation, not remote execution.
- Keep the unavailable Runner dependency explicit: the only visible instance Runner remains paused/offline/stale and no project Runner is available. This change reduces notifications and unnecessary queues; it does not make remote CI pass. Go source stays31e2b51 and daily r98 remains unchanged.

## 2026-10-12 — Bounded volcano flag evidence and open activity rules

- Record nine bounded fixed-client controls through conditional scene refresh, actor flag setters, real obfuscated storage/readback and attack-target qualification. Keep actual monster spawn, damage, HP and GUI acceptance open.
- Record the independently reviewed local second-client diagnostic preparation with explicit optimization rejection; it has not been executed against the game. Preserve the unknown cause of the earlier single failed normal CLI attempt.
- Reject the catalog-only joust draft after actual-resource transaction controls confirm material, participation reward and limit ownership gaps. Preserve old ledger behavior and leave the unproved new settlement rule open. Qualify the gold-cap source and native evidence without inventing fields or packets.
- Update the timestamped documentation pipeline snapshot. This commit changes documentation only; validated and running source stays31e2b51, daily r98 and accepted player reports remain unchanged.

## 2026-10-12 — Normal isolated QA live checkpoint and Runner failure

- Record the one normal controller start of source31e2 using the retained database, the read-only adoption after a case-only SHA guard correction, wrong-password rejection, and fresh native token-bound town entry.
- Preserve the original crafted necklace and saved512-byte option/skill data; keep skill GUI restoration and ordinary Anton0% animation/monster damage open. Record the failed additional-client attempt without inventing its cause or counting persisted roles as simultaneous online members.
- Keep the isolated joust2365 catalog mismatch open after persisting its existing disabled setting. Daily r98 and accepted player reports remain unchanged.
- Confirm pipeline164529 and source31e2 pipeline164570 failed with stuck_pending_no_matching_runners before scripts; current project Runner settings are enabled but the sole visible instance Runner is paused/offline/stale. Local combined56 validation does not replace remote CI execution. This update changes documentation only.

## 2026-10-12 — Reviewed normal native launch and character options replay

Integrate the current-Service username/password token route and consume its fresh token through normal launch-client. Keep PID registration as ownership metadata and retain existing raid/token/character guards. Login payloads and unknown provider/transport errors retain only bounded length/hash diagnostics; fixed domain-rule diagnostics remain readable. Preserve the independent nested-provider disclosure regression and the rejected round47 evidence.

Reset the character option replay gate on a real role entry and consume it only after successful op390 delivery, before op19. Reject non512 option saves only for the evidenced official24.9 profile. Preserve failed-write then fresh-reentry regression; same-entry retry and native GUI restoration remain open.

Support the verified official native/hookfalse profile through normal control. Add explicit start --use-running-database, default false, for the existing loopback QA database. This path creates only its own verified server process state, skips portable DB init/start/rollback/mysql.ini, closes schema initialization, and does not stop that external database. Existing owned DB lifecycle remains the default.

The exact combined56 source passes go list, 8560 core tests with774 environment skips, 396 scoped actual-PVF tests with one separate-asset skip, scoped vet and Windows/Linux BAT builds of all three programs. Log48 independent joint controls pass210/0/0; control50 independent tests and eight-file SHA audit pass. Mirror3433 source files. Earlier full-PVF failures remain historical evidence; this is source validation, not new GUI acceptance. Daily r98 remains unchanged; isolated native animation/damage, lock restoration and fresh token owner binding still need actual tests.

## 2026-10-12 — Review and integrate skill reset, Epic return and Luke waiting

- Rebuild free profession skills for op491, parse real automatic-level containers and reject manual mutations. Cover actual planner-produced class change/awakening, missing real grants, ownership and rollback; record the shared effect on73 automatic skills and recovery of historical points through full reset.
- Exclude the exact Epic Road resource from ordinary return origins; retain explicit compatibility mode and old snapshot limits. Bind the exact Luke waiting room to existing raid ownership without restricting ordinary area2.
- Withhold the durability tool proposal after independent review disproved its section classification and client-cause claim. Preserve P86/P87 and existing-data acceptance as open.
- Complete 8279 core and 388 scoped real-PVF passes, with 773 environment skips and one separate-asset skip; go list, scoped vet and Windows/Linux builds pass. Synchronize3403 source files and keep broader historical PVF failures visible.
- Preserve four actual worker deliveries/cross reviews and cap4. Record the isolated native raid-create failure and unbound-token evidence; running QA stays51886be and dailyr98 remains unchanged. No new GUI acceptance or public deployment.

## 2026-10-12 — Integrate four-worker repairs and GM request ordering

- Preserve accepted yellow records and original closed groups. Keep the four-subagent cap and record four actual deliveries; reuse lanes for disjoint next tasks.
- Merge account cargo auto-stack/count mirrors and atomic shared-crystal sort snapshots, exact training fatigue policy, actual quest tuple wildcards, character-scoped option saves and complete reward projection deduplication. Preserve GUI/SQL boundaries and broader historical PVF failures.
- Protect GM grow-options responses and errors by request generation and character identity. Retain male/female job values and labels; seven offline frontend scenarios pass after three baseline failures.
- Validate8266 core and332 scoped actual-PVF passes without failures, with740 environment skips and one separate-asset skip. Complete scoped vet and Windows/Linux builds; GM461 passes with17 skips. Preserve the failed output-decoding wrapper and occupied-QA preflight evidence.
- Synchronize3393 source files; running isolated QA stays frozen51886be, daily production staysr98. Volcano native animation/damage and remaining player acceptance stay open.

# Repository changelog

## 2026-10-12 — Tencent Linux remote test environment

- Add a separate Windows remote entrypoint using the inspected client, private WireGuard game address and loopback-only SSH bootstrap/account forwards. Preserve the existing local entrypoint, PVF/executable hash guards and API login tokens.
- Deploy the Linux image and MariaDB 11.8 to the 90cn host, with a dedicated persistent database volume, bounded container memory, VPN-before-Docker ordering, a systemd service and daily consistent logical backups. Copy the two existing local characters into the new remote database without replacing local data. Verify service recovery after an actual host reboot.
- Reproduce the remote v56 login failure: the unchanged client sends class1/op1 with a 610-byte body, routed into the legacy ignore path. Accept this observed exact length alongside 606 in stream and lifecycle classification; retain adjacent-length rejection and token binding. Regression cases fail before the change and pass afterward. The cause of the four-byte layout difference remains unproven.
- Verify actual remote token login, channel 10, the migrated roster, Lorain movement and a monster kill acknowledged with 39 experience, followed by town entry. Stop computer interaction at the user's request once connectivity is established. Full quest/party/raid acceptance remains separate; document validation and the legacy Windows preflight dependency in Docs/REMOTE_TEST.md.

## 2026-10-12 — Docker Linux profile and local token launcher

- Add a separate Linux image and generated Compose profile under ignored runtime/docker, with a dedicated MariaDB volume, private interface bindings, embedded UDP relay and timezone data. Extract real PVF channel assets with a source-built tool and preserve explicit external table overrides.
- Bind the embedded UDP relay to gameListenHost when configured while preserving the advertised host address and the legacy default; cover NAT address separation and invalid bind addresses.
- Add BAT/Python local-client entrypoints for the inspected v56 executable, API-issued login tokens, duplicate-client and asset-hash checks. Keep the vendor updater and binaries unchanged.
- Match the inspected v56 executable to the existing official-24.9 packet counts and recognize its observed 606-byte login body/token offset. Verify actual token-bound Linux game login, character creation, persisted roster after restarts, and keyboard movement in the Lorain tutorial; do not infer full gameplay acceptance.
- Add an opt-in restriction of default-group directory requests to real ordinary resident channels for clients that repeat bootstrap lookups without the script download. Retain multiple rows for native auto-selection. Enable it in the Docker local profile; retain named groups and the original server defaults/lifecycle. Revert an ineffective experimental keep-open change.
- Explicitly select the repository's existing channel reference for the tested v56 assets, retaining provenance and the PVF-extracted table. Keep the observed missing raid-map references and full gameplay verification open; no old one-click database or client binary patch is needed for this smoke test.
- Complete live fast-forward synchronization in both directions: GitHub Actions runs 38162071646 (GitHub to GitGud) and 38162153930 (GitGud to GitHub). Linux Verify passed after portable controller path fixes (38161846104).

## 2026-10-12 — Portable controller path validation

- GitHub's first Linux run reproduced two existing controller failures: backslash traversal was accepted in an installation receipt and Windows absolute assets bypassed the expected path guard.
- Normalize both separator styles and reject drive-relative/absolute Windows paths and UNC paths on every host. Retain Windows behavior and add cross-platform escape cases; do not skip the failing checks.

## 2026-10-12 — Optional GitHub / GitGud fork synchronization

- Add an opt-in GitHub Actions synchronizer for one master branch, using only fast-forward pushes and an independently scoped GitGud Deploy Key. Stop on divergent history or imported changes to CI definitions.
- Add a read-only verification workflow for Go checks, Linux/Windows builds and synchronization tests, including checks after token-driven synchronization.
- Validate seven integration cases with temporary bare repositories: equality, dry run, both directions, divergence, CI-change rejection and missing branches. Remote workflow results are recorded separately after publication.
## 2026-10-12 — Player report classification and gunner GM labels

- Classify72 source reports, close the five yellow acceptance records, preserve previous accepted groups and assign every remaining source record to an isolated lane. Record actual429 dispatch failures and the user-requested four-agent cap; resumed dispatch is not evidence of normal worker progress.
- Distinguish male/female gunner labels in GM choices after actual-PVF confirmation that both base names are identical while their awakening profiles differ. Preserve job values and keep the reported gender overwrite's full GUI cause open.
- Validate8230 core tests and461 GM tests without failures, scoped vet and Windows/Linux builds; synchronize3384 source files. Preserve prior PVF evidence and skips separately.
- Verify the original necklace remains visible in a fresh isolated client and read the same instance without granting/crafting. Retain formal-token verification failure and pending volcano damage acceptance. Daily r98 and the frozen51886be QA service are not replaced by the new GM build.

## 2026-10-12 — Isolated QA service switched to candidate33

- Shut down the exact owned QA09 service normally and retain the same database/config/assets. Preserve the initial port preflight block on the old admin connection's TIME_WAIT, with no candidate process created at that point.
- After endpoint release, start the frozen final32 candidate once; independently verify new-PID READY and118 game TCP listeners, then read the original necklace instance without granting/crafting again.
- Keep the player's standard client and dailyr98 untouched. New native PID binding and volcano animation/monster-damage acceptance remain open; source CI pending is separate from successful local builds.

## 2026-10-12 — Initial town snapshot deadline and preserved QA evidence

- Give the full initial-town player-state snapshot the existing15-second budget instead of the3-second mutation budget; preserve packet order, route/character guards and error withholding of typed op24. One legacy achievement cleanup transaction inherits the longer budget; other independently scoped contexts stay unchanged.
- Reproduce lost context after3.1 seconds with a real actor-route regression against the previous production file; verify healthy snapshot ordering and unchanged repository-error rejection after the patch. This is a unit-budget proof, not native/MySQL acceptance.
- Preserve cold29 disconnect/verify failure and cold30 early exit; retain the same crafted necklace by read-only snapshot without granting/crafting again. Standard launcher login is user-confirmed; volcano deployment and GUI acceptance remain open.
- Complete final32 domain/bridge/control, scoped actual-PVF, go list, scoped vet and Windows/Linux builds. Retain broader historical PVF failures, the separate-asset skip and the occupied-QA preflight boundary. Production staysr98.

## 2026-10-12 — Anton expired completed-map return across phases

- Preserve an already-owned first-phase expired-completed return receipt after the same Anton instance advances to the real second phase; retain exact run/account/channel/participant guards and town-only permission.
- Reproduce both op42/op72 failures before the patch with the real PVF phase VM, then cover phase advance, foreign instance rejection and unchanged retry/reward gates. Keep native phase-return root cause and GUI acceptance open.
- Complete final27 domain/bridge/control, scoped real-PVF, go list, scoped vet and Windows/Linux builds; preserve historical broader PVF failures and asset skip. Production stays r98 and QA09 retained.


## 2026-10-12 — Anton volcano native barrier state and adventure friend presence

- Derive native Anton symbol111 from full barrier depletion/recovery after verifying the shipped client controller. Keep it latched during partial recharge and restore it in reconnect snapshots; preserve ordered110/111/126 and110/111/127 writes without altering formal PVF markers. Exclude phase1 and Luke.
- Verify unmodified Go-exported break/recovery frames through actual v96/v97 native receive/583/store/UI111 branches; confirm AC and cutscene selection with explicit external fixtures, retain failed harness attempts, keep GUI/damage acceptance open.
- Verify real-PVF cross-squad delivery and actual server16 frames; add low-frequency successful-send diagnostics. Keep native animation and monster damage acceptance open; user reproduces on daily r98.
- Filter adventure-group friends to verified online characters; persist last-selected identity through the existing metadata entry and use per-account bind-generation ordering to prevent stale writes. Verify offline cold read and account isolation; native UI remains pending.
- Validate8227 core PASS/0 FAIL/736 environment SKIP and272 scoped real-PVF PASS/0 FAIL/1 separate-asset SKIP, scoped vet and Windows/Linux builds. Preserve the broader PVF run's seven assertion failures and timeout; reproduce all seven on the prior source baseline. Keep retained-QA port preflight failure explicit.
- Synchronize3383 source files to maintenance and current manifest. Production remainsr98, QA09 retained, computer control paused until2026-10-12 01:30 HK.

## 2026-10-11 — D33 raw declaration guards and CI runner diagnosis

- Reject ten unproved operation tags by raw section presence, including empty/symbol/duplicate/orphan declarations; retain atomic failure and legal floor1 controls. Independent B1 review passed.
- Final11 measured 1,058 supported/plannable level90 recipes, including273 floor recipes. Four former unresolved outputs declare empty item-category sections and now fail the raw guard; preserve1062/1058 as historical measurement.
- Validate8,220 core PASS/0 FAIL/725 environment SKIP,84 real-PVF PASS/0 FAIL/0 SKIP, go list, scoped vet and Windows/Linux builds; preserve final10 assertion failure.
- Record user acceptance of one2659502 necklace craft, retain remaining recipe/experience/cold-login work and UI pause until2026-10-12 01:30 HK. Production staysr98.
- Diagnose pipeline164381: both builds failed stuck_pending_no_matching_runners before script execution. Enable project shared runners; sole instance runner remains paused/offline. Keep CI gates and record the external runner dependency.

## 2026-10-11 — D33 bounded crafting floor and source qualifications

- Permit strict making-level1 in the eleven proved client categories; validate raw PVF shape instead of trusting the catalog projection. Preserve rejection for higher levels, skill gates, explicit rates and unknown semantics.
- Check ordinary drawing level in the owner transaction, reject restricted or malformed job fields, and conservatively reject all 39 drawings declaring unproved date semantics. Retain zero-count material audit and shared-asset rollback.
- Measure 1,062 supported / 1,058 plannable recipes at level90, including 273 floor recipes; preserve historical counts and avoid claiming native acceptance from catalog coverage.
- Validate final09: 8,097 core passes with 725 environment skips, 84 real-PVF passes without skips, scoped vet, go list, maintenance preflight and Windows/Linux builds. Keep native floor crafting and experience progression pending; production remains r98.

## 2026-10-11 — D33 native Vagabond checkpoint

- Verify one isolated level90 Vagabond support weapon through three native equip/unequip moves, unchanged item instance and durability on bag readback, and a new native client process retaining support slot24.
- Preserve combat and post-combat persistence as pending; additional item12 remains open. Shut down only the owned QA client/server and retain its database. Production remains r98.

## 2026-10-11 — D33 recipe settlement and observed native routes

- Read equipment design recipes from real PVF; fail closed for unproven rates, skill/itemmaking gates, upgrade tables and alternative source semantics.
- Settle supported drawings, character materials, account warehouse materials, gold and outputs atomically through the existing owner; reject historical character copies of account assets.
- Register observed native legacy op25 and op65 routes alongside their proved Upper owners. Refresh recipe inventory with op13 after commit; do not invent 119-byte reward fields.
- Retain rollback, locked/corrupt asset, overflow, shared refresh, cold read and native dispatch regressions; validate domain/bridge/control, real PVF, scoped vet and Windows/Linux builds (final07).
- Record isolated PID-bound native town entry and bounded recharge/diamond window recovery; verify one native 5003 craft and client cold relaunch; keep other recipes/failure paths, membership/payment, Luke and equipment acceptance open. Production remains r98; isolate the joust catalog workaround.

## 2026-10-11 — D33 candidate repairs and user acceptance

- Record user acceptance of original groups 1/2/3 and additional item 3. Production remains r98.
- Fix Vagabond support-weapon placement and integrate reviewed Luke route, prerequisite, material-drop and dual-boss candidates.
- Preserve historical source manifest; add current-source manifest and D33 validation/acceptance boundaries.
- Validate domain/bridge/control, real-PVF cases, vet and Windows/Linux builds; retain prior failure evidence.


## 2026-10-11 — Luke D33 candidate review (Luke source/test only)

- Review the Luke light/dark route owner, ordinary Mechanical Throne prerequisites, dual-boss clear gate and raid material filter against the real runtime Script.pvf.
- Confirm PHASE-2 `[LOCKOUT TYPE]` 4/5/0 = light/dark/shared: a route commits only on accepted entry, survives rejoin and group move, resets per epoch, and shared energy dungeons are never gated.
- Confirm `map/luke_normal/light_altar/55946.map` (dungeon 5104) spawns two bosses 56699/56698, so the >1 blocking-boss gate blocks the premature clear; dark altar 55953 keeps a single boss.
- Confirm the four Luke raid materials 10159142/10159143/10159623/10159649 are `[material]` and covered by the filter; light-altar essence 10159404 is `[quest]` and stays outside it.
- Document limitations without inventing rules: Mechanical Throne admission uses the persisted lifetime `DungeonPermission.ClearState` because sequence 42 and `etc/` carry no reset/day evidence; the material filter suppresses all `[material]` in the multiplayer Luke raid, which is broader than the named materials.
- Add `dungeon_luke_review_regression_test.go` and the limitation comment in `dungeon_luke_normal_rules.go`. Changed scope stays Luke source/test; no deployment, database or runtime asset change.

## 2026-10-11 — Online repository acceptance handoff

- Record source import commit 22960444f648a4f2f5bcc7b8a3fbe7086eebc5cc as merged into master.
- Add Docs/ACCEPTANCE_HANDOFF.md with previously accepted items, exact acceptance boundaries and merged source status.
- Preserve all pending GUI items and r98 deployment/paused-task boundaries; no new code or runtime changes.

## 2026-10-11 — Initial Go server source import

- Import complete 3,356-file D22/D23 source closure without changing source bytes.
- Add Windows BAT and POSIX shell build entrypoints with ignored dist outputs.
- Add Linux/Windows amd64 GitLab builds and core tests; preserve SAST include.
- Include example configuration with placeholder password; exclude runtime assets, credentials, player data and binaries.
- Record candidate/online version boundaries and pending acceptance in Docs/SOURCE_IMPORT.md.
- Build organization reference: rewio/ServerS4A21 commit 1f4a4e80610c767278008cd666588cb9a9629e34.

## 2026-10-11 — D33 latest user acceptance and volcano priority

- Close original groups5/12 on direct user confirmation; preserve groups1/2/3 and additional3. Keep original4/9 untested and original11 unreproduced.
- Prioritize Anton phase-two black-volcano barrier remaining at0%; separately track phase-one settlement return, squad board occupancy, adventure friend character selection and temporary-town/abyss admission.
- Preserve r98 deployment and UI pause until2026-10-12 01:30 HK; source tests do not prove native physical barrier removal.
## 2026-10-15 — User-native acceptance and server-only conflict reconciliation

- Record user acceptance of DOC045, DOC096, DOC100, DOC106, DOC118, DOC120, DOC127 and the GM experience behavior DOC135. DOC045 already exists in master. Reconcile MR21/25/27/30/34 for the accepted game changes only; preserve the independent GM repository boundary.
- DOC098 is explicitly untested after clarification, not accepted. DOC087 remains untested. DOC029 repeat upgrades, DOC066 immediate blue-lock display/72-hour yellow-lock delay, and DOC075 cross-character crystal behavior remain open. Other unmentioned candidates are not implicitly accepted or merged.
- GM is switched to the user-selected Cec1c/DNF90GM as a separate system. Conflict resolution, tests and actual remote merge outcomes are tracked in external receipts; this entry does not predeclare those operations completed.

## 2026-10-15 - MR41 accepted integration with MR42

- User confirmed personal and account cargo, the last slot and the full-cargo notice all pass native acceptance; the earlier last-slot report was withdrawn after retesting.
- Resolve the inventory Owner conflict by retaining both cargo settings and monster-card settings. Align the mixed-grade card move regression with MR41's incompatible-stack rejection and verify both stacks remain unchanged before moving to an empty slot.
- MR42 was already accepted and merged. This integration does not include unrelated unaccepted candidates, embedded GM code, runtime configuration or player data. Validation and exact merged-tree evidence are recorded separately.


## 2026-10-15 - User acceptance of MR23

- User explicitly accepted MR23 and authorized merging. Updated issue document also marks skill-lock and CD persistence items accepted. Preserve the remaining independent quick-cast/attack-type issues.
- Reconcile only changelog history; preserve skill option bytes, existing save guards and diagnostic limits.

## 2026-10-15 - Updated issue document accepts female-priest level-15 continuity

- Updated user document marks the female-priest level-15 mainline/job-change report accepted. Merge MR37 regression coverage; it adds tests rather than a new runtime fix. Other quest and experience reports remain open.
- Integrate the pinned MR41 cargo-capacity and MR42 monster-card upgrade/compound diffs onto deployed f9bcd3f for user acceptance only. Keep the existing gameplay candidates and independent GM private API. Do not merge or publish these unaccepted requests into master.
- Resolve overlapping inventory settings fields by retaining both consumers, and preserve forging request fields alongside cargo validators. Combine purchased cargo sort capacity with existing locked-slot exclusions; add a combined regression.
- DOC029, DOC066, DOC075, DOC087 and DOC098 remain unaccepted. This release does not claim to fix the latest enchant/forge UI feedback. Guild MR33 and historical embedded-GM MR2 are excluded from this incremental deployment.
- MR42 requires client COMMAND reception for op603/op414; record the exact client compatibility delta and deployment/validation results in external release receipts. No PVF change or manual player asset edits are planned.
- Combined regression initially exposed fixture assumptions: the existing cargo lock move lacked purchased capacity, and the card test expected same-ID incompatible stacks to swap despite MR41 rejecting that case. Supply real purchased capacity in the fixture and assert rejection leaves both card grades unchanged; retain subsequent empty-slot movement and sorting checks.

## 2026-10-15 - Persist the requested 72-hour equipment unlock delay

- Unlock requests now persist an absolute deadline and acknowledge 259200 seconds instead of clearing protection immediately. Repeated requests retain the deadline; cancellation restores the active lock. Missing or malformed legacy deadlines fail closed.
- Serialize item-lock mutations through the existing character-item transaction. Login snapshots derive remaining time from the deadline; inventory, collectbox, limited-cube, NPC sale and dungeon discard protections share expiry semantics. No player records are migrated or manually edited.
- Keep success-prefixed 267/268/269 responses. Pending requests do not send the class0 removal notice early. Expired pending locks cease blocking operations and disappear from login snapshots; native online countdown expiry remains to be accepted.
- The old implementation fails the pending-state regression; targeted tests cover retries, cancel/restart, exact expiry, malformed deadlines, write failure and concurrent slots. Full validation is recorded separately; this candidate is not deployed or user-accepted.
## 2026-10-15 - DOC-067 / new doc 105 blue sort-lock instant icon refresh

- Root cause is client-side and was re-verified offline from the shipped EXE: the inbound admission bitmap (`.rebuild` predicate 0x6E83610, bitmap VA 0x6E92300, one bit per opcode, no class dimension) has bit 0 for 713/714, and the receive entry 0x25239A0 evaluates it before the class switch; the false branch (0x2523F47) returns without dispatching. The class1/713 and class1/714 ACKs were therefore dropped before the client handlers 0x1FAEE80/0x1FAEEE0, so item+0x2aa stayed 0 and the blue lock icon only appeared after re-selecting the character (login op13 row byte 0x60). The same table also gates class0 notifications: 0x00FB (251, lock-list delta) is admitted, 0x00FC (252, unlock notice) is not; this report does not widen the client allowlist.
- Server side now emits, in the same result as the class1 ACK, an authoritative class0/0x000D container refresh for the three DOC-067 lists (main bag / avatar / personal cargo), so the icon refreshes immediately on the currently deployed client. The body is rebuilt by the bridge from the session projection (wallet row 0:0, account-shared slots, equipment quality seed/sockets/emblems); the module placeholder body never reaches the wire and a missing projection skips the refresh instead of falling back to a module snapshot.
- Validation: old-red/new-green after reverting only handler.go + sort_lock.go to HEAD (inventory 4 and dnfbridge 2 cases fail, then all pass; the two files were restored byte-identically, SHA256 545A8840.../9834395A...). gofmt on the changed files, go list, ./internal/modules/dnf/..., ./internal/services/dnfbridge, ./cmd/server/control, go vet and a server build all pass. Independent client-candidate check (E:/90/repair-round3-20261015/client-candidate/DNF.exe, sha256 6e61c361...): exactly two bitmap bytes differ from 1ac12645... (438 and 713/714); the real gate under unicorn over all 65536 opcodes changes only those three.
- Not closed: native GUI acceptance of the instant icon; 252 NOTI remains gated by the client bitmap. No deployment, no PVF change, no player-data change, nothing pushed to a remote.


## 2026-10-15 - Expired unlock notification compatibility

- v101 drops op252, while admitted class0/op251 applies each listed state through the same setter (0x1d46e70). Append a state-zero op251 delta after an expired unlock ACK and legacy op252 notice, preserving object identity and existing clients. The regression fails before this change.
- Correct the op251 comment: it updates listed objects rather than replacing a separate cache. Native expiry UI remains pending acceptance.
## 2026-10-15 — DOC098 附魔门店材料事务

- 按 test15 原生反馈与真实 PVF 配方的 `[input item]`，在门店附魔事务中复用既有材料消费器，消费请求方背包及账号共享固定槽材料；不足或锁定时连同卡片、手续费、经验和耐久回滚。A21 门店本身也未消费材料，不能作为本修复的权威依据。
- 请求无材料槽位，支付方采用与卡片同侧的请求方；这一归属仍是基于现有协议形状的判断。保留现有失败码映射和成功 ACK，宝珠生成分支不变。
- 仓库仅在槽内容确实变化时写回，空 map 与 nil 视为相同。新增自服务/访客内存事务和真实 PVF 回归；红绿原始输出与检查结果见独立 evidence/lane-a 目录，客户端结果窗口不属于本项验收。
## 2026-10-15 - Forging result reception root cause

- Trace the actual v101 COMMAND438 allowlist rejection against a captured successful server result and current x86 UI callback. Preserve the server packet and transaction; prepare a separate minimal client candidate.
- Offline opcode and callback execution evidence is documented in Docs/CLIENT_FORGING_RECEPTION_20261015.md. This does not establish native UI acceptance or deployment.

## 2026-10-15 - Opt-in authoritative reinforcement payment candidate

- Wire the previously retained NPC trailer into a server-derived PVF quote inside the existing character/account asset transaction. Equipment grade, rarity, slot type and current reinforcement level determine gold; NPC conversion rules come from npc.lst and the NPC document, never a client price. Normal upgrade-table materials remain additional.
- Add an explicitly disabled-by-default `DNFBRIDGE_NATIVE_UPGRADE_COST=candidate` path. Require a ready, bound character town scene containing the conversion NPC; stale character generations, missing scenes and foreign NPCs reject without silently charging gold. This proves area membership, not interaction distance; exact-multiple client preflight versus displayed conversion rounding remains unresolved, so this is not deployment-ready.
- Reject currency/shared/target/material/ticket aliases and exclude the reserved conversion slot from protection-ticket scanning. Portable upgrade consumables keep their existing no-gold/material substitution. Project only committed conversion rows and gold balances; quote failures return the existing invalid-material ACK and preserve the diagnostic cause.
- Add authoritative-payment, shared-material late-failure rollback, wallet projection, real-PVF cost and NPC scene regressions. Independent review and targeted validation are in the external round4 evidence; full integrated validation is recorded separately. No deployment, production data edits, PVF changes or native acceptance are claimed.
## 2026-10-15 - 文档段104：组队未解锁的整队拒绝应答

- 现场口径（用户文档第104段原文）：组队时队伍里某个队员没有解锁所选地图，返回城镇的选项直接消失、只能等倒计时退出。服务端那一半的缺陷是"整队进图在提交之前被拒绝时不回包"：队长按进图时 `prepareRuntimePartyDungeonEntry` 逐个成员做同一套前置校验，队员那条返回 `ErrEntryLevelTooLow` 后只写一条 `game-party-dungeon-entry-blocked` 就静默 `return nil`；队员自己按进图时更糟——它那条 op16 已经被回成功 ACK、并异步路由给队长执行，随后同样没有任何回包。两个客户端都停在"等进图"的等待窗口里，队员那边就是"返回城镇选项消失、只能等倒计时"。
- 修法：新增挂起态（角色号 + `characterGeneration`）记录"这名队员自己那条 op16 已被回 ACK、正在等队长那条整队进图流程给结果"。整队进图这条流程上**任何一条提交前拒绝**都按**权威**名单（`runtimePartyMembers(state)` + `onlineGameSession`）解挂并补一条**既有形状**的拒绝包 `{0, reason, 0}`（msg id = op16、普通频道分类，与成功 ACK 同一个 msg id），reason/class 与发起者那条逐字节相同——等级不足 = 14、`InvalidSelectionState` = 0x13、默认 `DungeonUnavailable` = 0x01，全部取自既有映射与既有站点取值，**没有新增包型、没有新增错误码**。实现见 `go-server/internal/services/dnfbridge/party_entry_refusal_before_commit.go`。
- 出口覆盖（都在任何 runtime/场景提交之前）：队员进图前置失败、`prepareCurrentRaidDungeonEntry`、返回城镇原点不可用、进图包前置、深渊派对入场消耗、安徒恩孵化转移、**队员运行时提交失败**、**队长 runtime 提交失败**。后两条是"晚期错误"：队员自己的 runtime 已经提交（失败时被回滚），改动前这里对两边都是静默 `return nil`；现在先回滚（队员挂着活动副本运行时的时候既有静默口径会吞掉拒绝包）、再解挂、再回包。挂起态的清除点也从"队员自己的 runtime 提交"移到"队长自己的 runtime 提交成功之后"，否则队长那条提交失败并回滚队员时挂起态已经没了、补不出包。
- 解锁门禁一个字节不动：等级/疲劳/队伍/门票/前置校验仍然按成员逐个执行、仍然全部发生在任何 runtime/场景提交之前；本次只补"拒绝之后要回包、要解挂"这半步，不放行任何未解锁地图。
- 换角与重复应答：挂起态同时绑 `characterGeneration`（`bindGameSessionCharacter` 每次换角都让 generation 前进一步），换角之后再换回来也取不出旧挂起态、不会把拒绝包发给新角色；take 即清，同一个 op16 只回一包（队员按两次 + 队长按一次 ⇒ 队员恰好两包、队长恰好三包）。
- 复审修正（父代理指出的排队竞态）：解挂那条包是投递到**队员自己的事件循环**上执行的，在队列里等待期间队员可能换角——"投递之前判定 + take"会把拒绝包发给换角之后的新角色，并吃掉不属于这条流程的挂起态。现在角色号 / `characterGeneration` / 队伍投影的资格判定与 take **全部落在 `callGameSession` 的 callback 内**，返回计数只算**真的发出**的那几包（既有静默口径吞掉的、资格不成立的都不计）；"这条拒绝会不会真的回包"的判据抽成 `dungeonSelectRefusalSuppression`，与 `refuseDungeonSelectEntry` 共用一处，避免日志与计数漂移。
- 复审修正（挂起态结束点，父代理要求核实）：`finishPartyDungeonEntryRoutedPending` 按"创建这条队员记录时的（角色号, generation）"做资格判定，只清属于本流程的那一条挂起态——队员换角之后新挂上的路由请求留给它自己的流程处理。（这条流程能走到提交成功本身也说明队员没换角：换角会让队员自己的 runtime 提交失败、整条流程走不到这一步；该判定是防投递并发窗口的兜底。）
- 旧红新绿 A（handler 接线）：单独把 `dungeon_navigation_runtime.go` 恢复到 HEAD（新文件与新用例保留）后，`TestDoc104RoutedFollowerRefusalAnswersBothWaitingClients`、`TestDoc104LeaderRefusalAnswersLeaderAndLeavesIdleFollowerSilent`、`TestDoc104RepeatedEntryAttemptsAnswerOncePerRoutedOp16`、`TestDoc104LateEntryRefusalStillAnswersAwaitingFollower` 四项 FAIL——其中"晚期拒绝"那条的原始输出正是现场形状：`follower` 收到 op16 ACK + op15/op36/op27 选择器页之后**末尾没有任何回包**；`TestDoc104CharacterSwitchDropsTheRoutedPendingAnswer` 仍 PASS（它钉的是解挂函数自身的契约，不依赖 handler 接线）。恢复后全绿（原始日志 `old-red-doc104.log` / `new-green-doc104.log`）。
- 旧红新绿 B（排队竞态，本次复审修复）：把资格判定临时挪回"投递之前"后 `TestDoc104QueuedRefusalDoesNotReachASwitchedCharacter` FAIL（`queued refusal reported 1 delivered refusals, want 0 after the character switch`），放回 callback 内即 PASS（原始日志 `old-red-queued-switch.log`）。
- 本次提交的文件哈希（SHA256）：`party_entry_refusal_before_commit.go` `56C360C4A73CFBFF3EFABA849DDED8CDE4F404056FE6C7A65DEABF88A64D05EA`、`dungeon_navigation_runtime.go` `1D9FF4E1937C543A4F8E22D7490A34A1731C34FE6BADCAA11607691AD6CB340A`、`game.go` `4BC20DA5FC3510B534B849B17032912BD3EB954A568F3660E7093BCF488CD867`、`doc104_party_entry_refusal_test.go` `7B7CF1DC331595348FF8F165A3931F3241C8F863799D3F5C9689B5C82DECEDDC`、`dungeon_entry_packets_test.go` `A8187FA494C00DF7C75BF63B66F4355879816CAB86B95909503AA5C7F80F1922`。
- 既有回归联改：`TestPartyDungeonEntryRejectsEveryMemberBeforeCommitWhenFollowerLevelIsTooLow` 原先钉的是"两名客户端都 0 字节"（= 缺陷口径本身），改为"发起者恰好一包 `{0,14,0}`、没按过进图的队员仍然 0 字节"，并保留"没有 runtime 部分提交""城镇在场没有被移除"两条断言。
- 已验证：`gofmt -l`（改动文件无输出）、`go list -buildvcs=false ./...`、`go test -buildvcs=false -count=1 ./internal/modules/dnf/...`、`go test -buildvcs=false -count=1 ./internal/services/dnfbridge`（整包 PASS）、`go test -buildvcs=false -count=1 ./cmd/server/control`、`go vet ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge`、Windows 与 Linux `go build ./cmd/server/dnf90` 全部通过；原始日志在 E:/90/repair-round4-20261015/party-evidence/。未跑 BAT 部署预检（`deploy/assets/channel_info.etc` 缺席，与既知失败一致，不计通过）。
- EXE 侧核实到这一步（不猜）：当前客户端 EXE（H:/86JP启动器/_dist/YKCN90服务器-玩家客户端-20261005/client/DNF.exe，SHA256 `1ac1264553f8f762e64f2c637c4c26d3bf5ff0fdc60a090ca8b077d452fd4331`）的入站准入位图（VA `0x6E92300`，谓词 VA `0x6E83610` = `movzx eax,[esp+4] → cmp eax,0x5C2 → ja → bt [0x6E92300],eax`）对 opcode 16（0x10）置位、且该表没有 class 维度（同表 251/0xFB 置位、252/0xFC 未置位，与既有结论一致）⇒ op16 回包（成功 ACK 与拒绝共用同一个 msg id）会进入客户端处理器。**未证**：客户端在"已经认为自己选图成功"之后再收到这条 op16 失败时是否一定退出等待窗口——准入位图只能证明包被派发。这一条需要实机抓包或更长的离线 RE，按"协议不明交父代理"上报。
- 边界：未部署、未推送、未合并远端；未改客户端、未改 PVF、未动生产数据；未使用游戏 UI。工作树 repair-round4-20261015/party（分支 repair/r4-party-return，基 0a4d83d737d9a88eb72f1144548f4853968b28ed）。

## 2026-10-15 - 文档段104复审：路由请求令牌 + 安全身份快照

- 复审问题①（跨事件循环裸读会话身份）：`session.selectedCharacterID` / `session.characterGeneration` 由 `bindGameSessionCharacter`（持 `session.dungeon.mu` + `s.mu`）、会话解绑（持 `s.mu`）、`return_select_character`（持 `s.mu` + `session.dungeon.mu`）改写；上一版在队长事件循环里裸读这两个字段没有互斥保证。现在一律走仓库既有的安全快照 `boundGameSessionCharacterSnapshot` / `boundGameSessionCharacterCurrent`（都在 `s.mu` 下读），挂起态本身仍在 `session.dungeon.mu` 下读写，两把锁**不嵌套**（`bindGameSessionCharacter` 的顺序是 `session.dungeon.mu` → `s.mu`，嵌套会构成 ABBA）。发送前的身份复核改在**队员自己的事件循环**里做（`callGameSession` 的 callback 内）。
- 复审问题②（没有请求序号 ⇒ 旧拒绝可能吞新 pending）：改成本次路由请求一个**令牌** `partyDungeonEntryRoutedRequest`（角色号 + generation + 递增 seq）。队员那条 op16 被回 ACK 时在队员自己的事件循环上绑定并拿到令牌，令牌跟着这次路由一起投递给队长那条流程；队长那条流程只用**自己拿到的那个令牌**应答（三者都匹配才取走并回包），整队进图真正提交之后也只结清"还是这个令牌"的那一条。于是"一个 op16 一包结论"逐请求成立：同角色连续按两次进图时，旧那条流程的拒绝不再吞掉新请求的挂起态（新请求由它自己的流程应答）。既有串行化（队长事件循环串行执行各条流程）保证不会并发双答，令牌把"谁该被答"钉到请求粒度。
- 复审问题③（文件头口径）：删掉"所有出口都在任何 runtime 之前"这句与晚期回滚自相矛盾的断言。改为按出口分别写明：普通出口（队员进图前置、raid 冻结、返回城镇原点、进图包前置、深渊派对入场消耗、安徒生孵化转移）都在**队长自己的** runtime/场景提交之前；两条晚期错误（队员 runtime 提交失败、队长 runtime 提交失败）先回滚已提交的队员运行时再回包 —— 所以队员收到拒绝包时不在副本里，也才不会被"已有活动运行时"的静默口径吞掉。
- 旧红新绿（本次两条修复各一次，均逐字节还原：`party_entry_refusal_before_commit.go` SHA256 `B473B04F0E6ED65BDAEBEB04266A81FCCBE297677A1B89007FFB7D8FBDC77B23`）：① 临时去掉令牌比较里的 seq 条件（= 旧的单槽 take）后 `TestDoc104OlderRoutedRefusalDoesNotConsumeANewerRequest` FAIL（`the older request was still answered after a newer request was bound`）；② 临时去掉发送前的身份复核后 `TestDoc104QueuedRefusalDoesNotReachASwitchedCharacter` FAIL（`the queued refusal reported a delivered packet after the character switch`）。还原后 8 个 DOC104 用例全绿。
- 用例改名/新增：`TestDoc104FinishClearsOnlyThePreparedMembersOwnPending`（钉的是已删除的 finish 函数）改为 `TestDoc104RoutedTokenDiscardsOnlyItsOwnRequest`（令牌只结清自己那一次：换角、换回之后的新请求都必须留着）；新增 `TestDoc104OlderRoutedRefusalDoesNotConsumeANewerRequest`。
- 本次提交的文件哈希（SHA256）：`party_entry_refusal_before_commit.go` `B473B04F0E6ED65BDAEBEB04266A81FCCBE297677A1B89007FFB7D8FBDC77B23`、`dungeon_navigation_runtime.go` `C857B31701ABB01716BF8ADF533095493EE7BD3A209FAE1761CF920580E45B72`、`game.go` `FC71C8DC19568C0C44C015D8FBF9B7B48E326A9416BA1FECA20355A84E05A317`、`doc104_party_entry_refusal_test.go` `37587D2187E0233EC3C754BAAA302D7319F1500EE4C53A17842A7051F57A3329`、`party_dungeon_entry_barrier.go` `CCF127EBD9B29B21E1896A51CC149B55416FB92AE57EB517973F69D7CA161C9F`。
- 已验证：`gofmt -l`（改动文件无输出）、`go list -buildvcs=false ./...`、`go test ./internal/modules/dnf/...`、`go test ./internal/services/dnfbridge`（整包 PASS 20.747s）、`go test ./cmd/server/control`、`go vet`（5 包）、Windows 与 Linux `go build ./cmd/server/dnf90` 全部 exit 0；日志在 E:/90/repair-round4-20261015/party-evidence/。
- 边界不变：未部署、未推送、未合并远端；未改客户端/PVF/生产数据；"客户端收到 op16 失败之后如何退出等待窗口"仍然**未证**（离线 EXE 追踪另见 party-evidence/README.md 第 4 节）。

## 2026-10-15 - 文档段104 第二轮复审：take 移入事件循环 + 流程兜底出口

- 复审问题④（take 仍早于投递）：上一版在 `callGameSession` **之前**取令牌，投递排队期间队员又按一次进图（同角色、新 seq）时会先被旧流程取走，而回调只核身份 ⇒ 旧拒绝会打到新请求上。现在 `takePartyDungeonEntryRoutedRequest` 落在**队员事件循环的 callback 内、发送之前**：取不到令牌（换角、或队员已经又按了一次进图）就一包不发，把新请求原样留给它自己那条流程应答。`take` 内部同时核身份（`s.mu` 下的安全快照）与（角色号, generation, seq），回调不再重复判身份。
- 复审问题⑤（只靠 8 个手工出口）：整队进图这条流程的 return 点不止那 8 个（例如 `prepareDungeonRuntime` 早期失败、raid 选图拒绝、深渊派对拦截、pending 回城提交失败、`replayDungeonSelectAck`、op9 预览移除失败…）。现在在请求解码之后对 `routed != nil` 装一个 **defer 兜底**：流程返回时如果这次路由请求还没有被任何出口精确应答（令牌仍挂着），就补一条既有形状的拒绝包（厂商 default `DungeonUnavailable` = 0x01，站点 reason `party_entry_routed_flow_exited_without_answer`）。精确出口先取走令牌 ⇒ 兜底不重复；整队已进图（令牌被 `discard`）、队员又按了一次进图（seq 变了）、换角，都取不到令牌 ⇒ 一包不发。已提交副本（post-commit 发包失败）仍由"已有活动运行时"静默口径保护。
- 旧红新绿（两条修复各一次，均逐字节还原：`party_entry_refusal_before_commit.go` SHA256 `A8BA057FE6EFDA0388A10FB5540BAD6C9821E1A7C8A644C600BDE00FB62C5CA2`）：① 把 take 临时挪回投递之前 ⇒ `TestDoc104QueuedAnswerDoesNotDisturbANewerRequestFromTheSameCharacter` FAIL（`the older answer was delivered although a newer request was bound`）；② 把兜底 defer 临时关掉 ⇒ `TestDoc104RoutedFlowBackstopAnswersOnAnUnwiredExit` FAIL（`timed out waiting for the backstop to answer the routed request`）。还原后 10 个 DOC104 用例全绿。
- 用例调整/新增：`TestDoc104QueuedRefusalDoesNotReachASwitchedCharacter` 的期望从"换角时令牌已被消费"改为"令牌取不到、挂起态原样留着"（与新语义一致）；新增 `TestDoc104QueuedAnswerDoesNotDisturbANewerRequestFromTheSameCharacter`（排队→新同角色请求 bind→旧回调 0 包且新令牌保留）与 `TestDoc104RoutedFlowBackstopAnswersOnAnUnwiredExit`（未精确应答的出口由兜底补包）。
- 本次提交的文件哈希（SHA256）：`party_entry_refusal_before_commit.go` `A8BA057FE6EFDA0388A10FB5540BAD6C9821E1A7C8A644C600BDE00FB62C5CA2`、`dungeon_navigation_runtime.go` `E6A674079B6258A7909C409586A29CE35F4138D582CBD7392274E7FA909D2933`、`game.go` `FC71C8DC19568C0C44C015D8FBF9B7B48E326A9416BA1FECA20355A84E05A317`、`doc104_party_entry_refusal_test.go` `6FD1F2860BC54A1BD9569286473C9F72A921685CCF6EBF5FCDF9A5C1A69E3F8B`、`party_dungeon_entry_barrier.go` `CCF127EBD9B29B21E1896A51CC149B55416FB92AE57EB517973F69D7CA161C9F`。（本文件里 `2026-10-15 - 文档段104复审：路由请求令牌 + 安全身份快照` 一节记录的 `B473B04F…`/`C857B317…`/`37587D21…` 是那一版（提交 `86a03bb`）的历史哈希，不是最终版；上一节 `5f…`/`56C360C4…`/`1D9FF4E1…` 同理是更早版本的历史哈希。）
- 已验证：`gofmt -l`（改动文件无输出）、`go list -buildvcs=false ./...`、`go test ./internal/modules/dnf/...`、`go test ./internal/services/dnfbridge`（整包 PASS 21.096s）、`go test ./cmd/server/control`、`go vet`（5 包）、Windows 与 Linux `go build ./cmd/server/dnf90` 全部 exit 0。


## 2026-10-15 - Candidate quote support boundary

- Reject level-linear equipment, cost discounts, custom upgrade modes and exclusive upgrade materials before deriving an ordinary reinforcement quote. These metadata paths are not implemented by the candidate; presence alone does not prove the previous numeric quote was incorrect. An error aborts the existing owner transaction instead of silently falling back to the old price.
- Isolated red/green coverage includes empty sections, normalized names, nested grade metadata and real PVF items 10194/104000339; ordinary target 28309 keeps its verified four prices. Full integrated validation is recorded against the subsequent frozen commit. The candidate remains disabled by default and is not deployed.


## 2026-10-15 - Routed party request expiry before handler start

- A leader task that expired or was rejected in the event queue never installed the handler's deferred refusal, leaving the follower with ACK only and a pending token. Atomically claim either handler start or cancellation before start; only the latter sends a token-bound refusal using a fresh bounded deadline. A running handler retains ownership and may commit after the dispatch wait expires.
- Capture log identities using the existing locked session snapshot; do not read leader identity from another session loop. Clarify that an answer returning false at its deadline may still finish an already-started write, so false is not permission to retry.
- Old-red/new-green tests cover real queue expiry, leader stopped before/after enqueue, and an already-started handler that passes the deadline then commits both runtimes without a refusal. Existing checks cover delayed writes, duplicate tokens and successful-handler cleanup. Independent review confirms claim mutual exclusion and lock ordering. Follower queue/socket failures remain delivery failures, not guaranteed recovery; native button behavior is still unaccepted. Integrated checks are recorded against the frozen commit.
## 2026-10-15 — Close a routed party peer after its refusal send fails

- A routed member's refusal runs in that member's event callback. A send error was reduced to a log/false after consuming the request token, so it did not reach the member's socket read loop and its normal disconnect cleanup. Close only that peer connection inside the actual send-error branch, preserving the original error and the healthy leader's refusal. Do not retry a potentially partial frame or close on the caller's queue deadline/cancellation alone.
- Six regression roots cover zero/partial writes, the leader's independent reply, healthy refusal, stale sequence/character, queued cancellation and started writes completing after the caller deadline. The same tests fail on cf3943f only at the four expected missing-close leaves and pass on the candidate. All DOC104 tests plus three isolated real-PVF backend replays pass together: 44 passing nodes, zero skips/failures. The send chain can also fail before a socket write; this follows the existing synchronous disconnect policy and is not a claim that every such TCP connection is itself broken.
- Background fixtures only; no native button acceptance, deployment, push or merge. Existing v2 validation/package still describes cf3943f; new integrated validation follows separately.
## 2026-10-16 — 圣物转换271的Lv85与Lv90候选

- 新问题62段：当前服务端没有271处理入口。新增严格19字节请求、按背包内实际转换书与目标装备ID/槽位取PVF的emancipate嵌套段；本批只支持ticket17与20。转换书need material是购买价，转换消耗只取源装备input；不扣购买用红玉髓。传承ticket18的保留上限、其它票种和勇者升级仍开放。
- 书与普通碎片从角色背包扣除，灵魂从账号固定格扣除，并和原位装备替换处在同一结算事务。拒绝锁定、过期、材料不足、不同账号、错误配对、重复请求及不完整规则；不拿角色历史灵魂副本垫付账号材料。新装备按两种书原文清除强化/增幅/锻造/附魔等旧实例属性，保留源封装位，并设角色绑定。专用封装零值投影仅作用于本路径，不改其它物品Bind回落规则。
- 当前EXE的271发送/接收函数与v103、v107、已发布v110相关字节一致；成功回包按消费者读取的消耗记录发送，之后先清旧目标再发权威槽位。v107及v110接收位图都拒收271，配套候选基于v110只置一个位图bit；65536编号比较仅271变化，保留已有788/86/大会/公会修改。EXE/PVF不进入源码树；哈希和验收界限见Docs/ROUND9_RELIC_CONVERSION_20261016.md。
- 自动化覆盖真实Lv85/90资源的注册表处理、角色和账号扣除、回包顺序、封装零值、失败回滚、账号保存失败后的跨聚合回滚，以及回包失败后不重复提交。客户端原生UI、交易显示仍待用户验收；这不是全部圣物转换或全部已知问题完成。
