package main

import (
	"fmt"
	"regexp"
)

func main() {
	pats := map[string]string{
		"building": `([东西南北]?\d+[号栋楼]+|[A-Za-z]\d*[号栋楼]+|[东西南北]区\d*号?[楼栋]?)`,
		"room":     `\b([1-9]\d{2,3}(?:室|房)?|\d+-\d{2,3})\b`,
		"class":    `(高[一二三123]|202[3-7]级?)\s*\(?(\d{1,2})\)?\s*班?`,
		"grade":    `(高一|高二|高三|高1|高2|高3)`,
		"name":     `[\p{Han}]{2,4}`,
	}
	for _, k := range []string{"building", "room", "class", "grade", "name"} {
		re, err := regexp.Compile(pats[k])
		if err != nil {
			fmt.Printf("%-9s COMPILE FAIL: %v\n", k, err)
			continue
		}
		fmt.Printf("%-9s ok=%v\n", k, re.MatchString("8栋 502 綾川星凛 高三(5)班 女 1号床 20230501"))
	}
}
