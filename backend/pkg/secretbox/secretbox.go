// Package secretbox 给"入库的第三方凭据"提供对称加密封装。
//
// 存在前提：AIConfig.api_key 与 TechWelfareGateway.api_key 曾以明文写在 sqlite 里，
// 任何能读到库文件的人（备份、误传的 zip、拖库）都直接拿到上游密钥。
// 这里只做静态加密，不解决传输与授权——读取接口仍必须自己脱敏。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// 密文格式前缀，用于区分"已封装"与历史明文，读取时可安全直通过去。
const prefix = "enc:v1:"

const secretFile = "crypto_secret.key"

var (
	keyOnce sync.Once
	key     []byte
)

// loadKey 解析顺序与 pkg/jwt 的签名密钥一致：环境变量 CRYPTO_SECRET > crypto_secret.key > 生成并落盘。
// 密钥不落源码，否则加密只是给数据库文件换了层包装纸。
func loadKey() []byte {
	if s := strings.TrimSpace(os.Getenv("CRYPTO_SECRET")); s != "" {
		return deriveKey(s)
	}
	if data, err := os.ReadFile(secretFile); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return deriveKey(s)
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		log.Fatalf("[Security] 生成配置加密密钥失败: %v", err)
	}
	generated := fmt.Sprintf("%x", raw)
	if err := os.WriteFile(secretFile, []byte(generated), 0600); err != nil {
		log.Printf("[Security][Warn] 无法写入 %s，本次会话密钥重启后已存配置将无法解密: %v", secretFile, err)
	} else {
		log.Printf("[Security] 未设置 CRYPTO_SECRET，已生成随机配置加密密钥并写入 %s（0600）；生产环境建议显式设置 CRYPTO_SECRET 并纳入备份", secretFile)
	}
	return deriveKey(generated)
}

// deriveKey 允许任意长度的口令；AES-GCM 需要固定 32 字节，故统一做摘要。
func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func box() cipher.AEAD {
	keyOnce.Do(func() { key = loadKey() })
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	return gcm
}

// IsSealed 判断字段值是否已是本包封装出的密文。
func IsSealed(value string) bool {
	return strings.HasPrefix(value, prefix)
}

// Seal 加密明文；空串原样返回，避免把"没有密钥"写成一段可解密的花费。
// 已封装的值不会被二次封装，重复保存不会产生套娃密文。
func Seal(plaintext string) (string, error) {
	if plaintext == "" || IsSealed(plaintext) {
		return plaintext, nil
	}
	aead := box()
	if aead == nil {
		return "", errors.New("配置加密密钥不可用")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机初始向量失败: %w", err)
	}
	ciphertext := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Open 解密；不带封装前缀的历史明文原样返回，升级前的库不会因为读不出而报错。
// 密文被篡改或换了密钥时会报错，而不是返回一段垃圾明文给上游。
func Open(value string) (string, error) {
	if !IsSealed(value) {
		return value, nil
	}
	aead := box()
	if aead == nil {
		return "", errors.New("配置加密密钥不可用")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", errors.New("密文编码损坏")
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("密文长度不合法")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("解密失败：加密密钥与入库时不一致，或密文已被篡改")
	}
	return string(plaintext), nil
}

// Mask 给界面看的脱敏串：只露首尾各 4 位。
// 传入封装好的密文时先解密再脱敏，调用方不必关心库里存的是哪种形态。
func Mask(value string) string {
	if value == "" {
		return ""
	}
	plaintext := value
	if IsSealed(value) {
		opened, err := Open(value)
		if err != nil {
			return "****"
		}
		plaintext = opened
	}
	if len(plaintext) <= 8 {
		return "****"
	}
	return plaintext[:4] + "****" + plaintext[len(plaintext)-4:]
}
