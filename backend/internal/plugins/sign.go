package plugins

// sign.go —— 开发者签名准入（计划《学管会系统_插件能力扩展计划.md》§3）。
//
// 一句话：**能往 plugins/ 丢文件，不再等于能让一个插件出现在可授权列表里**。
// P1 的信任门回答的是"人批没批"，签名回答的是"这份东西确实是那个作者写的、
// 且一个字都没被改过"。两件事都要：只有指纹则第一次批准没人背书，
// 只有签名则换文件后仍能跑（作者本人换的也算）。
//
// 算法只有 Ed25519：标准库 crypto/ed25519，纯 Go，CGO=0 下 Windows/Linux 都能编，
// 不引入任何第三方签名库——这条是上级口径的延续。
//
// 签名对象是 **两份摘要拼起来**：sha256(manifest.json 字节) ‖ sha256(可执行文件字节)。
// 为什么不只签 manifest：那样可以签一份"我只读"的清单、再投一个会写库的二进制，
// 指纹机制救不了它（指纹只保证"和授权时那份一致"，不保证"这份就是被批准的那份"）。
// 为什么把两份摘要也写进 signature 文件：只有一串签名的话，验失败时说不清是
// manifest 变了还是 exe 变了，作者本地自查会一头雾水。摘要本身被签名覆盖，
// 改摘要就等于伪造签名。

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SignatureFileName 插件目录里那份签名的文件名。
// 单独一个文件而不是 manifest 里的字段：签名放进被签的文档里是自我引用，哈希算不干净。
const SignatureFileName = "signature"

// 六种失败各自独立，因为**下一步动作完全不同**：
// 少文件是"找作者要一份完整的"，摘要不符是"文件被改过（可能是篡改）"，
// 不认识的 kid 是"这个人我们没信任"。合成一句"签名无效"等于让运维自己猜。
var (
	ErrSignatureMissing    = errors.New("没有签名文件")
	ErrSignatureUnreadable = errors.New("签名文件读不懂")
	ErrUnknownKey          = errors.New("签名者不在信任锚里")
	ErrManifestChanged     = errors.New("manifest.json 与签名时不一致")
	ErrExecChanged         = errors.New("可执行文件与签名时不一致")
	ErrBadSignature        = errors.New("签名本身对不上（伪造或损坏）")
)

// Signature 是 signature 文件里那一行 JSON。
type Signature struct {
	Kid      string `json:"kid"`
	Manifest string `json:"manifest"` // sha256(manifest.json) 的 hex
	Exec     string `json:"exec"`     // sha256(可执行文件) 的 hex
	Value    string `json:"sig"`      // base64(Ed25519 签名)
}

// DigestOfBytes 一份字节流的 sha256（hex）。导出给签名工具用，理由同 ManifestFrom。
func DigestOfBytes(b []byte) string { return digestOf(b) }

// digestOf 一份字节流的 sha256，hex 表示（与 loader 里的文件指纹同一套写法）。
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// signedMessage 被签的那 64 字节：两份摘要首尾相接。
func signedMessage(manifestHex, execHex string) ([]byte, error) {
	m, err := hex.DecodeString(strings.TrimSpace(manifestHex))
	if err != nil || len(m) != sha256.Size {
		return nil, fmt.Errorf("%w：manifest 摘要不是 64 位十六进制", ErrSignatureUnreadable)
	}
	e, err := hex.DecodeString(strings.TrimSpace(execHex))
	if err != nil || len(e) != sha256.Size {
		return nil, fmt.Errorf("%w：exec 摘要不是 64 位十六进制", ErrSignatureUnreadable)
	}
	return append(m, e...), nil
}

// KeyID 公钥的前 8 字节 hex，作为"这是哪位开发者"的短标识。
// 面板与审计里都显示它：只知道"有签名"不够，得知道是**谁**签的。
func KeyID(pub ed25519.PublicKey) string {
	return hex.EncodeToString(pub[:8])
}

