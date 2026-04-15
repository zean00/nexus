package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	mdhtml "github.com/yuin/goldmark/renderer/html"

	"nexus/internal/domain"
)

type markdownVariants struct {
	Plain        string
	Slack        string
	TelegramHTML string
	EmailHTML    string
	WhatsApp     string
}

var markdownEngine = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(mdhtml.WithHardWraps()),
)

type textRenderStyle struct {
	HeadingPrefix func(level int) string
	Heading       func(level int, text string) string
	BoldWrap      [2]string
	ItalicWrap    [2]string
	StrikeWrap    [2]string
	InlineCode    [2]string
	Link          func(text, href string) string
	CodeBlock     func(text string) string
}

var plainTextStyle = textRenderStyle{
	HeadingPrefix: func(level int) string { return strings.Repeat("#", level) + " " },
	BoldWrap:      [2]string{"**", "**"},
	ItalicWrap:    [2]string{"_", "_"},
	StrikeWrap:    [2]string{"~", "~"},
	InlineCode:    [2]string{"`", "`"},
	Link: func(text, href string) string {
		if href == "" || text == href {
			return text
		}
		if text == "" {
			return href
		}
		return text + " (" + href + ")"
	},
	CodeBlock: func(text string) string {
		return "```\n" + strings.TrimRight(text, "\n") + "\n```"
	},
}

var slackTextStyle = textRenderStyle{
	HeadingPrefix: func(level int) string { return strings.Repeat("#", level) + " " },
	Heading: func(level int, text string) string {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		return "*" + text + "*"
	},
	BoldWrap:   [2]string{"*", "*"},
	ItalicWrap: [2]string{"_", "_"},
	StrikeWrap: [2]string{"~", "~"},
	InlineCode: [2]string{"`", "`"},
	Link: func(text, href string) string {
		if href == "" && text == "" {
			return ""
		}
		if href == "" || text == href || text == "" {
			return "<" + href + ">"
		}
		return "<" + href + "|" + text + ">"
	},
	CodeBlock: func(text string) string {
		return "```\n" + strings.TrimRight(text, "\n") + "\n```"
	},
}

var whatsAppTextStyle = textRenderStyle{
	HeadingPrefix: func(level int) string { return strings.Repeat("#", level) + " " },
	BoldWrap:      [2]string{"*", "*"},
	ItalicWrap:    [2]string{"_", "_"},
	StrikeWrap:    [2]string{"~", "~"},
	InlineCode:    [2]string{"`", "`"},
	Link: func(text, href string) string {
		if href == "" || text == href {
			return text
		}
		if text == "" {
			return href
		}
		return text + ": " + href
	},
	CodeBlock: func(text string) string {
		return "```\n" + strings.TrimRight(text, "\n") + "\n```"
	},
}

func renderMarkdownVariants(markdown string) markdownVariants {
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return markdownVariants{}
	}
	var buf bytes.Buffer
	if err := markdownEngine.Convert([]byte(markdown), &buf); err != nil {
		fallback := strings.TrimSpace(markdown)
		return markdownVariants{
			Plain:        fallback,
			Slack:        fallback,
			TelegramHTML: stdhtml.EscapeString(fallback),
			EmailHTML:    "<p>" + stdhtml.EscapeString(fallback) + "</p>",
			WhatsApp:     fallback,
		}
	}
	emailHTML := strings.TrimSpace(buf.String())
	nodes, err := xhtml.ParseFragment(strings.NewReader(emailHTML), &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"})
	if err != nil {
		fallback := strings.TrimSpace(markdown)
		return markdownVariants{
			Plain:        fallback,
			Slack:        fallback,
			TelegramHTML: stdhtml.EscapeString(fallback),
			EmailHTML:    "<p>" + stdhtml.EscapeString(fallback) + "</p>",
			WhatsApp:     fallback,
		}
	}
	return markdownVariants{
		Plain:        strings.TrimSpace(renderTextNodes(nodes, plainTextStyle)),
		Slack:        strings.TrimSpace(renderTextNodes(nodes, slackTextStyle)),
		TelegramHTML: strings.TrimSpace(renderTelegramNodes(nodes)),
		EmailHTML:    emailHTML,
		WhatsApp:     strings.TrimSpace(renderTextNodes(nodes, whatsAppTextStyle)),
	}
}

