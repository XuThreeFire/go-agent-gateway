// Package exec 是 agent 执行器：拼上下文、调模型、跑工具、判终态。
//
// 不 import api，也不关心有没有客户端在看——这是 D-01（执行与连接解耦）在代码层的落点。
//
// 要点（见 handbook/reference/architecture.md）：
//   - 每轮循环开头查配额，不是启动时查一次
//   - 取消走 context.Cancel，不用共享标志位
//   - first_token_at 只记第一次
//   - panic 必须 recover 并 CAS 转 failed，同时释放信号量
//   - 启动时跑一次 RecoveryScanner，把 pending/running 批量置为 interrupted（D-02）
package exec
