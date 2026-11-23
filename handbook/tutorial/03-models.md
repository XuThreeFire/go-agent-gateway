# 03 接入自己的真实模型

目标：在不把密钥写进仓库的前提下，从模拟模型切换到真实模型。前置：完整本地环境已启动，且有自己的供应商账号。

## 配置方式

本项目实现 Chat Completions 兼容协议，请先确认供应商支持 `/chat/completions`、流式响应和可选工具调用。模型名必须填写账号实际可用的值。

原生开发时复制默认配置为根目录 `config.local.yaml`（已经忽略），保留 `demo`，将下面模型启用：

```yaml
- name: my-model
  provider: openai
  base_url: "https://api.openai.com/v1"
  upstream_model: "YOUR_MODEL_NAME"
  api_key_env: OPENAI_API_KEY
  supports_tool_call: true
  context_window: 16000
  enabled: true
```

`name` 是页面展示的别名；`upstream_model` 才是发送给供应商的模型名称；`base_url` 不含末尾的 `/chat/completions`。`provider` 是标签，写一个新标签不会自动实现新协议。

```bash
export OPENAI_API_KEY='替换为自己的密钥'
make run CONFIG=config.local.yaml
```

不要在终端录屏、截图或提交中展示真实密钥。原生服务不会自动加载 `.env`。Compose 路线则把密钥放到 `.env`，将 Compose 配置挂载路径改为自己忽略的配置文件；重新创建后端容器后生效。

## 验收

刷新页面，在模型下拉框选择 `my-model`。先发送简单问题，再发送 `计算 12345*6789`，查看工具步骤和最终结果。计费由供应商决定；模拟模式的 token 数只是演示值。

空密钥会在本地失败；401/403 检查密钥，429 检查限流和余额，404 检查地址及模型名。HTTP 200 但始终失败时，确认响应确实是兼容的流式 Chat Completions 格式。

相关代码：`backend/internal/llm/client.go` 负责请求与响应解析，`registry.go` 按模型别名选择客户端，`config.go` 负责配置校验。

练习：保留 `demo` 和一个真实模型，对同一个工具问题比较调用次数、耗时和供应商返回的 token 用量。不要用真实模型输出是否完全相同来判断工程测试是否通过。
