package wecom

import (
	"fmt"
	"math"
	"time"
)

// WeComStyler formats messages for WeCom AI Bot Markdown.
// WeCom supports a subset of Markdown: **bold**, > quote, ``` code blocks.
type WeComStyler struct{}

func (WeComStyler) Box(title, content string) string {
	return fmt.Sprintf("```%s\n%s\n```", title, content)
}

func (WeComStyler) Bar(title string) string {
	return "`" + title + "`"
}

func (WeComStyler) Bold(text string) string {
	return "**" + text + "**"
}

var zwspSuffixes = [...]string{
	"​",
	"​​",
	"​​​",
	"​​​​",
	"​​​​​",
	"​​​​​​​​​​​​​​​​​​​​",
	"​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​​",
}

func (WeComStyler) DiffSuffix(seq int) string {
	return zwspSuffixes[seq%len(zwspSuffixes)]
}

func (WeComStyler) Quote(text string) string {
	return "> " + text
}

func (WeComStyler) StreamWarning(remaining time.Duration) string {
	if remaining <= 0 {
		return "stream 即将断联，Agent 继续后台运行"
	}
	return fmt.Sprintf("⚡ stream %s 后断联，Agent 继续后台运行", formatDuration(remaining))
}

func (WeComStyler) Cost(prevCost, turnCost float64, symbol string) string {
	if math.IsNaN(prevCost) || math.IsNaN(turnCost) {
		return "cost ? + ?"
	}
	return fmt.Sprintf("cost %s%.2f + %s%.4f", symbol, prevCost, symbol, turnCost)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%d 小时 %d 分", h, m)
}
