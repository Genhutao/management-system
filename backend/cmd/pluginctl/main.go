// Command pluginctl 是插件作者的签名工具（计划《学管会系统_插件能力扩展计划.md》§3.2）。
//
// 三条子命令：
//
//	pluginctl keygen [-out <目录>]                    出一对开发者密钥（私钥默认落当前目录）
//	pluginctl sign   -key <私钥> <插件目录>            给目录里的 manifest + 可执行文件签名
//	pluginctl verify -pub <公钥文件或目录> <插件目录>   不启动主程序就自查这份东西能不能被收
//
// 为什么单独做这个工具：**私钥由使用方本人保管**（已拍板），所以生成密钥这一步必须
// 是他手敲的命令，不是谁替他代办的；而 verify 存在的理由是——签名没过时服务器面板上
// 只会显示一句"签名不符"，作者本地根本不知道是 manifest 换了个空格还是 exe 少传了几 KB。
// 没有这条自查命令，"只有签名插件能被加载"第一个挡住的就是作者本人。
//
// 这个工具不联网、不碰数据库、不读任何系统密钥。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xgh-system/internal/plugins"
)

const usage = `用法：
  pluginctl keygen [-out <目录>]
  pluginctl sign   -key <私钥文件> <插件目录>
  pluginctl verify -pub <公钥文件|目录>[,<更多>…] <插件目录>

密钥只该出现在保管它的那台机器上：私钥文件是 0600 的 base64 种子，
请放在仓库外并和 jwt_secret.key 一起进备份清单。`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = runKeygen(os.Args[2:])
	case "sign":
		err = runSign(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Println(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "不认识的子命令 %q\n\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败：", err)
		os.Exit(1)
	}
}

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", ".", "密钥文件写到哪个目录（请选仓库外）")
	name := fs.String("name", "developer", "文件名前缀，便于一次管好几把钥匙")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("生成密钥失败：%w", err)
	}
	kid := plugins.KeyID(pub)
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return fmt.Errorf("建目录失败：%w", err)
	}
	keyPath := filepath.Join(*out, fmt.Sprintf("%s-%s.key", *name, kid))
	pubPath := filepath.Join(*out, fmt.Sprintf("%s-%s.pub", *name, kid))
	// 只存 32 字节种子：整把私钥能由种子重建，备份少一半内容，也少一半抄错的机会
	seed := priv.Seed()
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return fmt.Errorf("写私钥失败：%w", err)
	}
	if _, err := os.Stat(pubPath); err == nil {
		return fmt.Errorf("公钥文件 %s 已存在，不覆盖（要换钥匙先自己确认旧的那把怎么处理）", pubPath)
	}
	pubLine := base64.StdEncoding.EncodeToString(pub) + "\n"
	if err := os.WriteFile(pubPath, []byte(pubLine), 0o644); err != nil {
		return fmt.Errorf("写公钥失败：%w", err)
	}

	fmt.Printf("开发者 kid：%s\n", kid)
	fmt.Printf("私钥（只留在保管它的机器上，别进仓库）：%s\n", keyPath)
	fmt.Printf("公钥：%s\n", pubPath)
	fmt.Println("\n公钥内容：")
	fmt.Print(pubLine)
	fmt.Println(`
接下来两选一（也可以都做）：
  1. 把公钥文件放进服务器运行目录的 plugin_keys/（与 plugins/ 同级，**不要放进 plugins/ 里面**）；
  2. 把上面那行 base64 填进 backend/internal/plugins/trusted_keys.go 的内置锚，重新编译发布包。
私钥丢了 = 已签插件仍按指纹运行，但新插件永远签不出来；私钥泄露 = 别人能造"我们信任的开发者"。
两种情况的止血都是：撤公钥 + 换一把新的（换内置锚要重编主程序，这是刻意的）。`)
	return nil
}

// readPrivateKey 读 keygen 写出的那种文件：一行 base64 的 32 字节种子。
func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不到私钥：%w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("私钥文件不是合法 base64：%w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("私钥长度 %d 字节，Ed25519 种子应是 %d 字节", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// digestsOf 一个插件目录里被签的那两份内容的摘要。与扫描期共用同一套解析规则，
// 否则会出现"签的是 A 文件、加载器验的是 B 文件"这种当场说不清的事。
func digestsOf(dir string) (manifestRaw []byte, execPath, manifestDigest, execDigest string, err error) {
	manifestRaw, err = os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, "", "", "", fmt.Errorf("读不到 manifest.json：%w", err)
	}
	m, err := plugins.ManifestFrom(manifestRaw)
	if err != nil {
		return nil, "", "", "", err
	}
	execPath, err = plugins.ExecPathFor(dir, m)
	if err != nil {
		return nil, "", "", "", err
	}
	manifestDigest = plugins.DigestOfBytes(manifestRaw)
	if execDigest, err = plugins.DigestFile(execPath); err != nil {
		return nil, "", "", "", fmt.Errorf("算可执行文件摘要失败：%w", err)
	}
	return manifestRaw, execPath, manifestDigest, execDigest, nil
}

