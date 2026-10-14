# GM 已迁往独立仓库

独立 GM 的源码、页面、鉴权、PVF 查询、福利批次和部署入口现位于：

**https://github.com/Cec1c/DNF90GM**

安装与构建见新仓库 README；旧部署迁移见新仓库 `docs/MIGRATION.md`。
服务端仅保留私有 `POST /api/game/admin` v1 契约、账号/角色初始化及游戏邮件/资产事务。
`signupbenefits` 保留原政策表、注册快照和入账回执，游戏不运行 GM 任务或福利扫描。

## 现有部署

仓库迁移本身不会改变正在运行的 GM 或数据库。旧 `gm_enabled=true` profile 在重新生成前会明确拒绝，避免静默丢失 GM 服务配置。
先按迁移说明保留 GM 配置、状态目录、MariaDB 福利记录和回退材料；迁出 GM 后，再移除游戏 profile 的 GM 设置。
游戏 profile 可独立设置 `player_admin_secret` 供外部管理工具调用；管理接口仍保持私有，不加入玩家网关。
不要删除旧福利表、发放回执或用空数据库替代原库。
