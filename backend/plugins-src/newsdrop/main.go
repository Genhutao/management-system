// Command newsdrop 是**会写数据的那类**插件的演示（计划 §5 / §5.3 / §8 的 S3 那六条验收用它跑）。
//
// 它与 diskusage 的差别只有一处，但那一处是整个安全设计的重心：
// 它多了一条 `actions` 声明，界面上因此多出一个按钮，点下去会往学校的播报队列里加一条稿子。
//
// **这个插件自己不发 HTTP 请求。** 它只算出"该发哪一个请求"，把 method/path/body 交给主程序，
// 由主程序带着**点按钮那个人自己的会话**去打本机接口。所以：
//
//   - 它拿不到别人的权限：部员点了只有部长能做的动作，被拦下的是部员自己的会话；
//   - 它拿不到口令：confirm=stepup 的口令由主程序当场收，一行都不进这根管道；
//   - 它改不了清单之外的接口：path 必须逐字等于签名清单里那一条，对不上就直接拒掉。
//
// 编译并投放（与 diskusage 同样三步，只是多一个签名）：
//
//	cd backend
//	go build -o .verify/s3-check/plugins/newsdrop/plugin.exe ./plugins-src/newsdrop
//	cp plugins-src/newsdrop/manifest.json .verify/s3-check/plugins/newsdrop/
//	go run ./cmd/pluginctl sign -key <你的私钥> .verify/s3-check/plugins/newsdrop
//
// 路径按各人的投放目录换（上面这组是本轮验收夹具 `backend/.verify/s3-check/` 用的）。
// 作者视角的完整口径见仓库根《学管会系统_插件开发指南.md》，动作那节是 §2.3。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

const protocolVersion = 1

// declaredPath 与 manifest.json 里 actions[0].path 一字一样。
// 这里是**故意重复写第二遍**的：插件回给主程序的路径必须自己拼一个出来，
// 如果它和清单不一致，主程序会判"越界"并拒掉——那正是这条要演示的行为。
const declaredPath = "/api/v1/publicity/broadcast/news"

type request struct {
	ID     int               `json:"id"`
	Op     string            `json:"op"`
	Keys   []string          `json:"keys"`
	UserID uint              `json:"user_id"`
	Role   string            `json:"role"`
	Key    string            `json:"key"`
	Params map[string]string `json:"params"`
}

type stat struct {
	Text  string `json:"text"`
	Hint  string `json:"hint,omitempty"`
	Level string `json:"level,omitempty"`
}

type actionRequest struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body"`
}

type response struct {
	ID       int             `json:"id"`
	OK       bool            `json:"ok"`
	Protocol int             `json:"protocol,omitempty"`
	Values   map[string]stat `json:"values,omitempty"`
	Error    string          `json:"error,omitempty"`
	Request  *actionRequest  `json:"request,omitempty"`
}

// produced 本进程**产出过**几个提交请求。注意它不是"写成了几条"：
// 插件看不到主程序那一跳的结果，把这两个数说成一个就是骗人。
var produced atomic.Int64

func main() {
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "newsdrop: "+format+"\n", args...)
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
}

func handle(req request) response {
	switch req.Op {
	case "ping":
		return response{ID: req.ID, OK: true, Protocol: protocolVersion}
	case "data":
		return response{ID: req.ID, OK: true, Protocol: protocolVersion, Values: values(req.Keys)}
	case "action":
		return act(req)
	default:
		return response{ID: req.ID, OK: false, Error: "不支持的 op: " + req.Op}
	}
}

// values 那块卡说的是"产出了几个请求"，不是"写成了几条"。
// 为什么不禁得住：插件这一侧确实不知道结果——发请求的是主程序，
// 报一个"已提交 N 条"会让人以为点一次就成一次，而超时那次可能一条都没进库。
func values(keys []string) map[string]stat {
	out := map[string]stat{}
	n := produced.Load()
	for _, k := range keys {
		if k == "produced" {
			level := "ok"
			hint := "本进程产出的提交请求数，不等于已写进库的条数（那一跳由主程序代做，结果去「播音组 · 今日新闻」页核对）。界面按纯文本显示这句，别写 markdown"
			if n == 0 {
				level = "warn"
				hint = "还没产出过任何提交请求。下方按钮点一次、由主程序代发一次。"
			}
			out[k] = stat{Text: fmt.Sprintf("%d 个请求", n), Hint: hint, Level: level}
		}
	}
	return out
}

// act 只把参数拼成"该发的那个请求"。**这里不发请求，也拿不到结果。**
func act(req request) response {
	if req.Key != "submit_news" {
		return response{ID: req.ID, OK: false, Error: "这个插件只实现了 submit_news，收到的是 " + req.Key}
	}
	title := strings.TrimSpace(req.Params["title"])
	content := strings.TrimSpace(req.Params["content"])
	if title == "" || content == "" {
		// 自己再判一遍空，虽然主程序已经按清单挡过：
		// 一条空标题的稿子进了播报队列，界面只会显示"录入成功"。
		return response{ID: req.ID, OK: false, Error: "标题与正文都得有内容"}
	}
	produced.Add(1)
	return response{
		ID: req.ID, OK: true, Protocol: protocolVersion,
		Request: &actionRequest{
			Method: "POST",
			Path:   declaredPath,
			Body: map[string]any{
				"title":    title,
				"content":  content,
				"category": "校园时讯",
				"keywords": "插件投递",
			},
		},
	}
}
