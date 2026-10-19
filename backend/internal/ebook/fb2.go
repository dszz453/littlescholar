package ebook

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseFB2 解析 FictionBook（.fb2）。
//
// FB2 就是一份 XML：<body> 里按 <section> 分层，<binary> 里是 base64 图片。
// 结构比 MOBI 干净得多，直接遍历 token 转 HTML 即可，不需要中间表。
func ParseFB2(file string) (*Book, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBookBytes {
		return nil, fmt.Errorf("文件太大")
	}

	// 第一遍：把 <binary> 里的图片解出来（正文里用 #id 引用）。
	images := map[string]Image{}
	title, author := "", ""
	{
		dec := xml.NewDecoder(bytes.NewReader(data))
		var curBinaryID, curBinaryMIME string
		var curBinary strings.Builder
		var inTitleInfo bool
		var lastField string
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("FB2 解析失败: %w", err)
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "binary":
					curBinaryID, curBinaryMIME = attr(t, "id"), attr(t, "content-type")
					_ = curBinaryMIME // 图片类型由 magic 判，MIME 只作参考
					curBinary.Reset()
				case "title-info":
					inTitleInfo = true
				case "book-title":
					if inTitleInfo {
						lastField = "title"
					}
				case "first-name", "last-name":
					if inTitleInfo {
						lastField = "author"
					}
				}
			case xml.CharData:
				s := strings.TrimSpace(string(t))
				if s == "" {
					continue
				}
				if curBinaryID != "" {
					curBinary.Write(t)
				}
				if inTitleInfo {
					switch lastField {
					case "title":
						title += s
					case "author":
						if author != "" {
							author += " "
						}
						author += s
					}
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "binary":
					if curBinaryID != "" {
						raw, derr := base64.StdEncoding.DecodeString(stripSpace(curBinary.String()))
						if derr == nil && looksLikeImage(raw) {
							name := "fb2_" + sanitizeID(curBinaryID) + guessImageExt(raw)
							images[curBinaryID] = Image{Name: name, Data: raw}
						}
					}
					curBinaryID = ""
				case "title-info":
					inTitleInfo = false
				case "book-title", "first-name", "last-name":
					lastField = ""
				}
			}
		}
	}

	// 第二遍：渲染正文
	var (
		chapters []Chapter
		cur      strings.Builder
		curTitle strings.Builder
		imgList  []Image
	)
	flush := func() {
		h := CleanHTML(cur.String(), nil)
		if strings.TrimSpace(stripTags(h)) != "" || strings.Contains(h, "<img") {
			t := strings.TrimSpace(curTitle.String())
			chapters = append(chapters, Chapter{Title: t, HTML: h})
		}
		cur.Reset()
		curTitle.Reset()
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	inBody, inTitle, titleText := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("FB2 解析失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			// 这里曾经只处理了章节标题的文本，正文全丢 ——
			// 结果是"目录正常、每章点开都是空白"。正文必须原样写进 cur，
			// 转义交给后面的 CleanHTML 统一做（它是解析成树再渲染的，
			// 手写 EscapeString 反而会双重转义）。
			if titleText {
				// 章节标题要**两边都写**：curTitle 供 EPUB 的目录（toc.ncx），
				// cur 供正文里的 <h2>。只写一边就是"目录有标题、正文里标题是空的"。
				txt := strings.TrimSpace(string(t))
				curTitle.WriteString(txt)
				cur.WriteString(txt)
			} else if inBody {
				cur.WriteString(string(t))
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "body":
				inBody = true
			case "section":
				if inBody {
					flush()
				}
			case "title":
				if inBody {
					inTitle = true
					cur.WriteString("<h2>")
				}
			case "subtitle":
				if inBody {
					cur.WriteString("<h3>")
				}
			case "p":
				if inBody {
					if inTitle {
						titleText = true
						cur.WriteString(" ")
					} else {
						cur.WriteString("<p>")
					}
				}
			case "emphasis":
				cur.WriteString("<em>")
			case "strong":
				cur.WriteString("<strong>")
			case "strikethrough":
				cur.WriteString("<s>")
			case "sub":
				cur.WriteString("<sub>")
			case "sup":
				cur.WriteString("<sup>")
			case "empty-line":
				cur.WriteString("<p><br/></p>")
			case "image":
				if inBody {
					href := attr(t, "href")
					if id := strings.TrimPrefix(href, "#"); id != "" {
						if im, ok := images[id]; ok {
							cur.WriteString(`<img src="../images/` + im.Name + `" alt=""/>`)
							imgList = append(imgList, im)
						}
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "body":
				inBody = false
			case "title":
				if inTitle {
					cur.WriteString("</h2>")
					inTitle = false
					titleText = false
				}
			case "subtitle":
				cur.WriteString("</h3>")
			case "p":
				if inBody && !inTitle {
					cur.WriteString("</p>")
				}
				titleText = false
			case "emphasis", "strong", "strikethrough", "sub", "sup":
				switch t.Name.Local {
				case "emphasis":
					cur.WriteString("</em>")
				case "strong":
					cur.WriteString("</strong>")
				case "strikethrough":
					cur.WriteString("</s>")
				case "sub":
					cur.WriteString("</sub>")
				case "sup":
					cur.WriteString("</sup>")
				}
			}
		}
	}
	flush()

	if len(chapters) == 0 {
		return nil, fmt.Errorf("这本书没有可读内容")
	}
	// 同一个 id 可能被引用多次，去重后只放一份进 EPUB
	seen := map[string]bool{}
	var uniq []Image
	for _, im := range imgList {
		if seen[im.Name] {
			continue
		}
		seen[im.Name] = true
		uniq = append(uniq, im)
	}
	b := &Book{
		Title:    strings.TrimSpace(title),
		Author:   strings.TrimSpace(author),
		Chapters: chapters,
		Images:   uniq,
	}
	if len(uniq) > 0 {
		b.Cover = uniq[0].Name
	}
	return b, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func stripSpace(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
