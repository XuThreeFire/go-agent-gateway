package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SessionSecret 本地首次启动生成密钥并持久保存，重启不会丢失浏览器身份。
// 生产只读环境变量；不得将生成文件或环境变量值发布到仓库。
func (c Config) SessionSecret() (string, error) {
	if v := os.Getenv(c.Security.SessionSecretEnv); v != "" {
		if len(v) < 32 {
			return "", errors.New("会话密钥至少需要 32 字节")
		}
		return v, nil
	}
	path := c.Security.SessionSecretFile
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(strings.TrimSpace(string(raw))) < 32 {
			return "", errors.New("本地会话密钥文件无效")
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if !os.IsNotExist(err) {
		return "", errors.New("无法读取本地会话密钥文件")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	value := hex.EncodeToString(key)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		raw, err = os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if len(strings.TrimSpace(string(raw))) < 32 {
			return "", errors.New("会话密钥文件未就绪")
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := file.WriteString(value); err != nil {
		return "", err
	}
	return value, nil
}
