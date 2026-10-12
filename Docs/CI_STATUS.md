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
