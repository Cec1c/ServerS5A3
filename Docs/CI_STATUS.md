# CI执行状态与处理

2026-10-11（香港）。邮件对应[流水线164381](https://gitgud.io/16ww/servers5a3/-/pipelines/164381)，master提交cba767e123e00e3112598012e61f5485b14640ea。

Windows作业570311与Linux作业570310均failed，failure_reason=stuck_pending_no_matching_runners；trace为空，构建脚本未执行。core-tests/semgrep-sast均skipped。没有远端编译错误证据，不能将该流水线写为通过。

项目51684原shared_runners_enabled=false、group_runners_enabled=true，可用Runner列表为空。本轮经现有仓库授权将项目shared_runners_enabled设为true并GET回查。之后仅出现站点共享Runner1/gitlab-multi-runner：status=stale、online=false、active=false、paused=true。项目已允许共享执行器，但当前执行器暂停且离线。

下一步需要站点管理员恢复共享Runner，或为本项目注册可运行Linux Docker executor的Runner；项目设置不能启动站点执行器。现有CI在Go1.26.5 Linux容器中分别交叉构建Windows/Linux，不要求Windows主机。未修改站点全局执行器、未安装本机服务、未删除或绕过build/core-tests/SAST门控。

本机final11核心8220 PASS/0 FAIL/725环境SKIP、真实PVF84 PASS/0 FAIL/0 SKIP、go list、规定范围vet、Windows/Linux各三个程序构建通过。本机测试和交叉构建不能替代远端CI执行；产物不部署到r98，不上传玩家数据或运行配置。新源码推送后的流水线状态另以本地API收据记录，若pending也不能计作通过。
