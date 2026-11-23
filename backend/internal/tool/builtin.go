package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
	// 容器镜像（尤其 Alpine）常缺 tzdata，LoadLocation 会直接失败。
	// 内嵌一份让时区工具在任何环境都能用，代价约 450KB。
	_ "time/tzdata"
)

// Builtins 返回内置工具集。新增工具在这里登记，别的地方不用改。
func Builtins() []Tool {
	return []Tool{NowTool{}, CalcTool{}}
}

// NowTool 让模型能回答「现在几点」。模型本身没有时钟，这是最典型的
// 「不知道自己不知道」场景：不接工具它会编一个时间。
type NowTool struct{}

func (NowTool) Spec() Spec {
	return Spec{
		Name:        "now",
		Description: "查询当前时间。被问到「现在几点」「今天几号」「距某天还有多久」时调用，不要凭记忆猜时间。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "timezone": {
      "type": "string",
      "description": "IANA 时区名，例如 Asia/Shanghai、America/New_York；不填按 UTC"
    }
  },
  "additionalProperties": false
}`),
	}
}

func (NowTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Timezone string `json:"timezone"`
	}
	if err := decodeStrict(args, &in); err != nil {
		return "", err
	}

	loc := time.UTC
	if in.Timezone != "" {
		l, err := time.LoadLocation(in.Timezone)
		if err != nil {
			// 报错要具体到「该填什么」，模型下一轮才有机会改对。
			return "", fmt.Errorf("未知时区 %q，请用 IANA 名称（如 Asia/Shanghai、UTC）", in.Timezone)
		}
		loc = l
	}

	now := time.Now().In(loc)
	return fmt.Sprintf("%s（%s，UTC%s）", now.Format("2006-01-02 15:04:05"), loc.String(), now.Format("-07:00")), nil
}

// CalcTool 做四则运算。故意的窄：不 eval、不接变量、不调库。
// 模型算数是弱项，接一个自己写的解析器既解决问题，又不给它执行任意代码的口子。
type CalcTool struct{}

func (CalcTool) Spec() Spec {
	return Spec{
		Name:        "calc",
		Description: "计算算术表达式。只要涉及多位数的加减乘除或百分比，都应调用它而不是心算。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "expression": {
      "type": "string",
      "description": "算式，支持 + - * / % 与括号，例如 (12+3)*4/2"
    }
  },
  "required": ["expression"],
  "additionalProperties": false
}`),
	}
}

func (CalcTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Expression string `json:"expression"`
	}
	if err := decodeStrict(args, &in); err != nil {
		return "", err
	}
	if in.Expression == "" {
		return "", fmt.Errorf("expression 不能为空")
	}
	if len(in.Expression) > 200 {
		return "", fmt.Errorf("表达式过长（上限 200 字符）")
	}

	value, err := evalExpr(in.Expression)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s = %s", in.Expression, formatNumber(value)), nil
}

// decodeStrict 用 DisallowUnknownFields 解码：模型多塞一个字段时要报错而不是静默忽略。
// 静默忽略会让「参数写错但工具照跑」的 bug 拖到很久以后才被发现。
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("参数解析失败: %w", err)
	}
	return nil
}
