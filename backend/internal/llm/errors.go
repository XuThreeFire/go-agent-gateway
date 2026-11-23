package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// APIError 是上游返回非 200 时的错误，保留状态码与响应片段用于归因。
// Body 仅供内部诊断，Error() 与公开终态不拼接原始上游内容。
type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	// 401/403 单独给一句人话：上游原文是「auth header format should be Bearer sk-...」，
	// 看着像我们拼错了 header，实际九成是密钥没配或过期。
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return fmt.Sprintf("上游鉴权失败 %d（检查模型密钥是否配置、是否过期）", e.Status)
	}
	return fmt.Sprintf("上游返回 %d", e.Status)
}

// ConfigError 是「本地就知道调不通」的错误，请求根本没发出去。
// 与 APIError 区分开：它的 message 直接指向要改的配置，不需要去翻上游日志。
type ConfigError struct {
	Model  string
	KeyEnv string
}

func (e *ConfigError) Error() string {
	if e.KeyEnv == "" {
		return fmt.Sprintf("模型 %s 未配置密钥（config.yaml 的 api_key 为空），请求未发出", e.Model)
	}
	return fmt.Sprintf("模型 %s 未配置密钥（环境变量 %s 为空），请求未发出", e.Model, e.KeyEnv)
}

// Classify 把错误映射到五类，唯一用途是决定「能不能重试」和「给用户看什么」。
// 不再细分：分类越细，维护成本越高，而处置动作只有那几种。
func Classify(err error) domain.ErrorCategory {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusTooManyRequests:
			return domain.ErrRateLimit
		default:
			return domain.ErrProtocol
		}
	}
	// 密钥没配不单开一类：分类只决定「能不能重试」和「展示什么」，
	// 这里既不重试，要展示的文案也已经在 Error() 里写清楚了。
	var cfgErr *ConfigError
	if errors.As(err, &cfgErr) {
		return domain.ErrProtocol
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		// 只有真的超时才算 timeout。连不上（connection refused）、连接被重置这些
		// 也实现 net.Error，但它们不是「慢」，冒充超时会把排查方向带偏。
		if netErr.Timeout() {
			return domain.ErrTimeout
		}
		return domain.ErrProtocol
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return domain.ErrTimeout
	}
	return domain.ErrProtocol
}
