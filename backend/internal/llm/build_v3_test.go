package llm

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// 这一组测试只验证一件事：**相同的逻辑输入必须产出相同的字节**。
// 只验证本地确定性；供应商是否实际复用缓存需要观察其返回值。

func v3Body(t *testing.T, c *openAICompat, req Request) []byte {
	t.Helper()
	w, err := c.buildRequestV3(req)
	if err != nil {
		t.Fatalf("buildRequestV3 失败: %v", err)
	}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	return b
}

func newTestClient() *openAICompat {
	return New(Target{Name: "t", UpstreamModel: "m", SupportsToolCall: true}, ClientOptions{}).(*openAICompat)
}

// 工具入参顺序变化不应引入多余的请求字节变化。
func TestBuildRequestV3ToolOrderIsIrrelevant(t *testing.T) {
	c := newTestClient()
	now := ToolSpec{Name: "now", Description: "时间", Parameters: json.RawMessage(`{"type":"object"}`)}
	calc := ToolSpec{Name: "calc", Description: "算数", Parameters: json.RawMessage(`{"type":"object"}`)}
	msgs := []domain.Message{{Role: domain.RoleUser, Content: "你好"}}

	a := v3Body(t, c, Request{Messages: msgs, Tools: []ToolSpec{now, calc}})
	b := v3Body(t, c, Request{Messages: msgs, Tools: []ToolSpec{calc, now}})
	if !bytes.Equal(a, b) {
		t.Fatalf("工具顺序不同导致请求体不同:\n%s\n%s", a, b)
	}

	// 顺带确认排序真的生效（按 name 升序），而不是碰巧两次输入一样。
	var wr wireRequest
	if err := json.Unmarshal(a, &wr); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(wr.Tools) != 2 || wr.Tools[0].Function.Name != "calc" || wr.Tools[1].Function.Name != "now" {
		t.Fatalf("工具未按 name 升序排列: %+v", wr.Tools)
	}
}

// schema 的键顺序、缩进不参与语义，但对上游来说是实打实的字节差异。
// 手写 JSON 常量（builtin.go 就是这么写的）调整一下缩进，就能让缓存全废。
func TestBuildRequestV3CanonicalizesSchemaKeyOrder(t *testing.T) {
	c := newTestClient()
	msgs := []domain.Message{{Role: domain.RoleUser, Content: "算一下"}}

	p1 := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
	p2 := json.RawMessage("{\n  \"required\": [\"a\"],\n  \"properties\": {\"a\": {\"type\": \"string\"}},\n  \"type\": \"object\"\n}")

	a := v3Body(t, c, Request{Messages: msgs, Tools: []ToolSpec{{Name: "calc", Parameters: p1}}})
	b := v3Body(t, c, Request{Messages: msgs, Tools: []ToolSpec{{Name: "calc", Parameters: p2}}})
	if !bytes.Equal(a, b) {
		t.Fatalf("schema 键顺序/缩进不同导致请求体不同:\n%s\n%s", a, b)
	}

	// 归一化后的 parameters 应该是紧凑且键有序的。
	const want = `"parameters":{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`
	if !bytes.Contains(a, []byte(want)) {
		t.Fatalf("parameters 未归一化，期望包含 %s，实际 %s", want, a)
	}
}

// 数字不能被归一化改形：UseNumber 的意义就在这里。
// 用 float64 解码会把 1 变成 1，但大整数会掉精度、1.50 会变 1.5，
// 那等于偷偷改了发给模型的 schema。
func TestCanonicalRawMessageKeepsNumbers(t *testing.T) {
	raw := json.RawMessage(`{"minimum":1.50,"maximum":12345678901234567890,"z":1}`)
	got, err := canonicalRawMessage(raw)
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if string(got) != `{"maximum":12345678901234567890,"minimum":1.50,"z":1}` {
		t.Fatalf("数字被改写: %s", got)
	}
}

// 工具回灌必须保留全部消息、tool_call_id 和工具声明。
func TestBuildRequestV3CarriesToolHistory(t *testing.T) {
	c := newTestClient()
	req := Request{
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "只用一句话回答"},
			{Role: domain.RoleUser, Content: "现在几点"},
			{Role: domain.RoleAssistant, Content: "我查一下",
				ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "now", Arguments: `{"timezone":"Asia/Shanghai"}`}}},
			{Role: domain.RoleTool, Content: "2026-11-23 11:30:00", ToolCallID: "call_1", Name: "now"},
		},
		Tools: []ToolSpec{{Name: "now", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}

	wr, err := c.buildRequestV3(req)
	if err != nil {
		t.Fatalf("buildRequestV3 失败: %v", err)
	}
	if len(wr.Messages) != 4 {
		t.Fatalf("消息数 = %d，期望 4（所有消息必须保留）", len(wr.Messages))
	}
	if wr.Messages[0].Role != "system" || wr.Messages[0].Content != "只用一句话回答" {
		t.Fatalf("system 被改写: %+v", wr.Messages[0])
	}
	as := wr.Messages[2]
	if len(as.ToolCalls) != 1 || as.ToolCalls[0].ID != "call_1" || as.ToolCalls[0].Type != "function" {
		t.Fatalf("assistant 丢了 tool_calls: %+v", as)
	}
	if as.Content != "我查一下" {
		t.Fatalf("assistant 的旁白丢失（会让模型下一轮忘记自己说过什么）: %+v", as)
	}
	tool := wr.Messages[3]
	if tool.ToolCallID != "call_1" || tool.Name != "now" {
		t.Fatalf("tool 消息丢了 tool_call_id: %+v", tool)
	}
	if len(wr.Tools) != 1 {
		t.Fatalf("tools 被丢弃: %+v", wr.Tools)
	}
}

