# CI 并行与资源配置

流水线允许核心测试、安全扫描和构建并行，六份构建产物保持独立。构建契约检查只执行一次，失败会阻止所有构建。Go 默认 `GOMAXPROCS=2`、`GOFLAGS=-p=2`。

Runner 并行度由维护方按主机实测容量决定。提高 `concurrent` 时同步检查 runner `limit`、每个 build/helper/service 容器上限、CPU、内存、磁盘与业务余量。Docker executor 创建的容器不受 `gitlab-runner.service` 的内存上限自动约束。

在使用 systemd 驱动和 cgroup v2 的主机，可配置独立 slice，再用 `[runners.docker].cgroup_parent` 约束全部 CI 容器。示例两槽预算为每 build/helper 1.5 CPU、2560 MiB，总计 3 CPU、5632 MiB；service 容器另设较小上限但仍计入总预算。该示例须根据业务峰值调整，不表示每台主机都能承载两槽。

出口有限时，把一个专用 host 文件 bind 到所有作业的同一路径，设置 Runner 环境变量 `CI_ARTIFACT_LOCK_PATH`。`upload_serialized.sh` 使用 `flock` 只串行产物步骤，默认等待最多 600 秒，进程退出自动释放；缺文件、超时或上传失败均使作业失败。不要用每槽私有目录代替共享锁，不删除正在使用的锁文件。

变更后读取真实作业容器 cgroup、CPU/内存上限及峰值，检查 OOM、业务响应和磁盘余量。保留变更前配置与恢复说明。测试成功、公开合入、私有累计和服务部署分别记录；本项不自动部署游戏。
