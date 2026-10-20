# 合并请求 !1 审查（415，2026-10-12）

审查对象：[Add Linux local and private remote testing, v56 token login and fork synchronization](https://gitgud.io/16ww/servers5a3/-/merge_requests/1)。源提交ca2540894d57be02777250918ea63b98a2677088，目标master在本次检查时为f8943040b153082f6e33da4aa921402a8c61858a，共38个改动文件。

结论：主体具备合并条件；建议先修复一处同步错误日志输出，再合并并保留当前Windows/QA配置。当前尚未执行线上合并或部署。

改动包括：增加Linux Docker/MariaDB独立部署和真实频道表提取；为已检查的v56客户端606/610字节登录请求补上upper路由和连接生命周期识别，保持现有令牌校验；增加私网WireGuard与SSH回环bootstrap/API转发、远端启动顺序和每日数据库备份；区分UDP实际监听地址与通告地址；增加跨Windows/Linux相对路径校验，以及GitHub/GitGud只快进同步和GitHub验证工作流。defaultGroupLoginOnly默认关闭，新部署配置选择开启。

该请求没有安徒恩、账号金库、疲劳、技能锁、晶体整理、一觉初始化或P57列表修复，不据此关闭这些玩家项目。新增Windows登录器严格限制v56 EXE指纹bc613e36…，与当前隔离客户端bb42898c…不是同一资产组合。作者在MR描述中记录了v56远端进图和打怪，但本次审查没有重复该原生验证，也不把作者的记录当当前v97/日常r98已验收。

隔离Git merge-tree试合并退出0、无冲突，fresh仓库API显示mergeable；没有写入R工作树或线上master。对确切合并树26eb5c5b91a22aad67e7bfbde001028580a40c12重新验证：protocol、bridge、control共5882个PASS节点、0FAIL、658环境SKIP；Python部署边界4项及同步集成7项通过；相应vet通过；Windows amd64与Linux amd64服务器编译通过。Docker容器构建/实机/云服务器验收未执行，本机Docker/Runner继续停止。

发现P2：scripts/ci/sync_repositories.py第27行把原始git stderr加入SyncError，第84行打印异常；失败输出可能包含由命令行传入的带URL认证远端地址。已用模拟失败进程和纯模拟认证标记走实际git()错误分支复现RED；准备的最小补丁只保留命令名称和真实退出码，去掉原始stderr，GREEN且7项同步集成回归仍通过。未读取或输出真实凭据、provider原始日志。补丁没有改同步方向、分歧拒绝或push规则，也没有写入MR源分支。

来源流水线164676显示failed；作业详情API返回403，具体原因在本次审查中无法核实，不能声称就是Runner导致或源码失败。新增GitHub工作流不等于恢复GitGud Runner；该请求没有Runner安装/注册实现。

合并前建议将准备好的错误输出补丁纳入源分支，并对新源提交重新确认。合并代码本身不会自动部署现有服务，也不授权启动本机Docker。当前GUI操作与实机测试仍等到香港2026-10-13 02:00，日常r98保持。
