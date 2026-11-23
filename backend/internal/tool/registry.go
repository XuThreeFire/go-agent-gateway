// Package tool 是模型可调用的工具集合：注册表 + 能力边界控制。
//
// 设计上的两个刻意选择：
//
//  1. **白名单，不是插件系统**。工具在编译期确定，配置只能关不能加。模型能做什么
//     由代码写死——不给它任意 HTTP、文件读写、命令执行。这是「Agent 行为边界」
//     最有效的一道闸，比在提示词里写「请不要删库」可靠得多。
//  2. **结果一律截断**。工具结果会进上下文（烧 token）也会进 event/step 两张表
//     （撑爆行大小），所以 Registry.Run 出口处统一截断到 MaxResultBytes。
//     截断做在出口而不是各个工具里，保证没有漏网的。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	// ErrNotFound 表示模型调了一个不存在的工具（多半是幻觉出来的名字）。
	// 这是可恢复错误：把「有哪些工具可用」回灌给模型，它通常会自己改。
	ErrNotFound = errors.New("工具不存在")
	// ErrPanic 表示工具实现自己崩了。这是代码 bug，不能回灌给模型——
	// 它再试一次只会再崩一次，还会白烧 token，所以执行器见到它直接判 Run 失败。
	ErrPanic = errors.New("工具执行 panic")
)

// Spec 是工具的对外声明，会原样转成上游的 tools 字段（JSON Schema）。
type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Tool 是所有工具的契约。Invoke 的 args 是模型给的 JSON 原文，
// 由工具自己解码：这样参数校验的错误信息可以写得很具体，模型看得懂才改得对。
type Tool interface {
	Spec() Spec
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// Options 控制注册表的边界。
type Options struct {
	// Enabled 是白名单；留空表示全部启用。填了但名字不存在的项会被忽略，
	// 由调用方（main）打日志提示，避免手滑写错工具名后「静默没有工具」。
	Enabled []string
	// MaxResultBytes 单次结果上限，0 用默认 8KB。
	MaxResultBytes int
}

const defaultMaxResultBytes = 8 << 10

// Registry 持有全部启用中的工具，并发只读，不需要加锁。
type Registry struct {
	tools          map[string]Tool
	order          []string
	maxResultBytes int
}

// New 按白名单装配注册表。tools 的顺序决定 Specs() 的输出顺序，
// 固定顺序能让上游 prompt 缓存命中率更高，排查时也稳定。
func New(opt Options, tools ...Tool) *Registry {
	limit := opt.MaxResultBytes
	if limit <= 0 {
		limit = defaultMaxResultBytes
	}
	allow := make(map[string]bool, len(opt.Enabled))
	for _, name := range opt.Enabled {
		allow[name] = true
	}

	r := &Registry{tools: make(map[string]Tool, len(tools)), maxResultBytes: limit}
	for _, t := range tools {
		if t == nil {
			continue
		}
		name := t.Spec().Name
		if name == "" {
			continue
		}
		if len(allow) > 0 && !allow[name] {
			continue
		}
		if _, dup := r.tools[name]; dup {
			continue
		}
		r.tools[name] = t
		r.order = append(r.order, name)
	}
	return r
}

func (r *Registry) Len() int { return len(r.order) }

// Names 返回启用中的工具名，顺序与 Specs 一致。
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Specs 返回工具声明，供执行器拼请求体。
func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

// Unknown 返回白名单里在注册表中找不到的名字，供启动时告警。
func (r *Registry) Unknown(enabled []string) []string {
	var out []string
	for _, name := range enabled {
		if _, ok := r.tools[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}

// Run 执行一次工具调用，统一负责：查表、参数合法性、panic 兜底、结果截断。
//
// 返回的 error 分两种语义，由调用方（执行器）区别对待：
//   - errors.Is(err, ErrPanic)：代码 bug，Run 直接失败；
//   - 其它：可回灌给模型的业务错误（参数写错、工具不存在、除零……）。
func (r *Registry) Run(ctx context.Context, name string, args string) (out string, err error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("%w: %q，可用工具：%s", ErrNotFound, name, strings.Join(r.order, ", "))
	}

	raw := strings.TrimSpace(args)
	if raw == "" {
		raw = "{}"
	}
	// 参数不是合法 JSON 时不能把原文塞给工具，就地判错让模型重写。
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("参数不是合法 JSON: %s", truncate(raw, 200))
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%w: 工具 %s 内部错误", ErrPanic, name)
		}
	}()

	out, err = t.Invoke(ctx, json.RawMessage(raw))
	if err != nil {
		return "", fmt.Errorf("工具 %s 执行失败: %w", name, err)
	}
	if strings.TrimSpace(out) == "" {
		// 空字符串回灌给模型等于「工具没反应」，它只能瞎猜。给个明确的空结果。
		out = "（工具执行成功，无输出）"
	}
	return truncate(out, r.maxResultBytes), nil
}

// truncate 按字节截断但保证不切坏 UTF-8：切坏了 JSON 编码会把非法字节
// 变成 \ufffd，模型看到一堆问号更困惑。max<=0 表示不限。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n...(结果已截断，原始 %d 字节)", len(s))
}
