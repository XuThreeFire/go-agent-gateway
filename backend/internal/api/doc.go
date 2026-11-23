// Package api 负责 HTTP 路由、请求校验与 SSE 连接管理。
//
// 不做：业务编排（在 service）、状态机迁移（在 exec/service）、SQL（在 store）。
// 关键约束：处理函数不得阻塞等待执行结束——创建类接口一律 202 立即返回（D-01）。
//
// 接口清单见 handbook/reference/protocol.md，状态与一致性见 architecture.md。
package api
