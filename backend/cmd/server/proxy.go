package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func parseLLMProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "":
		return nil, nil
	case "off", "none", "direct":
		return &url.URL{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("无法解析代理地址，请检查 URL 格式")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, errors.New("代理只支持 http、https 或 socks5 协议")
	}
	if u.Hostname() == "" {
		return nil, errors.New("代理地址缺少 host")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("代理端口必须在 1 到 65535 之间")
		}
	}
	return u, nil
}
func proxyLabel(u *url.URL) string {
	if u == nil {
		return "environment"
	}
	if u.String() == "" {
		return "direct（已忽略 HTTPS_PROXY）"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}
func validateLLMDebug(env string, proxy *url.URL, insecure bool) error {
	if env == "prod" && (insecure || (proxy != nil && proxy.String() != "")) {
		return errors.New("生产环境禁止显式 LLM 调试代理或跳过 TLS 证书校验")
	}
	return nil
}
