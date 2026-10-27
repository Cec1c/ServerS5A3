# 服务端基础内存与数据库写入优化

本改动减少启动扫描残留和地图/PVF目录的重复分配，并为控制器显式接入仓库已经具备的数据库写入开关。地图仍全量预载，未开启地图淘汰，也未开启低内存流式预载。

## 默认行为与启用方式

PVF文件表中重复名称/目录按字符串偏移共享，地图原始Token改为内部紧凑表示；公开查询仍返回独立完整副本，保留值、顺序、原文和源码位置。上述两项随构建生效，不需启动参数。

预载成功、开放监听前清理共享PVF归档的可重建文本和解压缓存，并执行一次`debug.FreeOSMemory`。归档字节、索引和已解析业务目录保留；后续读取可重建缓存。不会在每次玩家操作中强制GC。本改动减少预载后的保留量，不能避免此前已经发生的预载峰值，也不限制以后缓存增长。

## 数据库启动参数

批量INSERT与背包/穿戴差量保存的底层能力已经在主干，本提交只给控制器增加受校验的显式参数映射，不重复引入数据库引擎代码。控制器会清理继承的`DNFBRIDGE_*`，因此通过`DNF90Control`或其BAT启动时使用以下左列；直接启动游戏程序时使用右列。

| 控制器参数 | 游戏程序参数 | 取值与未设置时行为 |
| --- | --- | --- |
| `DNF90_MYSQL_INSERT_BATCH` | `DNFBRIDGE_MYSQL_INSERT_BATCH` | 1至1000；控制器未设置时使用实例配置，旧实例缺省200；直接启动游戏程序仍按其自身默认值 |
| `DNF90_INVENTORY_SAVE_SCOPE` | `DNFBRIDGE_INVENTORY_SAVE_SCOPE` | `collection`或`slot`；未设置保持原完整容器保存 |
| `DNF90_EQUIPMENT_SAVE_SCOPE` | `DNFBRIDGE_EQUIPMENT_SAVE_SCOPE` | `collection`或`entry`；未设置保持原完整容器保存 |

控制器对非法值不下发覆盖；不会让原始`DNFBRIDGE_*`继承值绕过清理。差量保存仍由既有事务和完整仓储快照确认实际变化，无法取得增量时走原完整保存回退。没有数据库升级、表结构迁移或降低持久化保障。

Windows CMD示例（在官方部署目录中执行，示例不是默认强制设置）：

```bat
set "DNF90_MYSQL_INSERT_BATCH=200"
set "DNF90_INVENTORY_SAVE_SCOPE=slot"
set "DNF90_EQUIPMENT_SAVE_SCOPE=entry"
set "GOGC=50"
set "GOMEMLIMIT=3GiB"
call deploy\windows\control.bat start
```

直接启动程序时改用上表右列变量；Linux shell使用`export 名称=值`，systemd使用服务的`Environment=`。不要把控制器的`DNF90_*`直接当游戏程序参数。

## Go内存参数与2GB边界

本提交不在程序中写死`GOGC`、`GOMEMLIMIT`或`GOMAXPROCS`，也不提交本机BAT/运行配置。`GOGC=50`、`GOMEMLIMIT=3GiB`只是一个部署示例。`GOGC`越低通常回收越频繁；`GOMEMLIMIT`是Go运行时软内存预算，不是进程RSS、启动峰值或整机内存的硬上限，预算低于存活数据和运行时开销时会增加GC压力。

“服务启动后占用低于2GB”与“2GB整机承载系统、游戏服、GM和数据库”是不同指标。独立的低内存专项还需要流式预载、有界PVF缓存与实际平台验证，不能仅把本例参数改成2GiB便视为完成。

## 验证与影响范围

单元测试覆盖紧凑节点还原/副本隔离、重复偏移与编码/路径/首条索引、缓存释放后重读及并发读取、控制器显式配置与继承清理。真实PVF专项逐张比较地图全部公开字段、逐条比较PVF元数据/查找，并确认启动缓存清理后已解析的奖励与物品目录保持。

内存和速度收益依赖资源与负载，不能把不同时段的堆、RSS、工作集相加成节省量。本提交不修改EXE、DLL、PVF、奖励规则、频道表、GM实现或数据库业务。
