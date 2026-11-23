-- 000001_init.up.sql
-- 初始表结构。后续变更新增迁移，不修改已经执行的版本；同步 handbook 架构说明。
--
-- 执行（库需自行创建，不由本文件负责）：
--   mysql -h 127.0.0.1 -P 13306 -u <user> -p <dbname> < backend/migrations/000001_init.up.sql
--
-- 建表约定：
--   * 每表 `id` 为 `BIGINT UNSIGNED AUTO_INCREMENT` 自增主键，对外直接用（taskId / runId）
--   * 每表都有 `created_at`、`updated_at`（DATETIME(3)，UTC），靠列属性维护，不写触发器
--   * 每张表、每个字段、每个二级索引都必须有 COMMENT，注释写业务含义而不是重复字段名
--   * 不用外键：关联字段的索引必须显式建，否则联表翻页会变慢
--   * 不用存储过程/函数/触发器
--   * 枚举用 VARCHAR，加值不改表

CREATE TABLE IF NOT EXISTS `task` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键，对外即 taskId',
  `client_id`  VARCHAR(64)     NOT NULL                COMMENT '客户端标识；无登录体系，取客户端本地生成的 uuid，不建 client 表',
  `title`      VARCHAR(200)    NOT NULL DEFAULT ''     COMMENT '会话标题，取首条用户输入截断至 200 字，不自动改写',
  `created_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  KEY `idx_client_updated` (`client_id`, `updated_at` DESC) COMMENT '历史列表：按客户端取最近会话并游标翻页'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='会话任务：一次完整对话的容器，历史列表、标题与 token 汇总挂在它上面';

CREATE TABLE IF NOT EXISTS `turn` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  `task_id`    BIGINT UNSIGNED NOT NULL                COMMENT '所属会话 task.id；无外键，靠 uk_task_seq 的前缀保证按会话查轮次不扫表',
  `seq`        INT             NOT NULL                COMMENT '会话内轮次序号，从 1 开始；拼接上下文按它排序',
  `user_input` MEDIUMTEXT      NOT NULL                COMMENT '用户原始输入，不脱敏、不改写，作为上下文拼接的原文',
  `created_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_seq` (`task_id`, `seq`) COMMENT '同一会话内轮次序号唯一；左前缀同时覆盖按 task 查轮次'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='会话轮次：一次用户输入的记录，追加后不修改';

CREATE TABLE IF NOT EXISTS `run` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键，对外即 runId',
  `task_id`         BIGINT UNSIGNED NOT NULL                COMMENT '所属会话 task.id；从 turn 冗余下来，供按会话查 Run 免联表',
  `turn_id`         BIGINT UNSIGNED NOT NULL                COMMENT '所属轮次 turn.id；重试与继续都复用同一 turn',
  `attempt_no`      INT             NOT NULL DEFAULT 1      COMMENT '同一轮次的第几次尝试，从 1 开始；重试 = 新建 Run 且 +1',
  `idempotency_key` VARCHAR(64)     NULL                    COMMENT '幂等键，同一 task 内唯一；重复提交直接返回同一 Run，不重复扣配额',
  `model_snapshot`  JSON            NOT NULL                COMMENT '执行时的模型参数快照（provider/model/temperature/max_tokens），保证配置变更后历史可还原',
  `status`          VARCHAR(16)     NOT NULL                COMMENT '执行状态：pending/running/completed/failed/aborted/interrupted',
  `error_category`  VARCHAR(32)     NULL                    COMMENT '错误分类：rate_limit/timeout/protocol/tool_error/quota_exceeded；仅失败时有值',
  `error_message`   TEXT            NULL                    COMMENT '脱敏后的错误摘要，可直接展示给用户',
  `input_tokens`    BIGINT          NOT NULL DEFAULT 0      COMMENT '输入 token 累计，应等于该 Run 所有 Step 之和',
  `output_tokens`   BIGINT          NOT NULL DEFAULT 0      COMMENT '输出 token 累计，应等于该 Run 所有 Step 之和',
  `started_at`      DATETIME(3)     NULL                    COMMENT '开始执行时间：拿到并发信号量后写入',
  `first_token_at`  DATETIME(3)     NULL                    COMMENT '首个 token 到达时间，只记一次；TTFT = 本字段 - started_at',
  `ended_at`        DATETIME(3)     NULL                    COMMENT '进入终态的时间；终态之后不再变化',
  `version`         INT             NOT NULL DEFAULT 0      COMMENT '乐观锁版本号，每次状态迁移 +1，配合 CAS 防止并发写出两个终态',
  `created_at`      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at`      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_turn_attempt` (`turn_id`, `attempt_no`) COMMENT '同一轮次的尝试号唯一；左前缀同时覆盖按 turn 查 Run',
  UNIQUE KEY `uk_task_idem` (`task_id`, `idempotency_key`) COMMENT '幂等约束：同一 task 下同一幂等键只能对应一个 Run',
  KEY `idx_task_status` (`task_id`, `status`) COMMENT '规则 1 校验：判断该会话是否已有 pending/running 的 Run',
  KEY `idx_status` (`status`) COMMENT '启动扫描：批量把残留 running 的 Run 置为 interrupted'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行尝试：某个轮次的一次运行，是状态机主体；重试只新建记录，不改写旧记录';

CREATE TABLE IF NOT EXISTS `step` (
  `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  `run_id`         BIGINT UNSIGNED NOT NULL                COMMENT '所属 Run run.id；归因查询按它取该 Run 的全部调用记录',
  `seq`            INT             NOT NULL                COMMENT 'Run 内连续序号，从 0 开始；必须连续才能核对调用是否丢失',
  `type`           VARCHAR(16)     NOT NULL                COMMENT '步骤类型：model_call（模型调用）/ tool_call（工具调用）',
  `provider`       VARCHAR(32)     NOT NULL DEFAULT ''     COMMENT '上游供应商标识，如 openai/anthropic/ollama',
  `model`          VARCHAR(64)     NOT NULL DEFAULT ''     COMMENT '本次实际调用的上游模型名',
  `http_status`    INT             NULL                    COMMENT '上游 HTTP 状态码；超时与建连失败时为空',
  `latency_ms`     INT             NULL                    COMMENT '本次调用耗时（毫秒），含等待首个 token 的时间',
  `input_tokens`   BIGINT          NOT NULL DEFAULT 0      COMMENT '本次调用消耗的输入 token',
  `output_tokens`  BIGINT          NOT NULL DEFAULT 0      COMMENT '本次调用消耗的输出 token',
  `tool_name`      VARCHAR(64)     NULL                    COMMENT '工具名，仅 tool_call 有值',
  `tool_input`     JSON            NULL                    COMMENT '工具入参 JSON，仅 tool_call 有值，超长截断',
  `tool_result`    JSON            NULL                    COMMENT '工具返回 JSON，仅 tool_call 有值，超长截断',
  `error_category` VARCHAR(32)     NULL                    COMMENT '错误分类，取值同 run.error_category；仅失败时有值',
  `created_at`     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at`     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_run_seq` (`run_id`, `seq`) COMMENT '同一 Run 内步骤序号唯一；左前缀同时覆盖按 run 查步骤'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行步骤：一次模型调用或工具调用，是耗时、状态码、token 与错误归因的最细粒度';

