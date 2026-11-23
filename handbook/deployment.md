# 受保护的个人部署

本指南用于一个人访问自己的实例；多人教学站还需要账号、组织配额、审计与防滥用能力。默认 Compose 端口只绑定 `127.0.0.1`，不会直接暴露到公网。

## 部署前

1. 准备域名和 HTTPS 反向代理，所有示例域名使用自己的配置替换。
2. 创建全新数据库和独立低权限账号，不使用个人既有业务库。
3. 生成互不相同的会话密钥和访问密码，例如分别运行 `openssl rand -hex 32`。
4. 在被忽略的生产环境文件中设置 `DATABASE_URL`、`GATEWAY_SESSION_SECRET`、`GATEWAY_ACCESS_PASSWORD`。需要真实模型时再设置模型密钥。
5. 使用 `deploy/config.prod.yaml`；其中数据库地址必须来自环境变量。

不要复用 `.env.example` 中公开的本机示例密钥和密码。非回环监听必须配置访问密码与签名密钥；生产模式额外启用 Secure Cookie，因此只通过 HTTPS 访问。

## 可选原生部署

创建专用 gateway 系统用户，将构建后的 server、生产配置和静态资源放入 `/opt/go-agent-gateway`，并复制 `backend/configs/prompts/` 目录。参考 systemd unit 位于 `deploy/go-agent-gateway.service`；环境文件路径是 `/etc/go-agent-gateway/env`，应仅服务管理员可读。应用目录下 data 的权限仅授予服务用户。

先执行迁移，再启动服务。将 `deploy/Caddyfile.go-agent-gateway` 作为站点配置参考，设置 `GATEWAY_DOMAIN`。它先通过 `forward_auth` 验证顶层页面访问，使浏览器正确显示认证提示；随后将 `/api` 与健康检查转发到回环 8080，其他路径托管 Web 静态文件。

浏览器会提示 HTTP Basic 登录，用户名为 gateway，密码是自己生成的访问密码。EventSource 使用同源 Cookie 和浏览器认证，无需把任何凭据放入 URL。修改监听端口后同步修改代理上游。

## 发布、备份与回滚

`scripts/backend/deploy.sh` 与 `scripts/frontend/deploy.sh` 只管理当前机器上的 Compose 服务，不执行 SSH 或修改其他站点。生产发布前运行全部测试、迁移检查和隐私扫描；保留旧版本二进制与配置，回滚时需要一起核对数据库兼容性。

备份数据库时，备份文件属于私有数据，放在忽略目录并限制权限。数据库恢复应先在独立环境验证。`docker compose down -v` 会删除教程数据，不能作为日常停止命令。

日志不输出模型密钥与原始供应商错误体；仍不要公开原始对话、数据库转储或整份生产日志。最小健康检查只确认数据库可达，不代表模型密钥有效或全部业务表已完成迁移。

## 更新提示词

默认提示词路径相对配置文件解析。Compose 编辑挂载的提示词目录后运行 `make reload-prompt`；原生部署发送 SIGHUP。重载失败保留旧内容，在跑的 Run 不切换版本。不要把个人提示词、真实业务样例和抓包日志提交到公开仓库。详细步骤见[模型运行时参考](reference/model-runtime.md)。