func renderTextNodes(nodes []*xhtml.Node, style textRenderStyle) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		part := strings.TrimSpace(renderTextNode(node, style, 0))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func renderTextNode(node *xhtml.Node, style textRenderStyle, listDepth int) string {
	switch node.Type {
	case xhtml.TextNode:
		return collapseInlineWhitespace(stdhtml.UnescapeString(node.Data))
	case xhtml.ElementNode:
		switch node.Data {
		case "p":
			return strings.TrimSpace(renderTextChildren(node, style, listDepth))
		case "br":
			return "\n"
		case "strong", "b":
			return wrapStyledText(style.BoldWrap, renderTextChildren(node, style, listDepth))
		case "em", "i":
			return wrapStyledText(style.ItalicWrap, renderTextChildren(node, style, listDepth))
		case "del", "s":
			return wrapStyledText(style.StrikeWrap, renderTextChildren(node, style, listDepth))
		case "code":
			if node.Parent != nil && node.Parent.Type == xhtml.ElementNode && node.Parent.Data == "pre" {
				return extractNodeText(node)
			}
			return wrapStyledText(style.InlineCode, extractNodeText(node))
		case "pre":
			return style.CodeBlock(extractNodeText(node))
		case "blockquote":
			return prefixQuotedLines(renderTextChildren(node, style, listDepth))
		case "ul":
			return renderTextList(node, style, false, listDepth)
		case "ol":
			return renderTextList(node, style, true, listDepth)
		case "li":
			return strings.TrimSpace(renderTextListItem(node, style, listDepth))
		case "a":
			label := strings.TrimSpace(renderTextChildren(node, style, listDepth))
			href := nodeAttr(node, "href")
			if label == "" {
				label = href
			}
			return style.Link(label, href)
		case "table":
			return renderPlainTable(node)
		case "thead", "tbody", "tr", "th", "td", "div", "span":
			return renderTextChildren(node, style, listDepth)
		case "input":
			return ""
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := headingLevel(node.Data)
			text := strings.TrimSpace(renderTextChildren(node, style, listDepth))
			if style.Heading != nil {
				return style.Heading(level, text)
			}
			return style.HeadingPrefix(level) + text
		default:
			return renderTextChildren(node, style, listDepth)
		}
	default:
		return ""
	}
}

func renderTextChildren(node *xhtml.Node, style textRenderStyle, listDepth int) string {
	var b strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(renderTextNode(child, style, listDepth))
	}
	return normalizeInlineNewlines(b.String())
}

func renderTextList(node *xhtml.Node, style textRenderStyle, ordered bool, listDepth int) string {
	lines := make([]string, 0, 4)
	index := 1
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != xhtml.ElementNode || child.Data != "li" {
			continue
		}
		prefix := "- "
		if ordered {
			prefix = fmt.Sprintf("%d. ", index)
		}
		line := strings.TrimSpace(renderTextListItem(child, style, listDepth+1))
		if checkbox, ok := listItemCheckbox(child); ok {
			prefix = "- " + checkbox + " "
		}
		lines = append(lines, indentRendered(prefix+line, strings.Repeat(" ", len(prefix))))
		index++
	}
	return strings.Join(lines, "\n")
}

func renderTextListItem(node *xhtml.Node, style textRenderStyle, listDepth int) string {
	parts := make([]string, 0, 4)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if isTaskCheckbox(child) {
			continue
		}
		part := strings.TrimSpace(renderTextNode(child, style, listDepth))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

func renderTelegramNodes(nodes []*xhtml.Node) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		part := strings.TrimSpace(renderTelegramNode(node, 0))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func renderTelegramNode(node *xhtml.Node, listDepth int) string {
	switch node.Type {
	case xhtml.TextNode:
		return stdhtml.EscapeString(collapseInlineWhitespace(stdhtml.UnescapeString(node.Data)))
	case xhtml.ElementNode:
		switch node.Data {
		case "p":
			return strings.TrimSpace(renderTelegramChildren(node, listDepth))
		case "br":
			return "\n"
		case "strong", "b":
			return wrapStyledText([2]string{"<b>", "</b>"}, renderTelegramChildren(node, listDepth))
		case "em", "i":
			return wrapStyledText([2]string{"<i>", "</i>"}, renderTelegramChildren(node, listDepth))
		case "del", "s":
			return wrapStyledText([2]string{"<s>", "</s>"}, renderTelegramChildren(node, listDepth))
		case "code":
			code := stdhtml.EscapeString(extractNodeText(node))
			if node.Parent != nil && node.Parent.Type == xhtml.ElementNode && node.Parent.Data == "pre" {
				return code
			}
			return "<code>" + code + "</code>"
		case "pre":
			return "<pre>" + stdhtml.EscapeString(extractNodeText(node)) + "</pre>"
		case "blockquote":
			return prefixQuotedLines(stdhtml.UnescapeString(renderTelegramChildren(node, listDepth)))
		case "ul":
			return renderTelegramList(node, false, listDepth)
		case "ol":
			return renderTelegramList(node, true, listDepth)
		case "li":
			return strings.TrimSpace(renderTelegramListItem(node, listDepth))
		case "a":
			href := stdhtml.EscapeString(nodeAttr(node, "href"))
			label := strings.TrimSpace(renderTelegramChildren(node, listDepth))
			if label == "" {
				label = href
			}
			if href == "" {
				return label
			}
			return `<a href="` + href + `">` + label + `</a>`
		case "table":
			return "<pre>" + stdhtml.EscapeString(renderPlainTable(node)) + "</pre>"
		case "thead", "tbody", "tr", "th", "td", "div", "span":
			return renderTelegramChildren(node, listDepth)
		case "input":
			return ""
		case "h1", "h2", "h3", "h4", "h5", "h6":
			return "<b>" + strings.TrimSpace(renderTelegramChildren(node, listDepth)) + "</b>"
		default:
			return renderTelegramChildren(node, listDepth)
		}
	default:
		return ""
	}
}

func renderTelegramChildren(node *xhtml.Node, listDepth int) string {
	var b strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(renderTelegramNode(child, listDepth))
	}
	return normalizeInlineNewlines(b.String())
}