// SignDigests 用私钥给两份**摘要**签名，返回可直接写盘的 Signature。
// 传摘要而不是文件字节：一个插件的可执行文件可能几十上百 MB，为了签一次把它整个读进
// 内存没必要（算摘要本来就是流式的），而且加载器与工具都已经有 DigestFile。
// cmd/pluginctl 与测试共用这一份实现——签名逻辑出现第二套写法，迟早一边能验一边不能验。
func SignDigests(priv ed25519.PrivateKey, manifestDigest, execDigest string) Signature {
	pub := priv.Public().(ed25519.PublicKey)
	sig := Signature{Kid: KeyID(pub), Manifest: manifestDigest, Exec: execDigest}
	msg, err := signedMessage(manifestDigest, execDigest)
	if err != nil { // 摘要由调用方算好传进来，走到这里说明调用方给的不是 64 位 hex
		panic("plugins.SignDigests: 摘要不合式: " + err.Error())
	}
	sig.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	return sig
}

// Marshal 把签名序列化成 signature 文件的内容（一行 JSON + 换行）。
func (s Signature) Marshal() ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseSignature 读 signature 文件的字节。缺字段一律拒：
// 少一个 exec 就当"没签 exec"放行，等于把这条防线最关键的那半份悄悄拆掉。
func ParseSignature(raw []byte) (Signature, error) {
	var s Signature
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%w: %v", ErrSignatureUnreadable, err)
	}
	if strings.TrimSpace(s.Kid) == "" || strings.TrimSpace(s.Value) == "" ||
		strings.TrimSpace(s.Manifest) == "" || strings.TrimSpace(s.Exec) == "" {
		return s, fmt.Errorf("%w：kid/manifest/exec/sig 四个字段都得有", ErrSignatureUnreadable)
	}
	return s, nil
}

// AnchorSet 被信任的开发者公钥集合。
//
// 两个来源（计划 §3.2）：编译内置的出厂锚 + 运行目录 plugin_keys/*.pub。
// 界面上加公钥那条路**明确不做**——那等于"能授权插件的人可以自己造可信开发者"，
// 信任门自己把自己绕过去了。
type AnchorSet struct {
	keys   map[string]ed25519.PublicKey
	source map[string]string // kid -> 来源说明，出错时要说得出"是哪把钥匙的文件坏了"
}

// NewAnchorSet 从 base64 公钥列表建集合（内置锚走这条路）。
// 认不出的条目不报错、只跳过并记日志：一把钥匙的格式问题不该让整个加载器起不来，
// 但也不能不出现在日志里——静默少一个可信开发者比报错更难查。
func NewAnchorSet(entries []string, source string) *AnchorSet {
	a := &AnchorSet{keys: map[string]ed25519.PublicKey{}, source: map[string]string{}}
	for _, line := range entries {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := a.add(line, source); err != nil {
			log.Printf("[Plugins] 信任锚里一条认不出，已跳过（来源 %s）: %v", source, err)
		}
	}
	return a
}

func (a *AnchorSet) add(b64, source string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return fmt.Errorf("不是合法 base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("公钥长度 %d 字节，Ed25519 应是 %d 字节", len(raw), ed25519.PublicKeySize)
	}
	pub := ed25519.PublicKey(raw)
	a.keys[KeyID(pub)] = pub
	a.source[KeyID(pub)] = source
	return nil
}

// Empty 一把可信钥匙都没有。此时**任何插件都进不了可授权列表**（失败关闭）。
func (a *AnchorSet) Empty() bool { return a == nil || len(a.keys) == 0 }

// Kids 当前被信任的开发者列表，按 kid 排序（面板与日志要说得出"我们信谁"）。
func (a *AnchorSet) Kids() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.keys))
	for kid := range a.keys {
		out = append(out, kid)
	}
	sort.Strings(out)
	return out
}

