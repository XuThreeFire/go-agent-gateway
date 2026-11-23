# 接入模型与新增协议适配器

如果供应商兼容 Chat Completions，通常只需添加模型配置，无需改执行器。详细步骤见[真实模型教程](../tutorial/03-models.md)。测试应确认路径、消息角色、流式结束、usage 和工具参数分片，不能只验证普通文本。

如果协议不同，实现 `backend/internal/llm.Client`：

```go
type Client interface {
    Target() Target
    Stream(context.Context, Request, func(Chunk) error) (Result, error)
}
```

新增文件实现协议转换，在 `llm.New` 根据 provider 选择该实现。执行器只使用这个接口，因此不需要了解供应商的事件字段。

必须处理：context 取消、网络超时、完整结束判定、部分工具参数按调用身份归并、工具请求与回灌消息、供应商用量，以及已开始输出后禁止自动重试。

测试使用 `net/http/httptest` 模拟供应商响应，不把真实密钥和付费调用放入单元测试。参考 `client_test.go` 的测试服务器。加入新 provider 后，还应更新配置层支持说明和教程；标签变化本身不代表获得新协议能力。

当前模型快照用于审计路由；重试前会核对关键路由是否与快照一致，配置已更换时明确拒绝，避免同一别名偷偷切换到另一个模型。
