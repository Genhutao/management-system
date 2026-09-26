package model

import (
	"testing"
	"time"
)

func TestPeriodTypeMatches(t *testing.T) {
	// 2026-09-25 是周五，ISO 周数 39（奇数）；2026-09-26 周六、2026-09-27 周日
	friday := time.Date(2026, 9, 25, 9, 0, 0, 0, time.Local)
	saturday := time.Date(2026, 9, 26, 9, 0, 0, 0, time.Local)
	// 2026-12-28 是周一，ISO 周数 53（奇数）；2026-12-07 是周一，ISO 周数 50（偶数）
	weekOdd := time.Date(2026, 12, 28, 9, 0, 0, 0, time.Local)
	_, weekOddNo := weekOdd.ISOWeek()
	weekEven := time.Date(2026, 12, 7, 9, 0, 0, 0, time.Local) // ISO 周数 50（偶数）
	_, weekEvenNo := weekEven.ISOWeek()

	if weekOddNo%2 != 1 || weekEvenNo%2 != 0 {
		t.Fatalf("测试前提不成立：期望奇偶周各一，实际 %d / %d", weekOddNo, weekEvenNo)
	}

	cases := []struct {
		period string
		at     time.Time
		want   bool
		desc   string
	}{
		{PeriodDaily, saturday, true, "每日在周末也生效"},
		{PeriodWeekday, friday, true, "周内工作日在周五生效"},
		{PeriodWeekday, saturday, false, "周内工作日在周六不生效"},
		{PeriodWeekend, saturday, true, "周末在周六生效"},
		{PeriodWeekend, friday, false, "周末在周五不生效"},
		{"weekend", saturday, true, "种子数据的单数写法生效"},
		{"weekends", friday, false, "历史复数别名按周末处理"},
		{"weekdays", saturday, false, "历史复数别名按工作日处理"},
		{PeriodSingleWeek, weekOdd, true, "单周在奇数周生效"},
		{PeriodSingleWeek, weekEven, false, "单周在偶数周不生效"},
		{PeriodDoubleWeek, weekEven, true, "双周在偶数周生效"},
		{PeriodDoubleWeek, weekOdd, false, "双周在奇数周不生效"},
		{"", friday, true, "空值按每日处理"},
		{"garbage", friday, true, "未知取值不吞掉整条时段"},
	}

	for _, tc := range cases {
		if got := PeriodTypeMatches(tc.period, tc.at); got != tc.want {
			t.Errorf("%s: PeriodTypeMatches(%q) = %v, 期望 %v", tc.desc, tc.period, got, tc.want)
		}
	}
}

func TestCanonicalPeriodType(t *testing.T) {
	for _, v := range []string{"daily", "weekday", "weekend", "single_week", "double_week", "weekends", " weekday "} {
		got, ok := CanonicalPeriodType(v)
		if !ok {
			t.Errorf("CanonicalPeriodType(%q) 应被接受", v)
		}
		if got != NormalizePeriodType(v) {
			t.Errorf("CanonicalPeriodType(%q) = %q, 与 NormalizePeriodType 不一致", v, got)
		}
	}
	for _, v := range []string{"", "week", "每月", "WEEKEND"} {
		if got, ok := CanonicalPeriodType(v); ok {
			t.Errorf("CanonicalPeriodType(%q) = %q, 应被拒绝而不是静默接受", v, got)
		}
	}
}
