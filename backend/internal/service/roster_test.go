package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitRosterNamesSeparators(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"顿号分隔", "李华、张明、王小红", []string{"李华", "张明", "王小红"}},
		{"换行分隔", "李华\n张明\r\n王小红", []string{"李华", "张明", "王小红"}},
		{"混用逗号分号空白", "李华, 张明；王小红 赵一", []string{"李华", "张明", "王小红", "赵一"}},
		{"带序号前缀", "1. 李华\n2、张明\n3)王小红", []string{"李华", "张明", "王小红"}},
		{"夹杂班级与楼栋噪声", "李华 高一(2)班 302 张明 1号楼", []string{"李华", "张明"}},
		{"重复姓名去重", "李华、李华、张明", []string{"李华", "张明"}},
		{"全角空格", "李华　张明", []string{"李华", "张明"}},
		{"空白输入", "   \n  ", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitRosterNames(tc.raw)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("期望空名单，实际 %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("输入 %q\n期望 %v\n实际 %v", tc.raw, tc.want, got)
			}
		})
	}
}

func TestSplitRosterNamesRejectsNonNames(t *testing.T) {
	got := SplitRosterNames("302、A、AB、李华")
	if !reflect.DeepEqual(got, []string{"李华"}) {
		t.Fatalf("纯数字与非汉字片段不应被当成姓名，实际 %v", got)
	}
}

func TestSplitRosterNamesRespectsCap(t *testing.T) {
	parts := make([]string, 0, MaxSubjectsPerReport+25)
	for i := 0; i < MaxSubjectsPerReport+25; i++ {
		parts = append(parts, "赵"+string(rune(0x4E00+i))+string(rune(0x5000+i)))
	}
	got := SplitRosterNames(strings.Join(parts, "、"))
	if len(got) != MaxSubjectsPerReport {
		t.Fatalf("应截断到上限 %d，实际 %d", MaxSubjectsPerReport, len(got))
	}
}
