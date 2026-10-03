// Command diskusage 是文件期·独立进程插件的演示插件（方案 P1 验收用）。
//
// 它同时是"插件作者须知"的可执行版本：协议怎么谈、哪些数可以报、报不出来时
// 该说什么，全在这一个文件里。刻意只用标准库、不读任何密钥、不开网络端口。
//
// M2 起它是四种只读组件的对照物：stat / note / list / table 各来一个，
// 于是"清单声明的类型"和"这一键回的形状"怎么对上，有一个能跑的例子可看。
//
// 编译并投放：
//
//	cd backend && go build -o ../deploy/plugins/diskusage/plugin.exe ./plugins-src/diskusage
//	cp backend/plugins-src/diskusage/manifest.json ../deploy/plugins/diskusage/
//
// 然后重启主程序，去侧栏「插件加载器」页下方的「插件管理」表里授权它。丢进去不等于上线。
// 作者视角的完整口径（协议、限制、排错对照表）见仓库根《学管会系统_插件开发指南.md》。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const protocolVersion = 1

type request struct {
	ID     int      `json:"id"`
	Op     string   `json:"op"`
	Keys   []string `json:"keys"`
	UserID uint     `json:"user_id"`
	Role   string   `json:"role"`
}

// value 是一份**按清单声明类型选用的并集**（M2 起）：
// stat / note 填 Text，list 填 Items，table 填 Columns + Rows，多余的字段服务端会整键判成形状不符。
// 刻意不在这里用 map[string]any 之类的自由结构：能塞进去的字段越多，越容易塞出一个界面认不出的形状。
type value struct {
	Text    string     `json:"text,omitempty"`
	Hint    string     `json:"hint,omitempty"`
	Level   string     `json:"level,omitempty"`
	Items   []item     `json:"items,omitempty"`
	Columns []column   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
}

type item struct {
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
	Hint  string `json:"hint,omitempty"`
	Level string `json:"level,omitempty"`
}

type column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type response struct {
	ID       int              `json:"id"`
	OK       bool             `json:"ok"`
	Protocol int              `json:"protocol,omitempty"`
	Values   map[string]value `json:"values,omitempty"`
	Error    string           `json:"error,omitempty"`
}

func main() {
	// 日志一律走 stderr：stdout 是对主程序的应答通道，多写一个字节都会让它读歪。
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "diskusage: "+format+"\n", args...)
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := json.NewEncoder(os.Stdout)

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			log("收到的不是 JSON: %v", err)
			continue
		}
		if err := out.Encode(handle(req)); err != nil {
			log("写 stdout 失败，退出: %v", err)
			return
		}
	}
	if err := in.Err(); err != nil {
		log("stdin 出错: %v", err)
	}
	// stdin 关闭 = 主程序走了。立刻退出，不留孤儿进程。
}

func handle(req request) response {
	switch req.Op {
	case "ping":
		return response{ID: req.ID, OK: true, Protocol: protocolVersion}
	case "data":
		return response{ID: req.ID, OK: true, Protocol: protocolVersion, Values: values(req.Keys)}
	default:
		// 不认识的 op 要自己说清楚，而不是回一个空 values 让对方猜
		return response{ID: req.ID, OK: false, Error: "不支持的 op: " + req.Op}
	}
}

// values 只算被问到的键。报不出来的键整个不放进 map——
// 放一个 0 或空串进去，界面上就会读成"真的是 0"。
func values(keys []string) map[string]value {
	out := map[string]value{}
	for _, k := range keys {
		switch k {
		case "data_disk":
			total, free, usedPct, err := diskUsage(".")
			if err != nil {
				// 算不出来就自报失败：这句话会原样进警示卡的 hint
				out[k] = value{Text: "读取失败", Hint: "磁盘信息读不到: " + err.Error(), Level: "warn"}
				continue
			}
			warn := usedPct >= 85
			level := "ok"
			if warn {
				level = "warn"
			}
			out[k] = value{
				Text:  fmt.Sprintf("%.0f%% 已用", usedPct),
				Hint:  fmt.Sprintf("共 %s，余 %s（口径：插件进程所在文件系统）", humanGB(total), humanGB(free)),
				Level: level,
			}
		case "plugins_dir":
			n, bytes, err := dirSize("..")
			if err != nil {
				out[k] = value{Text: "读取失败", Hint: "plugins/ 目录读不到: " + err.Error(), Level: "warn"}
				continue
			}
			out[k] = value{
				Text: humanGB(float64(bytes) / (1024 * 1024 * 1024)),
				Hint: fmt.Sprintf("plugins/ 下 %d 个文件（含未授权的，授权才生效）", n),
			}
		case "read_scope":
			out[k] = value{Text: readScopeNote()}
		case "dir_sizes":
			entries, err := siblingDirs()
			if err != nil {
				// list 组件的取值里不能塞 Text（服务端会整键判成形状不符），
				// 所以"读不到"本身写成一行——那一行说的就是这一栏当下的真相。
				out[k] = value{Items: []item{{
					Label: "plugins/ 目录",
					Value: "读取失败",
					Hint:  err.Error(),
					Level: "warn",
				}}}
				continue
			}
			out[k] = value{Items: dirItems(entries)}
		case "dir_table":
			entries, err := siblingDirs()
			if err != nil {
				out[k] = value{
					Text:  "读取失败",
					Hint:  "plugins/ 目录列不出来: " + err.Error(),
					Level: "warn",
				}
				continue
			}
			out[k] = value{Columns: dirColumns(), Rows: dirRows(entries)}
		}
	}
	return out
}

