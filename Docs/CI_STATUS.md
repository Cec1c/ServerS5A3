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