// 空参数在两条路上形态不同：流式拼装补成 "{}"，数据库里可能存的是 ""。
// 不统一就会出现「同一段对话重放两次，字节不同」的缓存裂缝。
func TestBuildRequestV3UnifiesEmptyArguments(t *testing.T) {
	c := newTestClient()
	req := Request{Messages: []domain.Message{
		{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c1", Name: "now"}}},
	}}
	wr, err := c.buildRequestV3(req)
	if err != nil {
		t.Fatalf("buildRequestV3 失败: %v", err)
	}
	if got := wr.Messages[0].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("空参数 = %q，期望 {}", got)
	}
}

// 没有工具时必须整个字段省掉，不能发空数组：部分供应商会当成参数错误。
func TestBuildRequestV3OmitsEmptyTools(t *testing.T) {
	c := newTestClient()
	b := v3Body(t, c, Request{Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}}})
	if bytes.Contains(b, []byte(`"tools"`)) {
		t.Fatalf("无工具时不应出现 tools 字段: %s", b)
	}
}

// 语义不能和 V1 分叉：V3 只是换了个更稳的写法，不是新协议。
func TestBuildRequestV3MatchesV1(t *testing.T) {
	c := newTestClient()
	req := Request{
		Messages: []domain.Message{
			{Role: domain.RoleSystem, Content: "sys"},
			{Role: domain.RoleUser, Content: "现在几点"},
			{Role: domain.RoleAssistant, Content: "我查一下",
				ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "now", Arguments: `{"timezone":"UTC"}`}}},
			{Role: domain.RoleTool, Content: "12:00", ToolCallID: "call_1", Name: "now"},
		},
		Tools: []ToolSpec{{Name: "now", Description: "时间", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}

	v1, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		t.Fatalf("V1 序列化失败: %v", err)
	}
	v3 := v3Body(t, c, req)
	if !bytes.Equal(v1, v3) {
		t.Fatalf("V1 与 V3 的请求体不一致（语义分叉了）:\nV1=%s\nV3=%s", v1, v3)
	}
}

// 指纹的用途是「命中率掉了先看它变没变」，所以它必须对「会增长的对话」
// 免疫，只对「静态前缀」敏感。
func TestStaticPrefixFingerprint(t *testing.T) {
	c := newTestClient()
	tools := []ToolSpec{{Name: "now", Description: "时间", Parameters: json.RawMessage(`{"type":"object"}`)}}
	sys := domain.Message{Role: domain.RoleSystem, Content: "你是助手"}

	fp := func(msgs []domain.Message, ts []ToolSpec) string {
		t.Helper()
		w, err := c.buildRequestV3(Request{Messages: msgs, Tools: ts})
		if err != nil {
			t.Fatalf("buildRequestV3 失败: %v", err)
		}
		return staticPrefixFingerprint(w)
	}

	base := fp([]domain.Message{sys, {Role: domain.RoleUser, Content: "第一问"}}, tools)

	// 追加对话不能改变前缀指纹——否则这个指标每轮都变，失去诊断价值。
	grown := fp([]domain.Message{sys,
		{Role: domain.RoleUser, Content: "第一问"},
		{Role: domain.RoleAssistant, Content: "答"},
		{Role: domain.RoleUser, Content: "第二问"},
	}, tools)
	if grown != base {
		t.Fatalf("追加对话后指纹变了: %s → %s", base, grown)
	}

	// 改工具描述 → 必须变（这是最容易误伤缓存的改动）。
	changedTool := fp([]domain.Message{sys, {Role: domain.RoleUser, Content: "第一问"}},
		[]ToolSpec{{Name: "now", Description: "查时间", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if changedTool == base {
		t.Fatal("工具描述变了，指纹却没变")
	}

	// 改 system → 必须变。SIGHUP 热重载一次提示词 = 全站缓存清零一轮，
	// 指纹能把这件事和「缓存坏了」区分开。
	changedSys := fp([]domain.Message{
		{Role: domain.RoleSystem, Content: "你是严谨的助手"},
		{Role: domain.RoleUser, Content: "第一问"},
	}, tools)
	if changedSys == base {
		t.Fatal("system 变了，指纹却没变")
	}
}

func TestCanonicalRawMessageRejectsTrailingValues(t *testing.T) {
	for _, raw := range []string{`{} {}`, `{} trailing`, `{}]`} {
		if _, err := canonicalRawMessage(json.RawMessage(raw)); err == nil {
			t.Fatalf("应拒绝尾部内容：%q", raw)
		}
	}
}
