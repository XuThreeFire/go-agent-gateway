# 05 工具声明、验证与错误反馈

目标：理解模型可调用工具的权限来自代码。前置：读懂最小 Agent 的消息回灌。

`backend/internal/tool/registry.go` 定义接口：

```go
type Tool interface {
    Spec() Spec
    Invoke(context.Context, json.RawMessage) (string, error)
}
```

`Spec` 给模型看，`Invoke` 才是真正执行。默认 `Builtins()` 注册 `now` 和 `calc`，配置白名单只能从已注册工具中选择。

## 参数错误与实现错误

- 非法 JSON、除零和未知工具：结构化反馈给模型，让下一次模型调用决定如何修正。
- 工具 panic：内部实现错误，终止当前 Run；用户可见错误不包含堆栈。
- 超时：工具应主动检查传入的 context；仅创建超时 context 不能强行停止忽略 context 的任意函数。

运行：

```bash
go test ./backend/internal/tool -v
go test ./backend/internal/exec -run 'TestLoopFeedsToolErrorBackToModel|TestLoopFailsOnToolPanic' -v
```

在页面发送 `计算 1/0`，查看失败工具步骤。真实模型可能纠正表达式或解释错误；模拟模式只展示固定流程。

## 安全的扩展练习

阅读 `builtin.go` 中的严格参数解码、表达式长度限制，以及注册表统一结果截断。不要将任意 Shell、任意路径读写或任意 URL 请求作为新手工具。

下一步按[新增工具教程](../development/tools.md)注册 `CourseTool`。该工具只查询虚构课程，能够练习完整扩展流程而不访问个人资料。验收标准：参数错误能被拒绝，配置禁用后模型拿不到该能力，成功结果能进入第二次模型请求。
