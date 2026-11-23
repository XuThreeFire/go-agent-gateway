# 扩展事件与页面

以新增 `run.notice` 为例，应依次完成：

1. 在 `backend/internal/domain` 定义事件名和明确的 payload。
2. 在执行器相应位置通过 Hub 发布，沿用 Run 内 seq。
3. 在 `web/src/api/types.ts` 和 `events.ts` 加入事件类型与监听。
4. 在 `payload.ts` 校验数据，在 `useSession.ts` 处理展示。
5. 同时测试实时 SSE 与 JSON 历史回放。

页面两种入口共用 `applyEvent`，避免实时和刷新后的渲染语义不同。新增字段优先采用可选字段；未知状态应被明确拒绝，不能自动当作成功。

不要直接在执行器中操作 HTTP，也不要绕过 Hub 单独发送 SSE，否则事件可能无法回放。前端收到终态后要关闭 EventSource，不能把正常收尾当作网络失败。

推荐用 Vitest 驱动事件输入并断言状态变化，用 Go 的 httptest 验证 wire payload，最后在本地真实数据库中核对回放。运行 `make test vet web-test web-build`，完成后更新[协议参考](../reference/protocol.md)。

新增涉及 Task/Run 的接口必须复用资源归属校验；前端隐藏按钮不能替代后端校验。
