# 技能命令、查看资料刷新与构建说明

原构建说明对应 `scripts/client/gameplay-refresh`。以下保留该工具的基线说明；称号入口的后续修复另见 `Docs/TITLE_REQUEST_RETURN_RETEST_20261025.md`。

构建器只接受已审查的 v170 映像，SHA256 为 `e5751354901fc142324d11cfc717e4a958396877f1c0fc545f51645f8e77af55`。它在称号请求标记扩展之上合并三项窄修复：

- 自定义技能命令缺失时，在注册前序列化技能自身的 PVF 默认值；已有显式保存命令仍走原路径。
- mode8 查看资料刷新只在打开的个人信息窗口已绑定同一 actor 和命名空间时更新活动 actor。后台角色目录预热保留辅助 actor 与安静 UI 路径。
- 个人信息面板仅在普通 actor 目录没有匹配对象时回退到已初始化的辅助 actor。这为跨场景查看提供武器和已学精通计算。

服务端须在 mode3 发送完整已学技能和当前装备，并为同区域的其他查看者保存最近一次成功查看关系。任一连接换角或断线时清除该关系，发送前重新核对身份、所有权和可见性。

构建入口 `build_candidate.py --baseline INPUT --output NEW_DIRECTORY` 生成映像和精确字节变更回执。验证入口 `verify_candidate.py --baseline INPUT --candidate OUTPUT_EXE --output NEW_JSON` 重建完整映像，检查 Windows 只读映像映射，并在有界原生序列化、窗口读取和 ABI 用例中执行生成的 helper。验证需要 Unicorn 和 Windows；构建器只使用标准库。

全部工作离线。原生检查中的 actor 查找、UI 构造和分配器叶子使用明确夹具。这些检查不建立端到端玩法验收，实机行为仍属独立测试。
