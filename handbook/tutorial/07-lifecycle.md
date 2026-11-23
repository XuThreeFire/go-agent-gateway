# 07 取消、重试与重启中断

目标：理解 Task、Turn、Run 与浏览器连接各自的生命周期。前置：完成 SSE 教程。

Task 是会话，Turn 是一次用户输入，Run 是一次尝试，Step 是模型或工具调用。一个 Task 同时最多有一个活跃 Run；重试会新建 Run，不把旧 Run 从终态改回运行中。

```text
pending → running → completed / failed / aborted / interrupted
```

## 三个实验

1. 发送较长输入，然后点击停止。取消通过 context 传到模型读取；运行中的执行器完成必要步骤记账后写 aborted 终态。
2. 模型配置故意缺密钥，观察失败；修复配置后重启，重新打开会话并重试。旧 Run 保留失败状态，新 Run 的 attempt_no 增加。
3. 在模拟输出期间执行 `docker compose restart backend`，重新打开会话。正常关闭可能产生 aborted；强制终止留下的 pending/running 会在下次启动扫描中变成 interrupted。两者都可以手动重试。

生产系统不要拿真实业务工具做强制终止实验。本项目默认工具为只读示例，适合本地练习。

相关代码：`service/service.go` 编排用例，`exec/executor.go` 跑循环和收尾，`store/run.go` 用条件更新保证终态竞争只有一个赢家。

## 为什么不自动重跑

进程可能在工具已经产生副作用、但结果还没持久化时退出。自动重跑可能重复付款或重复写入。手动重试同样不能自动消除副作用；新增写入型工具前，必须设计工具自身的幂等键与业务状态查询。

重启不会丢失签名身份：原生开发密钥保存在被忽略的 `data/session.key`；容器模式通过环境变量提供稳定密钥。丢失或更换密钥会使旧 Cookie 失效。

练习：运行 `go test ./backend/internal/exec -run 'TestCancel|TestRecover' -v`，指出取消传播与数据库状态更新为什么都需要验证。
