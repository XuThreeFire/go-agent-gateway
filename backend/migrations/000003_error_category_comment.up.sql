-- 0003：只更新 run.error_category 的列注释，把接工具循环后新增的 max_steps 写进去。
--
-- 纯元数据变更：不改类型、不改可空性、不动任何数据，MySQL 8 走 INSTANT 算法。
-- 不执行也不影响功能（代码写入 max_steps 不受注释约束），但 SHOW CREATE TABLE
-- 会和 docs/03 的说明对不上——DBA 排障时正是靠这行注释认值域。
ALTER TABLE `run`
  MODIFY COLUMN `error_category` VARCHAR(32) NULL
  COMMENT '错误分类：rate_limit/timeout/protocol/tool_error/quota_exceeded/max_steps；仅失败时有值';
