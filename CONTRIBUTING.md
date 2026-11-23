# 参与改进

先阅读 README 与 handbook。欢迎提交能够帮助初学者理解 Agent 的小改进；新增能力应说明对应的学习目标。

## 本地检查

```bash
make tools web-install
make test vet web-test web-build privacy-check
```

Go 测试使用标准 testing/httptest，前端行为测试使用 Vitest。涉及事务、迁移、身份归属或事件回放时必须运行真实 MySQL 集成用例。

## 集成测试

Docker 引擎可用时，优先使用完整的隔离入口，无需安装本机 MySQL、手工建库或配置测试凭据：

```bash
make test-container
```

该命令创建独立测试项目，不占用宿主端口，不复用开发卷；测试结束清理专用容器和卷，缓存只保留在 tmp/。

使用已有独立测试实例时，再采用下面的原生命令：

创建专用、可丢弃的 MySQL 8.4 数据库 `go_agent_gateway_test`，测试账号只对该库授权。不要把生产或个人开发库连接串用于测试。

```bash
export GATEWAY_TEST_DATABASE_URL='mysql://TEST_USER:URL_ENCODED_PASSWORD@127.0.0.1:3306/go_agent_gateway_test?parseTime=true&loc=UTC'
DATABASE_URL="$GATEWAY_TEST_DATABASE_URL" make migrate-local
make test-integration
```

测试入口检查库名必须以 `_test` 结尾；用例会写入虚构会话数据。迁移由固定版本 golang-migrate 管理，可重复执行 up；不要手工循环执行全部 ALTER 脚本。

## 文档与隐私

公开文档在 handbook/；docs/、.local-docs/ 是当前用户与 AI 的私人资料，不得提交、强制添加或打包。配置示例只放占位符或明确的本机演示值；真实密钥、数据库转储、IDE 状态、个人服务器地址均不进入变更。

提交前运行 `make release-check`，阅读变更，更新受影响教程。不要将尚未验证的设计目标写成已实现保证。对于 AI 辅助生成的改动，贡献者仍需理解实现并提交行为验证结果。
