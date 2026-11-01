# CI 并行与资源配置

流水线允许核心测试、安全扫描和构建并行，六份构建产物保持独立。构建契约检查只执行一次，失败会阻止所有构建。Go 默认 `GOMAXPROCS=2`、`GOFLAGS=-p=2`。

Runner 并行度由维护方按主机实测容量决定。提高 `concurrent` 时同步检查 runner `limit`、每个 build/helper/service 容器上限、CPU、内存、磁盘与业务余量。Docker executor 创建的容器不受 `gitlab-runner.service` 的内存上限自动约束。

在使用 systemd 驱动和 cgroup v2 的主机，可配置独立 slice，再用 `[runners.docker].cgroup_parent` 约束全部 CI 容器。示例为四个调度槽位、单个 build/helper 上限 2 CPU/4608 MiB、所有 CI 容器总上限 3 CPU/5632 MiB；service 容器另设较小上限但仍计入总预算。总上限不是四份单作业上限相加，必须同时启用下述计算门禁并按业务峰值验收。

维护方将三个独立 host 文件 `compute.lock`、`slot0.lock`、`slot1.lock` bind 到同一容器目录，并配置 `CI_COMPUTE_LOCK_DIR`。普通构建持共享门禁和一个构建槽，最多两个并行；核心测试与安全扫描持独占门禁，轻量检查继续执行。门禁默认最多等待 1800 秒，超时失败；普通构建结束即释放门禁，附件上传另行排队。大包核心测试采用 `GOMEMLIMIT=3GiB` 与 `-p=1`，普通构建软目标 1800 MiB。软目标不能代替硬上限，未知大任务应加入计算门禁。

编译对象保存在 Runner 的 `/cache` 下，按项目与保护状态分目录，Runner 的受保护/非受保护缓存卷隔离仍须保留。Go 自身核对编译输入；没有复用测试通过结论，每次验证和构建命令仍执行。观察缓存磁盘增长并按引用与既有清理规则维护，不上传整个编译缓存作为 artifact。

出口有限时，把一个专用 host 文件 bind 到所有作业的同一路径，设置 Runner 环境变量 `CI_ARTIFACT_LOCK_PATH`。`upload_serialized.sh` 使用 `flock` 只串行产物步骤，默认等待最多 600 秒，进程退出自动释放；缺文件、超时或上传失败均使作业失败。不要用每槽私有目录代替共享锁，不删除正在使用的锁文件。

变更后读取真实作业容器 cgroup、CPU/内存上限及峰值，检查 OOM、业务响应和磁盘余量。保留变更前配置与恢复说明。测试成功、公开合入、私有累计和服务部署分别记录；本项不自动部署游戏。
