## 本段最终CI与停止结论（528，香港2026-10-13 00:30:45独立回读）

用户合入MR1的b181abd1已在云Runner1615完成[流水线164976](https://gitgud.io/16ww/servers5a3/-/pipelines/164976)：pipeline为success，Linux build571415、Windows build571416、core-tests571418、semgrep-sast571417四项均实际success，开始/完成时间完整、均由1615执行。再次独立GET pipeline/jobs/runner均HTTP200，Runner online=true、paused=false；不是仅Runner在线或允许失败导致的pipeline绿色。旧164936/164963核心测试失败记录保留。

四路已完成交接且关闭，02:00恢复heartbeat已回读PAUSED，后续需用户明确继续。线上游戏仍为18393b1d，合并b181及独立候选未部署；本轮只同步交接文档。可验收/未修复清单以本文件523/524节为准；账号金库候选完整仍有3项FAIL，P66金币与P57按钮尚未定位，团队掉落候选未集成。新流水线成功不关闭玩家尚未通过的报告。

## 2026-10-13 最终代理交接及停止回读（524）

四路510/517/516/522均已完成交接并停止，各自报告无遗留自有检查进程。各报告仅作为其fork时点的证据，不覆盖当前远端master/实际线上版本。

- Hilbert510：R基线6PASS/7FAIL、409同样6/7、439完整控制10PASS/3FAIL，合法容量/资源边界子范围9PASS/0FAIL。三项未通过是不同stackable_type、expire_time、expire_unix被合并；尚未证为玩家现场根因。实际PVF417是[waste]，合法消耗品3..8或65..120；材料页对照用3277，12个合法共享晶体/灵魂均没有正容量上限。不得沿用“417是材料121..176”错误前提或把完整候选写GREEN。报告在player-issues36/raid/evidence/account-vault-normal-route510/HANDOFF-REPORT510.md。
- Turing517：已定位截图红三角在好友列表行、真实行命中→选中→事件0x18→条件式本地popup_menu，804只是一条条件分支；三角具体图帧与命中绑定尚未完成，无生产改动。报告在social-ui/evidence/adventure-red-button517/HANDOFF517.md。
- Pauli516：list0/list12所有已实现分类的生产Sort，金币余额和金钱行保持4321；真实客户端晶体整理opcode/list/category及刷新因果仍缺，无产品RED或生产修改。报告在persistence/evidence/crystal-sort-gold516/HANDOVER-516.md。
- Schrodinger522：b181 control整包以及新增v56/protocol/UDP测试实际exit0，独立路径边界回退前合并树RED、合并后GREEN。Linux云CI结果另以实际回读为准；未执行完整新base bridge/其他规定检查。scripts/ci/sync_repositories.py仍把git私密stderr带入SyncError和CI输出，存在凭据URL暴露风险，未修；恢复时优先最小去除原始stderr并测试。报告在raid/evidence/merged-master-review522/HANDOFF-522.md。

截至香港2026-10-13 00:26:05，164976的两个build成功、core-tests和semgrep-sast仍running，未确认全四项success。526回执保留；个人失败邮件开关未作额外改变。凌晨02:00恢复heartbeat已回读PAUSED；代理停止，线上游戏与云Runner保留运行。稍后的最终CI回读若有结果另附，不改旧失败记录。

## 2026-10-13 云Runner、合并及本段停止（523）

云117.72.214.66已安装Docker并实际启用项目Runner1615，locked且仅项目51684，Docker executor、非privileged、concurrent=2，官方Go1.26.5/helper19.4.0/semgrep6镜像已校验并实际运行。本机Docker仍停止。注册凭据只在云root0600配置，文档及仓库没有明文凭据。

旧流水线164936与164963两项build及semgrep-sast均success，core-tests为script_failure。私密trace仅内存读取：当前已定位TestResolveBinaryInstallStateRejectsEscapingPath、TestValidateInstance两个公开路径校验失败，未保存或散列私密trace。Runner离线/未匹配的问题已解决，不能把旧流水线写成整体通过。

用户已合并MR1，merge commit b181abd1，本地ff-only同步。合并含portablePath/absoluteOrVolumePath/safeManifestPath处理；新流水线164976已在Runner1615实际开始。截至香港2026-10-13 00:21:45，两平台build均success，core-tests和semgrep-sast仍running；完整CI尚未通过确认。后续回读如有新结果，另附精确时点，不覆写历史失败。

用户要求本段完成后停止；02:00恢复测试heartbeat已PAUSED。云Runner服务保留运行，站点已经调度的CI正常继续；代理不继续轮询或改代码，后续用户明确恢复时先核164976最终结果。个人本项目failed_pipeline通知此前已回读false，不改变其他项目或全局通知。

## 2026-10-12 当前源码同步及CI边界（255）

Go源码20b0c911已同步远端master，3443文件已部署到正常隔离QA并完成启动；日常r98不变。其源码流水线164662保留四项既有build/core-tests/SAST定义，四job均未分配Runner、未开始，已取消并独立读回全canceled。个人本项目failed_pipeline通知已读回false。没有远端编译/测试通过结论，也未恢复Runner。

当前用户已停止本机Docker/Runner路线，后续等待其提供云服务器；不再安装、启动Docker、注册Runner或重启系统。本次仅根Docs及changlog同步，按现有workflow规则应跳过普通文档流水线，实际远端触发结果另以同步收据记录，不使用skip-ci标记或删减门控。

以下为历史检查点，以本节最新源码/QA状态及后续同步收据为准。

## 2026-10-12 本机Runner路线停止，等待云服务器

用户已要求停止在本机安装Docker，后续将提供云服务器。停止指令到达前，官方Docker Desktop4.94.0.241994已完成当前用户安装（数字签名Valid、安装退出码0）；现有WSL3.0.1默认版本2。容器引擎创建WSL虚拟机失败，错误为`Wsl/Service/RegisterDistro/CreateVm/HCS/ERROR_NOT_FOUND`；正常停止和WSL关闭后的唯一重试仍失败。Windows存在CBS待重启标记，但未确定根因，也未重启系统。

已停止Docker并核实其Desktop/backend进程不存在，禁用安装生成的当前用户登录自启动；软件保留，未卸载。没有创建或注册项目Runner，没有开始新的远端CI作业。后续改在用户提供的云服务器部署Linux容器执行器，不能把本机安装成功称为Runner恢复。现有build/core-tests/SAST及个人失败邮件关闭设置保持，已验证/运行Go源码仍为31e2b51，日常r98未改。

## 2026-10-12 08:17 持续失败邮件处理

08:24推送后实查：配置提交`5f200362`已同步远端master，正常push确实创建[流水线164607](https://gitgud.io/16ww/servers5a3/-/pipelines/164607)，包含两项build、core-tests及semgrep-sast，证明CI配置变化仍进入既有四job。四项都没有Runner/started_at；仅在确认尚未执行后取消，并GET读回canceled，避免继续空排队。共享Runner仍paused/offline/stale，失败邮件开关再次读回false。这次配置触发成功不代表Runner或远端构建成功；下一次普通纯Docs推送应由新workflow过滤，真实触发结果保存至本地交接收据。

项目51684（16ww/servers5a3）已将当前用户的本项目通知改为custom、failed_pipeline=false。PUT后独立GET读回一致；账号全局通知仍为participating。只显式提交这两个通知字段，其他事件保持原有或默认值；不承诺custom的所有事件语义与participating完全相同。项目没有活动pipeline email integration，列表为空、pipelines-email查询404。该开关控制此项目之后的普通失败通知，已排队或已发送的邮件不能撤回。[GitLab通知API](https://docs.gitlab.com/api/notification_settings/)

文档流水线164597在各job尚未获配Runner、没有started_at时取消，GET回读为canceled；历史失败流水线保留。通知过滤和取消空排队不代表远端CI执行或通过。

本次增加workflow创建规则：已有普通分支的纯根Docs/README/AGENTS/changlog推送不创建流水线。完整go-server子树（包括依赖、测试及嵌入资源）、构建脚本、CI及deploy输入变化仍创建流水线；源码与文档混合推送也保留。tag、首次新分支和web/api/trigger/schedule等显式入口保持。以后新增go-server外的构建模块或不在规则中的输入，须同次更新changes路径。[GitLab workflow规则](https://docs.gitlab.com/ci/yaml/workflow/)

服务端静态CI Lint基线与候选均valid=true、errors/warnings为空；更严格的全定义比较发现build/core的隐式only归一化差异，原始结果保留。随后真实服务端dry_run模拟基线与候选均产生相同的四个实际jobs：Linux build、Windows build、core-tests及semgrep-sast，脚本、stage和既有allow_failure策略一致。所有原job/include正文保留；此处验证配置，不称远端编译/测试通过。推送后的真实普通文档触发行为另行核对。

08:14实际API现状：唯一可见站点Runner1仍paused=true、online=false、status=stale，available_runners为空。当前Windows电脑没有已安装的Docker或WSL Linux环境，无法直接运行现有SAST容器。远端CI恢复仍需站点管理员启动共享执行器，或提供本项目专用、可运行Linux容器的Docker执行器；现有Go镜像、双平台构建、核心测试和SAST保持。项目设置及邮件过滤不能启动站点执行器，不新增allow_failure或删除job来制造绿色。日常r98及已验证/运行源码31e2b51不变。

## 2026-10-12 邮件流水线164529实际核查

GitGUD流水线164529对应58145b08。Windows/Linux build两作业均stuck_pending_no_matching_runners，runner与started_at为空、trace为0字节；core-tests/SAST跳过。项目shared/group runners已启用，但API可见唯一instance runner处于paused/offline/stale，未执行脚本。不能把邮件称为编译错误，也不能把本机组合56 GREEN称为远端CI GREEN。新源码提交的流水线另行回查；不绕过现有门控或修改全局Runner。

# CI执行状态与处理

## 当前状态：2026-10-12（香港）

香港 07:38:33 的只读 API 回查：纯文档提交 `c7b748b3` 的 [流水线 164588](https://gitgud.io/16ww/servers5a3/-/pipelines/164588) 仍为 pending。Windows 作业 570675、Linux 作业 570674 的 Runner 与开始时间均为空，core-tests/SAST 仍为 created。该结果只对应此提交和检查时点；之后文档提交的新流水线另行核对，运行及完整已验证源码仍为 `31e2b51`。

邮件截图对应提交 `58145b08` 的 [流水线 164529](https://gitgud.io/16ww/servers5a3/-/pipelines/164529)。Windows 作业 570581 和 Linux 作业 570580 均失败，`failure_reason=stuck_pending_no_matching_runners`；Runner 与脚本开始时间为空，trace 为 0 字节，构建脚本没有执行。core-tests 和 SAST 被跳过。

最新已验证源码 `31e2b51` 的 [流水线 164570](https://gitgud.io/16ww/servers5a3/-/pipelines/164570) 也已失败。香港 07:03:39 的 API 回查确认 Windows 作业 570646、Linux 作业 570645 为同一失败原因；07:05:15 的回查进一步确认四个作业 trace 均为 0 字节。两项 build 没有 Runner、没有开始时间，core-tests/SAST 被跳过。此前 06:38 的 pending 是历史快照，不能继续作为当前状态。

07:05 的项目及执行器回查：项目共享和组 Runner 都已启用，可见唯一站点执行器仍为 `paused=true / online=false / status=stale`。需要站点管理员恢复共享执行器，或为项目提供可运行现有 Linux Docker 构建环境的 Runner。项目开关已经打开，尚没有可用执行器；现有 build/core-tests/SAST 门控保持。

本机组合 56 核心 **8560 PASS / 0 FAIL / 774 环境 SKIP**、指定真实 PVF 专项 **396 PASS / 0 FAIL / 1 独立资产 SKIP**，go list、规定范围 vet、Windows/Linux 各三个程序构建通过。这些结果支持候选源码验证，不代表远端 CI 通过；远端没有执行编译或测试，当前也没有远端源码编译错误证据。

隔离候选已正常启动并完成新的客户端登录入城，日常 r98 保持不变；火山实机验收状态见 [D33_NORMAL_QA_STATUS.md](D33_NORMAL_QA_STATUS.md)。后续文档提交触发的新流水线另行记录实际 API 状态，不把新建或 pending 记作通过。

## 历史检查点

2026-10-11（香港）。邮件对应[流水线164381](https://gitgud.io/16ww/servers5a3/-/pipelines/164381)，master提交cba767e123e00e3112598012e61f5485b14640ea。

Windows作业570311与Linux作业570310均failed，failure_reason=stuck_pending_no_matching_runners；trace为空，构建脚本未执行。core-tests/semgrep-sast均skipped。没有远端编译错误证据，不能将该流水线写为通过。

项目51684原shared_runners_enabled=false、group_runners_enabled=true，可用Runner列表为空。本轮经现有仓库授权将项目shared_runners_enabled设为true并GET回查。之后仅出现站点共享Runner1/gitlab-multi-runner：status=stale、online=false、active=false、paused=true。项目已允许共享执行器，但当前执行器暂停且离线。

下一步需要站点管理员恢复共享Runner，或为本项目注册可运行Linux Docker executor的Runner；项目设置不能启动站点执行器。现有CI在Go1.26.5 Linux容器中分别交叉构建Windows/Linux，不要求Windows主机。未修改站点全局执行器、未安装本机服务、未删除或绕过build/core-tests/SAST门控。

本机final11核心8220 PASS/0 FAIL/725环境SKIP、真实PVF84 PASS/0 FAIL/0 SKIP、go list、规定范围vet、Windows/Linux各三个程序构建通过。本机测试和交叉构建不能替代远端CI执行；产物不部署到r98，不上传玩家数据或运行配置。新源码推送后的流水线状态另以本地API收据记录，若pending也不能计作通过。

2026-10-12本轮本机验证：常规8227 PASS/0 FAIL/736环境SKIP，相关真实PVF272 PASS/0 FAIL/1单独资源门控SKIP，规定vet及Windows/Linux三个程序构建通过。扩大PVF运行的七个基线断言失败和超时保留。该本机结果不能替代远端流水线，推送后状态以API回查记录。
