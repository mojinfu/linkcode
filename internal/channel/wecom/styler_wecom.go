package wecom

import "fmt"

// WeComStyler formats messages for WeCom AI Bot Markdown.
// WeCom supports a subset of Markdown: **bold**, > quote, ``` code blocks.
type WeComStyler struct{}

func (WeComStyler) Box(title, content string) string {
	return fmt.Sprintf("```%s\n%s\n```", title, content)
}

func (WeComStyler) Bar(title string) string {
	return fmt.Sprintf("```%s\n```", title)
}

func (WeComStyler) Bold(text string) string {
	return "**" + text + "**"
}
