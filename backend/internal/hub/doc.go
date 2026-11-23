// Package hub 是事件中心，同时承担三件事：分配 seq、批量落库、广播给订阅者。
//
// 不参与业务判断，不 import service。
//
// 三条硬约束（见 handbook/reference/architecture.md）：
//   - seq 只能在这里集中分配，保证同一 Run 内严格递增
//   - 锁内只做内存操作，落库在锁外批量进行（阈值由配置指定，D-06）
//   - 订阅 channel 满时直接踢掉该订阅，绝不反压到执行器
//
// 补发顺序：必须先 Subscribe 再读库，反序会在窗口期永久丢事件（A-03）。
package hub