-- event 只建 uk_run_seq：它已覆盖补发查询 WHERE run_id=? AND seq>? ORDER BY seq，
-- 再建 idx_run_seq 是重复索引，只会拖慢写入。
CREATE TABLE IF NOT EXISTS `event` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键，仅作行标识不对外暴露；事件身份由 (run_id, seq) 决定',
  `run_id`     BIGINT UNSIGNED NOT NULL                COMMENT '所属 Run run.id；订阅与补发都按它过滤',
  `seq`        BIGINT          NOT NULL                COMMENT 'Run 内事件序号，严格递增但允许跳号；批量落库与进程崩溃都会留洞',
  `type`       VARCHAR(32)     NOT NULL                COMMENT '事件类型：run.started/turn.delta/step.started/step.finished/tool.result/run.terminal/heartbeat',
  `payload`    JSON            NOT NULL                COMMENT '事件负载 JSON，schema 按 type 各自定义；只写入不按字段查询',
  `created_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC；只追加表，写入后不再变化',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_run_seq` (`run_id`, `seq`) COMMENT '补发的地基：按 run + seq 单调读取，同时拒绝重复的 seq 写入'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='事件流：推给客户端的 SSE 事件，只追加不修改，支撑实时推送与断线补发';

CREATE TABLE IF NOT EXISTS `model_config` (
  `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  `provider`           VARCHAR(32)     NOT NULL                COMMENT '供应商标识：openai/anthropic/ollama 等',
  `base_url`           VARCHAR(255)    NOT NULL DEFAULT ''     COMMENT '自定义网关地址；为空表示使用该供应商官方默认地址',
  `model_name`         VARCHAR(64)     NOT NULL                COMMENT '上游模型名，如 gpt-4o-mini、qwen-plus',
  `supports_tool_call` TINYINT         NOT NULL DEFAULT 0      COMMENT '是否支持函数调用：1 支持 0 不支持；不支持时拒绝带工具的请求',
  `context_window`     INT             NOT NULL DEFAULT 0      COMMENT '上下文窗口 token 上限，用于超长输入预检',
  `enabled`            TINYINT         NOT NULL DEFAULT 1      COMMENT '是否启用：1 可用 0 下架；下架不物理删除，保住历史 Run 的可追溯性',
  `created_at`         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at`         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_provider_model` (`provider`, `model_name`) COMMENT '同一供应商下模型名唯一，避免 /api/models 出现重复项'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='模型配置：可用模型清单与能力声明，驱动 /api/models 与能力校验';

CREATE TABLE IF NOT EXISTS `usage_counter` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  `scope`      VARCHAR(16)     NOT NULL                COMMENT '统计维度：run（单次执行累计）/ client_day（单客户端按天累计）',
  `scope_key`  VARCHAR(64)     NOT NULL                COMMENT '维度键：scope=run 时为 runId；scope=client_day 时为 {clientId}:{yyyymmdd}',
  `tokens`     BIGINT          NOT NULL DEFAULT 0      COMMENT '累计 token 数，靠 UPDATE ... SET tokens = tokens + ? 原子累加，用于配额判定',
  `created_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间，UTC',
  `updated_at` DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间，UTC，由列属性自动维护',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_scope_key` (`scope`, `scope_key`) COMMENT '同一维度只有一行；累加走 INSERT ... ON DUPLICATE KEY UPDATE'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用量计数：按执行与客户端天两个维度累计 token，是配额拦截的数据来源';
