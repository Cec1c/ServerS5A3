# V212 双组 Buff 部署配置

V212 客户端已经包含双组 Buff 协议，服务端也实现了对应的 8 字节 1426 请求。只有 `official-24.9` 与该协议成对使用时，才通过 `DNFBRIDGE_BUFF_GROUP_SYNC=on` 启用显式组号。未配置时生成器不注入该变量，服务端保持原有的关闭行为；生成器只接受精确的 `on` 或 `off`，不会把该功能设为全局默认值。

## 协议配对

- 配对客户端：V212 EXE SHA256 `69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544`。
- 版本：`client_version` 必须是 `official-24.9`。
- 请求：1426 正文为 `B7, group:u8, skill:u16, slot:u8, list:u8, index:u16`，共 8 字节；`group` 只接受 0 或 1。
- 该开关与 V212 客户端是一个兼容单元。旧客户端应保持开关关闭，不能与启用该开关的 V212 环境混用。

开关未配置或为 `off` 时，服务端把 8 字节 1426 当作非法请求消费，不保存组号，也不向该组回投影。角色或场景重建时使用的是旧的完整快照投影；被拒绝的登记没有持久状态可恢复，客户端临时显示的双组选择会被空快照替换。因此，配套 V212 客户端需要显式设为 `on`。

`deploy/templates/buff-group-v212.compatibility.json` 是配置片段。已有实例应把其中的字段合并到 `runtime/docker/instance.json`，保留密码、地址、PVF、频道表、镜像和其他兼容设置；直接用片段覆盖整个实例文件会使生成器失去这些字段。

```json
{
  "client_version": "official-24.9",
  "runtime_compatibility": {
    "DNFBRIDGE_BUFF_GROUP_SYNC": "on"
  }
}
```

若实例已有 `runtime_compatibility`，只在该对象内增加 `DNFBRIDGE_BUFF_GROUP_SYNC`，不要覆盖其他兼容项。需要关闭时改为 `"off"`；删除该项则回到“不注入、服务端默认关闭”。

## Docker 更新

在仓库根目录使用原实例参数重新生成配置。默认运行目录是 `runtime/docker`；使用过 `--runtime-dir`、`--project` 或 `--image` 时继续使用原来的值：

```sh
python3 deploy/docker/manage.py prepare
grep '^DNFBRIDGE_CURRENT_CLIENT_VERSION=\|^DNFBRIDGE_BUFF_GROUP_SYNC=' runtime/docker/server.env
```

`server.env` 是生成文件，修改应落在 `instance.json`。生成后只重建游戏服务容器，数据库容器和卷保持不动；项目名和路径按本实例实际参数替换：

```sh
docker compose -p servers5a3-local -f runtime/docker/compose.json \
  up -d --no-deps --force-recreate server
```

该命令只更新 `server`，不使用 `down -v`，也不会替换数据库卷。服务启动后可在容器内核对实际环境：

```sh
docker compose -p servers5a3-local -f runtime/docker/compose.json \
  exec server printenv DNFBRIDGE_CURRENT_CLIENT_VERSION DNFBRIDGE_BUFF_GROUP_SYNC
```

直接启动服务程序时，在现有的配套启动环境中增加：

```sh
DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9 \
DNFBRIDGE_BUFF_GROUP_SYNC=on \
<server command and arguments>
```

## 验证边界

离线机制验证覆盖 Docker 生成器对 `on`、`off`、未配置及非法值的处理，并检查新增行之外的环境文件保持逐字节一致。隔离 Linux 中的 Buff 域与桥接专项共 42 个主测试、81 个含子测试节点通过，覆盖 8 字节请求、两组独立持久化、人物重建恢复、发送失败重试及关闭路径。该记录使用内存事务仓储，未作为真实 MySQL 集成结果。

本次没有捕获搭建者现场的双组操作或原始报文，也没有运行 Docker、启动游戏或进行玩家实机验收。因此上文确认的是生成器行为和可复现的离线机制，不是对某个已搭建实例端到端结果的声明。
