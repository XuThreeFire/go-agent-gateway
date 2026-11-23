# 新增只读课程查询工具

目标：增加 `course_lookup`，让 Agent 查询虚构课程资料。参考实现已放在 `backend/internal/tool/course.go`，默认没有注册，便于你动手完成最后的接线。

## 实现与注册

阅读 `CourseTool.Spec()` 的 JSON Schema 和 `Invoke()` 的严格解码。该工具只接受 `agent`、`sse` 两个示例主题，不读取文件或访问网络。

将 `backend/internal/tool/builtin.go` 的注册表改为：

```go
return []Tool{NowTool{}, CalcTool{}, CourseTool{}}
```

在自己的忽略配置文件中启用它：

```yaml
tools:
  enabled: [now, calc, course_lookup]
```

重启后端，在页面输入 `课程 agent`。模拟模型会产生工具调用并回灌课程结果。仅修改 YAML 不能凭空创建工具；仅写类型但不注册也不会获得工具能力。

## 测试与验收

```bash
go test ./backend/internal/tool -run TestCourseReferenceTool -v
make test vet
```

再补充正式用例：未知主题、未知参数、context 已取消、配置白名单排除工具。验收时同时查看 `/api/tools` 清单与 `tool.result` 事件，不只看模型最终文本。

如果增加网络工具，用 context 创建 HTTP 请求，固定允许的主机、设置超时和返回长度，避免把任意 URL 交给模型。写入型工具还需要自己的幂等策略；context 取消不能撤回已经发生的外部副作用。

完成练习后可以保留三工具版本；提交时同步工具清单与教程，不提交个人实验配置。
