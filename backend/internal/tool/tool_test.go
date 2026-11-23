package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCalcEvaluates(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"1+2", "3"},
		{"(12+3)*4/2", "30"},
		{"2*(3+4)-5", "9"},
		{"-3*2", "-6"},
		{"10%3", "1"},
		{"1.5+2.25", "3.75"},
		{"0.1+0.2", "0.30000000000000004"},
	}
	for _, c := range cases {
		got, err := CalcTool{}.Invoke(context.Background(), mustJSON(t, map[string]string{"expression": c.expr}))
		if err != nil {
			t.Fatalf("%s 求值失败: %v", c.expr, err)
		}
		if !strings.HasSuffix(got, "= "+c.want) {
			t.Fatalf("%s = %q，期望以 %q 结尾", c.expr, got, c.want)
		}
	}
}

// 拒绝的东西比支持的东西重要：这里就是「不给模型执行任意代码」的具体落点。
func TestCalcRejects(t *testing.T) {
	cases := []string{
		"1/0",          // 除零
		"2**3",         // 不支持幂运算
		"import('os')", // 想执行代码
		"(1+2",         // 括号不闭合
		"1+",           // 缺操作数
		"alert(1)",     // 函数调用
		"1 + 2 extra",  // 尾巴有多余内容
	}
	for _, expr := range cases {
		if _, err := (CalcTool{}).Invoke(context.Background(), mustJSON(t, map[string]string{"expression": expr})); err == nil {
			t.Fatalf("%q 应该被拒绝", expr)
		}
	}
}

// 参数多塞一个字段要报错，不能静默忽略——静默忽略会让「模型用错参数名」
// 这种问题拖到很久以后才暴露。
func TestCalcRejectsUnknownFields(t *testing.T) {
	_, err := CalcTool{}.Invoke(context.Background(), json.RawMessage(`{"expression":"1+1","unit":"cm"}`))
	if err == nil {
		t.Fatal("多余字段应该报错")
	}
}

// panic 必须变成可识别的 ErrPanic，执行器据此直接判 Run 失败，而不是回灌给模型。
func TestRegistryRecoversPanic(t *testing.T) {
	r := New(Options{}, panicTool{})
	_, err := r.Run(context.Background(), "boom", "{}")
	if err == nil {
		t.Fatal("panic 应该变成错误")
	}
	if !errors.Is(err, ErrPanic) {
		t.Fatalf("错误类型不对: %v", err)
	}
}

// 不存在的工具要带上「有哪些可用」，模型才有可能自己改对。
func TestRegistryUnknownToolListsAvailable(t *testing.T) {
	r := New(Options{}, NowTool{}, CalcTool{})
	_, err := r.Run(context.Background(), "search_web", "{}")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("错误类型不对: %v", err)
	}
	if !strings.Contains(err.Error(), "calc") || !strings.Contains(err.Error(), "now") {
		t.Fatalf("错误信息没列出可用工具: %v", err)
	}
}

func TestRegistryTruncatesResult(t *testing.T) {
	r := New(Options{MaxResultBytes: 16}, echoTool{})
	out, err := r.Run(context.Background(), "echo", string(mustJSON(t, map[string]string{"text": strings.Repeat("好", 100)})))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(out) > 64 {
		t.Fatalf("结果没被截断，长度 %d", len(out))
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("截断要留可见标记，实际: %q", out)
	}
	for _, r := range out {
		if r == '\uFFFD' {
			t.Fatal("截断切坏了 UTF-8")
		}
	}
}

// 白名单只能关不能加：写错名字或写了不存在的工具，注册表里就是没有。
func TestRegistryWhitelist(t *testing.T) {
	r := New(Options{Enabled: []string{"now", "no_such_tool"}}, Builtins()...)
	if got := r.Names(); len(got) != 1 || got[0] != "now" {
		t.Fatalf("启用列表 = %v，期望只有 now", got)
	}
	if unknown := r.Unknown([]string{"now", "no_such_tool"}); len(unknown) != 1 || unknown[0] != "no_such_tool" {
		t.Fatalf("未识别的工具 = %v", unknown)
	}
}

// 参数不是合法 JSON 时不能硬塞给工具，就地判错让模型重写。
func TestRegistryRejectsInvalidJSON(t *testing.T) {
	r := New(Options{}, CalcTool{})
	_, err := r.Run(context.Background(), "calc", `{"expression": "1+1"`)
	if err == nil || !strings.Contains(err.Error(), "合法 JSON") {
		t.Fatalf("应该报参数不是合法 JSON: %v", err)
	}
}

type panicTool struct{}

func (panicTool) Spec() Spec { return Spec{Name: "boom", Description: "always panics"} }

func (panicTool) Invoke(context.Context, json.RawMessage) (string, error) { panic("boom") }

type echoTool struct{}

func (echoTool) Spec() Spec { return Spec{Name: "echo", Description: "echo text"} }

func (echoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	return in.Text, nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

func TestCourseReferenceTool(t *testing.T) {
	out, err := (CourseTool{}).Invoke(context.Background(), json.RawMessage(`{"topic":"agent"}`))
	if err != nil || !strings.Contains(out, "Agent") {
		t.Fatalf("%s %v", out, err)
	}
}
