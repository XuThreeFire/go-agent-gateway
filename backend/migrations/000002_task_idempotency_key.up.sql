-- 000002_task_idempotency_key.up.sql
-- 补上「建会话」这一层的幂等能力。
--
-- 背景：原设计把 idempotencyKey 只落在 run 的 uk_task_idem(task_id, idempotency_key) 上，
-- 但 POST /api/tasks 每次都新建 task，新 task 的 id 不可能与已有记录相同，
-- 于是这个唯一约束对新会话永远成立，等于没生效。真正要去重的是「客户端 + 幂等键」。
--
-- 执行（库需自行创建，不由本文件负责）：
--   mysql -h 127.0.0.1 -P 13306 -u <user> -p <dbname> < backend/migrations/000002_task_idempotency_key.up.sql
--
-- MySQL 的唯一索引允许多个 NULL，所以不带幂等键的会话不受影响。
-- 由于是 DDL，执行前建议先确认 task 表数据量（当前为开发库，可直接执行）。

ALTER TABLE `task`
  ADD COLUMN `idempotency_key` VARCHAR(64) NULL COMMENT '建会话的幂等键，同一客户端内唯一；重复提交直接返回已有会话，不新建' AFTER `title`,
  ADD UNIQUE KEY `uk_client_idem` (`client_id`, `idempotency_key`) COMMENT '建会话幂等约束：同一客户端同一个键只对应一个会话';