func humanGB(gb float64) string {
	switch {
	case gb >= 1:
		return fmt.Sprintf("%.1f GB", gb)
	case gb >= 1.0/1024:
		return fmt.Sprintf("%.0f MB", gb*1024)
	default:
		return fmt.Sprintf("%.0f KB", gb*1024*1024)
	}
}

// readScopeNote note 组件的那一段：这个进程到底在读什么。
// 刻意每一行都是一个可以核对的事实（路径、有没有网络、有没有密钥），
// 而不是"本插件安全可靠"这类没法核对的话——说明卡是给人核对用的。
func readScopeNote() string {
	cwd, err := os.Getwd()
	where := "工作目录 " + cwd
	if err != nil {
		where = "工作目录读不到: " + err.Error()
	}
	return strings.Join([]string{
		"只读两个路径：\".\"（算数据盘用量）与 \"..\"（也就是 plugins/ 这一层）。",
		"不联网、不开端口、不读密钥、不写任何文件；上面这些用 strace/lsof 对着这个进程看，应该一无所获。",
		"清单里没声明任何动作，所以这个插件点不出任何会改学校数据的东西。",
		where,
	}, "\n")
}

// dirEntry plugins/ 下一个插件目录的实况。
type dirEntry struct {
	name   string
	files  int
	bytes  int64
	exe    string // 清单里写的那个文件名，读不到清单时退回默认的 plugin.exe
	hasExe bool
	note   string // 没有 manifest.json 之类"这一行只能这么报"的情况
}

func siblingDirs() ([]dirEntry, error) {
	list, err := os.ReadDir("..")
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(list))
	for _, de := range list {
		if !de.IsDir() {
			continue // 散在 plugins/ 下的文件不是插件，列出来只会让人以为漏登记了什么
		}
		e := dirEntry{name: de.Name(), exe: "plugin.exe"}
		path := filepath.Join("..", e.name)
		if raw, err := os.ReadFile(filepath.Join(path, "manifest.json")); err == nil {
			var m struct {
				Exec string `json:"exec"`
			}
			// 清单坏了不影响这里报占用：那一栏照样是真的，只是可执行文件名退回默认。
			if json.Unmarshal(raw, &m) == nil && strings.TrimSpace(m.Exec) != "" {
				e.exe = strings.TrimSpace(m.Exec)
			}
		} else {
			e.note = "没有 manifest.json"
		}
		if _, err := os.Stat(filepath.Join(path, e.exe)); err == nil {
			e.hasExe = true
		}
		n, bytes, err := dirSize(path)
		if err != nil {
			e.note = "目录读不到: " + err.Error()
		} else {
			e.files, e.bytes = n, bytes
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// dirItems list 组件：一行一个目录，值就是占用。
// 空目录（"一个插件目录都没有"）照常回一个空数组——那是事实，不是"没回这一键"。
func dirItems(entries []dirEntry) []item {
	out := make([]item, 0, len(entries))
	for _, e := range entries {
		warn := ""
		hint := fmt.Sprintf("%d 个文件", e.files)
		if !e.hasExe {
			// 少了可执行文件的目录永远加载不起来，这一行的 warn 就是它的原因
			warn = "warn"
			hint += " · 缺 " + e.exe
		}
		if e.note != "" {
			warn = "warn"
			hint += " · " + e.note
		}
		out = append(out, item{
			Label: e.name,
			Value: humanGB(float64(e.bytes) / (1024 * 1024 * 1024)),
			Hint:  hint,
			Level: warn,
		})
	}
	return out
}

func dirColumns() []column {
	return []column{
		{Key: "dir", Label: "目录"},
		{Key: "files", Label: "文件数"},
		{Key: "size", Label: "占用"},
		{Key: "exe", Label: "可执行文件"},
	}
}

// dirRows table 组件：格子数必须等于 dirColumns 的长度，差一格服务端就把整键判成形状不符。
func dirRows(entries []dirEntry) [][]string {
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		exe := e.exe + "（在）"
		if !e.hasExe {
			exe = e.exe + "（缺）"
		}
		rows = append(rows, []string{
			e.name,
			strconv.Itoa(e.files),
			humanGB(float64(e.bytes) / (1024 * 1024 * 1024)),
			exe,
		})
	}
	return rows
}
