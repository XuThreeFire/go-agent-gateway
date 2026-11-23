// Package store 是持久化层：七张表的读写，只做一层接口，不预制多实现（A-05）。
//
// 约定：
//   - 常规读写用 GORM；CAS、唯一索引冲突、ON DUPLICATE KEY UPDATE 用原生 SQL，语义优先于省代码
//   - 不使用外键，因此每个关联字段必须显式建索引（run.task_id、step.run_id、event.run_id）
//   - 不使用存储过程/函数/触发器；token 累加用 UPDATE ... SET x = x + ? 的原子更新
//   - 依赖 go-sql-driver/mysql，DSN 必须带 parseTime=true&loc=UTC
//
// 三个关键方法先写：InsertEvents / CASStatus / ListEvents。
package store
