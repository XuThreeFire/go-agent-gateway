package llm

import "fmt"

// Registry 保存「对外模型名 → 客户端」的映射。
// 模型清单的来源是配置文件（configs/*/config.yaml），不是数据库：
// 密钥不能进库，而且进程启动就该知道能用哪些模型，不该多一次查询。
type Registry struct {
	clients map[string]Client
	order   []string
}

func NewRegistry(targets []Target, opt ClientOptions) *Registry {
	r := &Registry{clients: make(map[string]Client, len(targets))}
	for _, t := range targets {
		r.clients[t.Name] = New(t, opt)
		r.order = append(r.order, t.Name)
	}
	return r
}

func (r *Registry) Get(name string) (Client, bool) {
	c, ok := r.clients[name]
	return c, ok
}

// List 按配置顺序返回，保证 /api/models 的输出稳定，便于客户端缓存。
func (r *Registry) List() []Target {
	out := make([]Target, 0, len(r.order))
	for _, name := range r.order {
		c, ok := r.clients[name]
		if !ok {
			continue
		}
		t := c.Target()
		t.APIKey = "" // 密钥不出现在任何响应里
		out = append(out, t)
	}
	return out
}

func (r *Registry) Default() (string, error) {
	if len(r.order) == 0 {
		return "", fmt.Errorf("没有可用模型")
	}
	return r.order[0], nil
}
