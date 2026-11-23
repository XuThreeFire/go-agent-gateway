package llm

// buildRequestV3 规范化工具顺序、Schema 键顺序和空参数形式，保持消息正文不变。
// 请求字节稳定有助于排查前缀变化，不保证供应商一定命中缓存。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// buildRequestV3 保持消息语义，对工具声明的排序和 JSON 对象键做规范化。
//
// 返回 error 而不是静默降级：schema 不是合法 JSON 属于配置错误，
// 发一个坏掉的 tools 上去，上游要么 400，要么安静地忽略整个工具声明——
// 后者会表现成「模型突然不会用工具了」，比启动时报错难查得多。
func (c *openAICompat) buildRequestV3(req Request) (wireRequest, error) {
	msgs, err := wireMessagesV3(req.Messages)
	if err != nil {
		return wireRequest{}, err
	}
	tools, err := wireToolsV3(req.Tools)
	if err != nil {
		return wireRequest{}, err
	}
	return c.withOutputLimit(wireRequest{
		Model:    c.target.UpstreamModel,
		Messages: msgs,
		Stream:   true,
		// 不带这个参数，多数供应商在流式模式下不返回 usage，
		// token 统计（包括下面的缓存命中数）会全是 0。
		StreamOptions: &streamOption{IncludeUsage: true},
		Tools:         tools,
	}, req.MaxOutputTokens), nil
}

// wireMessagesV3 把领域消息转成线上格式。
//
// 空参数统一成 "{}"，兼容不同适配器产生的空参数表示。
// 当前跨轮上下文只加载用户与助手正文；本函数也保留当前 Run 的工具调用配对。
func wireMessagesV3(msgs []domain.Message) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		role := string(m.Role)
		if role == "" {
			role = string(domain.RoleUser)
		}
		// content 恒为字符串（哪怕空串），不省略：字段忽有忽无同样会改变前缀，
		// 而且部分网关对 assistant 消息缺 content 的处理并不一致。
		wm := wireMessage{Role: role, Content: m.Content}

		if len(m.ToolCalls) > 0 {
			wm.ToolCalls = make([]wireToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				args := tc.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					ID:       tc.ID,
					Type:     domain.TypeTool,
					Function: wireCallFunction{Name: tc.Name, Arguments: args},
				})
			}
		}
		// tool_call_id 与 name 只在 tool 消息上有意义。少填 tool_call_id 上游直接 400，
		// 多填在 assistant 上则是协议外的字段。
		if m.Role == domain.RoleTool {
			wm.ToolCallID = m.ToolCallID
			wm.Name = m.Name
		}
		out = append(out, wm)
	}
	return out, nil
}

// wireToolsV3 生成工具声明，顺序按 name 升序，参数结构做归一化。
//
// 为什么排序：供应商可能把工具声明纳入提示词前缀，工具变化会影响可复用部分。
// 现在 tool.Registry 用切片保序，看起来是稳的，但这份「稳」是调用方
// （main 里手写的 Builtins() 顺序）施舍的，不是本函数保证的。
// 任何一次「从 map 里生成工具列表」的重构都会静默把缓存打到 0，且不报错。
// 排序把这条隐含约定变成代码里的显式契约。
func wireToolsV3(specs []ToolSpec) ([]wireTool, error) {
	if len(specs) == 0 {
		// 返回 nil 而不是空切片：omitempty 才会真的把 tools 字段省掉。
		// 发 [] 有的供应商当成「声明了 0 个工具」直接拒。
		return nil, nil
	}

	sorted := make([]ToolSpec, len(specs))
	copy(sorted, specs)
	// 同名的两个工具不存在（Registry 会去重），用稳定排序只是为了结果可复现。
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	out := make([]wireTool, 0, len(sorted))
	for _, s := range sorted {
		params, err := canonicalRawMessage(s.Parameters)
		if err != nil {
			return nil, fmt.Errorf("工具 %q 的 parameters 不是合法 JSON: %w", s.Name, err)
		}
		out = append(out, wireTool{
			Type: domain.TypeTool,
			Function: wireFunction{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  params,
			},
		})
	}
	return out, nil
}

// canonicalRawMessage 规范化 JSON 对象键和空白，保留数组顺序和数字字面量。
//
// 做法：解码成 any（数字用 json.Number 保原样，不让 float64 把 1 变成 1E+00），
// 再编码回去。encoding/json 对 map 的键会排序，递归下去键顺序就全固定了。
//
// 这一步真正防的是：schema 被手写成字符串常量（builtin.go 里就是），
// 哪天有人调了缩进、换了键的顺序、从 map 拼出来，语义没变但字节变了,
// 缓存从 tools 的第一个 token 就开始失配。
func canonicalRawMessage(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil // 与 V1 一致：没有 parameters 就整个字段省掉
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// 两个 JSON 拼在一起时 Decode 只吃第一个，剩下的必须判错，
	// 否则脏数据会悄悄进到前缀里。
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("JSON 后面还有多余内容")
	}

	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// staticPrefixFingerprint 是「每次请求都应该一模一样」那一段的指纹：
// model + tools + 首条 system。
//
// 它不覆盖对话内容——那部分本来就会增长，覆盖了等于每轮都变，没有诊断价值。
//
// 指纹仅覆盖模型名、规范化工具和开头的 system 消息；不会记录用户对话。
// 它是诊断线索，不代表供应商的真实 token 序列或缓存命中保证。
func staticPrefixFingerprint(w wireRequest) string {
	h := sha256.New()
	// Model 也算进去：换模型时上游的缓存池本来就不共享。
	h.Write([]byte(w.Model))
	if b, err := json.Marshal(w.Tools); err == nil {
		h.Write(b)
	}
	// 只有一条 system 且必须在首位（见 exec.withSystem），取到第一条非 system 为止。
	for _, m := range w.Messages {
		if m.Role != string(domain.RoleSystem) {
			break
		}
		h.Write([]byte(m.Content))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