func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "私钥文件（keygen 出来的那个 .key）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := fs.Arg(0)
	if dir == "" {
		return errors.New("要签哪个目录没给。例：pluginctl sign -key dev.key ./plugins/diskusage")
	}
	if *keyPath == "" {
		return errors.New("-key 必填：签名要有私钥，公钥只能验不能签")
	}
	priv, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	_, execPath, manifestDigest, execDigest, err := digestsOf(dir)
	if err != nil {
		return err
	}
	sig := plugins.SignDigests(priv, manifestDigest, execDigest)
	out := filepath.Join(dir, plugins.SignatureFileName)
	body, err := sig.Marshal()
	if err != nil {
		return fmt.Errorf("序列化签名失败：%w", err)
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return fmt.Errorf("写 %s 失败：%w", out, err)
	}
	fmt.Printf("已签名：%s\n", out)
	fmt.Printf("  开发者 kid      ：%s\n", sig.Kid)
	fmt.Printf("  manifest 摘要   ：%s\n", manifestDigest)
	fmt.Printf("  可执行文件摘要  ：%s（%s）\n", execDigest, execPath)
	fmt.Println("签完之后这两份文件再动过任何一个字都要重签；重签等于新插件，服务器上要重新授权。")
	return nil
}

// loadAnchorsFrom 从命令行给的公钥路径建信任锚集合。
// 给目录就吃里面的 *.pub，给文件就只吃它——**私钥文件（.key）一律不认**，
// 把私钥当公钥读会立刻报错，而不是"看起来能用"。
func loadAnchorsFrom(spec string) (*plugins.AnchorSet, []string, error) {
	var lines []string
	var loaded []string
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, nil, fmt.Errorf("公钥路径 %s 读不到：%w", p, err)
		}
		var files []string
		if st.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, nil, fmt.Errorf("公钥目录 %s 读不到：%w", p, err)
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".pub") {
					files = append(files, filepath.Join(p, e.Name()))
				}
			}
			if len(files) == 0 {
				return nil, nil, fmt.Errorf("公钥目录 %s 里一个 .pub 都没有", p)
			}
		} else {
			files = []string{p}
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				return nil, nil, fmt.Errorf("公钥文件 %s 读不到：%w", f, err)
			}
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					lines = append(lines, line)
				}
			}
			loaded = append(loaded, f)
		}
	}
	if len(lines) == 0 {
		return nil, nil, errors.New("没有 -pub：至少要给一个公钥文件或目录")
	}
	return plugins.NewAnchorSet(lines, "命令行 -pub"), loaded, nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pub := fs.String("pub", "", "公钥文件或目录（多个用逗号分隔）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := fs.Arg(0)
	if dir == "" {
		return errors.New("要验哪个目录没给。例：pluginctl verify -pub ./plugin_keys ./plugins/diskusage")
	}
	anchors, loaded, err := loadAnchorsFrom(*pub)
	if err != nil {
		return err
	}
	fmt.Printf("用了 %d 个公钥文件：%s\n", len(loaded), strings.Join(loaded, "、"))
	fmt.Printf("可信开发者：%s\n\n", strings.Join(anchors.Kids(), "、"))

	_, execPath, manifestDigest, execDigest, err := digestsOf(dir)
	if err != nil {
		return err
	}
	sigPath := filepath.Join(dir, plugins.SignatureFileName)
	raw, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("目录里没有 %s —— 这个插件还没签名。先跑：pluginctl sign -key <私钥> %s",
			plugins.SignatureFileName, dir)
	}
	sig, err := plugins.ParseSignature(raw)
	if err != nil {
		return fmt.Errorf("%s 读不懂：%w", sigPath, err)
	}
	// 与加载器走同一条判定：把"哪一步不对"原样报出来，服务器面板上只有这一句的一半
	if err := anchors.Verify(sig, manifestDigest, execDigest); err != nil {
		fmt.Printf("验不过：%v\n", err)
		switch {
		case errors.Is(err, plugins.ErrUnknownKey):
			fmt.Printf("  这份是 kid=%s 签的，而你给的公钥里没有他。要么把它的 .pub 加进 -pub，要么这份东西不该被收。\n", sig.Kid)
		case errors.Is(err, plugins.ErrManifestChanged):
			fmt.Printf("  manifest.json 与签名时不一致：签名里 %s，磁盘上 %s\n", sig.Manifest, manifestDigest)
		case errors.Is(err, plugins.ErrExecChanged):
			fmt.Printf("  可执行文件与签名时不一致（%s）：签名里 %s，磁盘上 %s\n", filepath.Base(execPath), sig.Exec, execDigest)
		case errors.Is(err, plugins.ErrBadSignature):
			fmt.Println("  两份摘要都对得上，但签名本身验不过：要么用错了私钥，要么 signature 文件被改过。")
		}
		return errors.New("签名校验未通过")
	}
	fmt.Printf("签名通过：开发者 kid=%s，两份内容与签名时逐字节一致。\n", sig.Kid)
	fmt.Println("投到服务器上之后还要重启主程序才会被扫到，然后在「插件加载器」页授权一次。")
	return nil
}