// KeysDirFor 公钥目录：PLUGIN_KEYS_DIR 覆盖，默认是插件目录的兄弟目录 plugin_keys/。
// **刻意不放在 plugins/ 里面**：那个目录的写权是"丢插件的人"也有的，
// 公钥放进去等于谁能丢文件谁就能自造可信开发者。
func KeysDirFor(pluginsDir string) string {
	if v := strings.TrimSpace(os.Getenv("PLUGIN_KEYS_DIR")); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(pluginsDir), "plugin_keys")
}

// LoadAnchors 内置锚 + 公钥目录，合成一次校验要用的集合。
// 目录不存在是常态（还没加过任何开发者），不算错误。
func LoadAnchors(pluginsDir string) *AnchorSet {
	a := NewAnchorSet(embeddedAnchorKeys, "编译内置")
	dir := KeysDirFor(pluginsDir)
	files, err := os.ReadDir(dir)
	if err != nil {
		return a
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".pub") {
			continue // 只认 .pub：把私钥误放进来时**不能**顺手当公钥用
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			log.Printf("[Plugins] 公钥文件 %s 读不到，已跳过: %v", f.Name(), err)
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if err := a.add(line, filepath.Join(dir, f.Name())); err != nil {
				log.Printf("[Plugins] 公钥文件 %s 里一条认不出，已跳过: %v", f.Name(), err)
			}
		}
	}
	return a
}

// Verify 检查两份内容的**摘要**是否被某位可信开发者签过。
// 传摘要而不是原始字节：加载器本来就是流式算摘要的（一个插件的可执行文件可能几百 MB，
// 不该为了验签再整份读进内存），而摘要本身被签名覆盖，所以"比对摘要 + 验签名"
// 与"比对字节"完全等价。
//
// 返回的 error 一定能用 errors.Is 对上上面那六个哨兵之一，调用方据此分状态。
//
// 顺序是刻意的：先认人（kid 在不在锚里），再对文件（两份摘要），最后验签名。
// 反过来会让"伪造者随便填一个已知 kid + 改过的文件"多暴露一次内部状态。
func (a *AnchorSet) Verify(sig Signature, manifestDigest, execDigest string) error {
	if a.Empty() {
		// 一把可信钥匙都没有时不说"签名者不认"——那会让人去查签名，
		// 而真正的事实是"这台服务还没配任何开发者公钥"。
		return fmt.Errorf("%w：这台服务没有配置任何可信开发者公钥（内置锚为空且 plugin_keys/ 里没有 .pub）",
			ErrUnknownKey)
	}
	pub, ok := a.keys[sig.Kid]
	if !ok {
		return fmt.Errorf("%w：kid=%s（当前可信：%s）", ErrUnknownKey, sig.Kid, joinKids(a.Kids()))
	}
	if got := strings.ToLower(strings.TrimSpace(manifestDigest)); got != strings.ToLower(strings.TrimSpace(sig.Manifest)) {
		return fmt.Errorf("%w：签名里 %s…，现在磁盘上 %s…", ErrManifestChanged, short(sig.Manifest), short(got))
	}
	if got := strings.ToLower(strings.TrimSpace(execDigest)); got != strings.ToLower(strings.TrimSpace(sig.Exec)) {
		return fmt.Errorf("%w：签名里 %s…，现在磁盘上 %s…", ErrExecChanged, short(sig.Exec), short(got))
	}
	msg, err := signedMessage(sig.Manifest, sig.Exec)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig.Value))
	if err != nil {
		return fmt.Errorf("%w：sig 不是合法 base64: %v", ErrSignatureUnreadable, err)
	}
	if !ed25519.Verify(pub, msg, raw) {
		return fmt.Errorf("%w：kid=%s 的签名验不过（摘要与签名不匹配）", ErrBadSignature, sig.Kid)
	}
	return nil
}

func joinKids(kids []string) string {
	if len(kids) == 0 {
		return "无"
	}
	return strings.Join(kids, "、")
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