func renderTelegramList(node *xhtml.Node, ordered bool, listDepth int) string {
	lines := make([]string, 0, 4)
	index := 1
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != xhtml.ElementNode || child.Data != "li" {
			continue
		}
		prefix := "- "
		if ordered {
			prefix = fmt.Sprintf("%d. ", index)
		}
		if checkbox, ok := listItemCheckbox(child); ok {
			prefix = "- " + checkbox + " "
		}
		line := strings.TrimSpace(renderTelegramListItem(child, listDepth+1))
		lines = append(lines, indentRendered(prefix+line, strings.Repeat(" ", len(prefix))))
		index++
	}
	return strings.Join(lines, "\n")
}

func renderTelegramListItem(node *xhtml.Node, listDepth int) string {
	parts := make([]string, 0, 4)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if isTaskCheckbox(child) {
			continue
		}
		part := strings.TrimSpace(stdhtml.UnescapeString(renderTelegramNode(child, listDepth)))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

func renderPlainTable(node *xhtml.Node) string {
	rows := collectTableRows(node)
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, 0, len(rows[0]))
	for _, row := range rows {
		for col, cell := range row {
			if col >= len(widths) {
				widths = append(widths, 0)
			}
			if len(cell) > widths[col] {
				widths[col] = len(cell)
			}
		}
	}
	lines := make([]string, 0, len(rows)+1)
	for idx, row := range rows {
		cells := make([]string, 0, len(widths))
		for col := range widths {
			cell := ""
			if col < len(row) {
				cell = row[col]
			}
			cells = append(cells, padRight(cell, widths[col]))
		}
		lines = append(lines, strings.Join(cells, " | "))
		if idx == 0 && len(rows) > 1 {
			divider := make([]string, 0, len(widths))
			for _, width := range widths {
				divider = append(divider, strings.Repeat("-", max(width, 3)))
			}
			lines = append(lines, strings.Join(divider, "-|-"))
		}
	}
	return strings.Join(lines, "\n")
}

func collectTableRows(node *xhtml.Node) [][]string {
	rows := make([][]string, 0, 4)
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.ElementNode && current.Data == "tr" {
			row := make([]string, 0, 4)
			for cell := current.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type != xhtml.ElementNode || (cell.Data != "th" && cell.Data != "td") {
					continue
				}
				row = append(row, strings.TrimSpace(renderTextChildren(cell, plainTextStyle, 0)))
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return rows
}

func parseAwaitPrompt(prompt []byte) (string, []domain.RenderChoice, error) {
	var model struct {
		Title   string                `json:"title"`
		Body    string                `json:"body"`
		Choices []domain.RenderChoice `json:"choices"`
	}
	if len(prompt) > 0 {
		if err := json.Unmarshal(prompt, &model); err != nil {
			return "", nil, err
		}
	}
	text := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(model.Title), strings.TrimSpace(model.Body)}, "\n\n"))
	if text == "" {
		text = "The agent needs your input to continue."
	}
	return text, model.Choices, nil
}

func wrapStyledText(wrap [2]string, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	return wrap[0] + content + wrap[1]
}

func prefixQuotedLines(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[i] = "> " + strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

func indentRendered(text, indent string) string {
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func collapseInlineWhitespace(text string) string {
	return strings.NewReplacer("\r", "", "\t", " ").Replace(text)
}

func normalizeInlineNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	return text
}

func extractNodeText(node *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			b.WriteString(stdhtml.UnescapeString(current.Data))
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(b.String())
}

func nodeAttr(node *xhtml.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func isTaskCheckbox(node *xhtml.Node) bool {
	return node.Type == xhtml.ElementNode && node.Data == "input" && nodeAttr(node, "type") == "checkbox"
}

func listItemCheckbox(node *xhtml.Node) (string, bool) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if isTaskCheckbox(child) {
			if nodeAttr(child, "checked") != "" {
				return "[x]", true
			}
			return "[ ]", true
		}
	}
	return "", false
}

func headingLevel(tag string) int {
	switch tag {
	case "h1":
		return 1
	case "h2":
		return 2
	case "h3":
		return 3
	case "h4":
		return 4
	case "h5":
		return 5
	default:
		return 6
	}
}

func padRight(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
