// Package ebook 把浏览器读不了的电子书转换成 EPUB。
//
// 为什么统一转 EPUB，而不是给每种格式写一个前端阅读器：
//   - EPUB 是唯一一个「有成熟、纯前端、离线可用」阅读器的开放格式（epub.js）；
//   - MOBI/AZW3/FB2/CBR 的排版语义高度重叠（章节 + 图片），
//     转一次就能复用同一套翻页、目录、字号、续读逻辑；
//   - 前端只依赖一个渲染器，不用为每种格式各写一份容错。
//
// 转换产物按资源 ID 落盘缓存：同一本书只在第一次打开时付出转换成本。
package ebook

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Chapter 一个章节。HTML 是**正文片段**（不含 <html>/<body>），
// 由 EPUB 生成器套上完整骨架。
type Chapter struct {
	Title string
	HTML  string
}

// Image EPUB 内的一张图片。Name 是文件名（bare name，不含目录）。
type Image struct {
	Name string
	Data []byte
}

// Book 转换后的中间形态。
type Book struct {
	Title   string
	Author  string
	Chapters []Chapter
	Images  []Image
	// FixedLayout 漫画类：一页一张图，按图片原始比例铺满。
	FixedLayout bool
	// Cover 封面图片名（在 Images 里）
	Cover string
}

// BuildEPUB 把 Book 写成 EPUB 文件。
//
// 结构按 EPUB 2（OPF + NCX）输出：epub.js 与各家阅读器对它的支持
// 比 EPUB 3 的 nav.xhtml 更普遍，而我们不需要 EPUB3 的任何新特性。
func BuildEPUB(b *Book, dst string) error {
	if b == nil {
		return fmt.Errorf("空书")
	}
	if len(b.Chapters) == 0 {
		return fmt.Errorf("这本书没有可读内容")
	}
	if strings.TrimSpace(b.Title) == "" {
		b.Title = "未命名"
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()

	zw := zip.NewWriter(f)

	// mimetype 必须是第一个文件、且不压缩（EPUB 规范的硬要求）
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		return err
	}

	if err := writeFile(zw, "META-INF/container.xml", containerXML); err != nil {
		return err
	}

	// 图片
	imgExts := make(map[string]string, len(b.Images))
	for i, im := range b.Images {
		name := im.Name
		if name == "" {
			name = fmt.Sprintf("img_%04d%s", i, guessImageExt(im.Data))
		}
		imgExts[name] = imageMIME(name)
		if err := writeBytes(zw, "OEBPS/images/"+name, im.Data); err != nil {
			return err
		}
	}

	// 章节
	for i, ch := range b.Chapters {
		fn := fmt.Sprintf("chapter_%04d.xhtml", i+1)
		body := ch.HTML
		if b.FixedLayout {
			body = fixedLayoutBody(ch.HTML)
		}
		doc := xhtmlDoc(ch.Title, body)
		if err := writeFile(zw, "OEBPS/text/"+fn, doc); err != nil {
			return err
		}
	}

	if err := writeFile(zw, "OEBPS/style.css", styleCSS(b.FixedLayout)); err != nil {
		return err
	}
	if err := writeFile(zw, "OEBPS/content.opf", contentOPF(b, imgExts)); err != nil {
		return err
	}
	if err := writeFile(zw, "OEBPS/toc.ncx", tocNCX(b)); err != nil {
		return err
	}

	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// 原子替换：前端随时可能来取，不能让它读到半个 zip
	return os.Rename(tmp, dst)
}

func writeFile(zw *zip.Writer, name, content string) error {
	return writeBytes(zw, name, []byte(content))
}

func writeBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

func xhtmlDoc(title, body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="zh">
<head><meta charset="utf-8"/><title>` + html.EscapeString(title) +
		`</title><link rel="stylesheet" type="text/css" href="../style.css"/></head>
<body>` + body + `</body></html>`
}

// fixedLayoutBody 漫画页：一个全屏 img，不参与重排。
func fixedLayoutBody(inner string) string {
	return `<div class="page">` + inner + `</div>`
}

func styleCSS(fixed bool) string {
	if fixed {
		return `html,body{margin:0;padding:0;background:#111;}
.page{display:flex;align-items:center;justify-content:center;width:100%;height:100%;}
.page img{max-width:100%;max-height:100vh;display:block;}
`
	}
	return `body{font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;line-height:1.75;padding:0 .2em;color:#2b2b2b;}
h1,h2,h3{line-height:1.35;margin:1.1em 0 .6em;}
p{margin:.65em 0;text-indent:2em;}
img{max-width:100%;height:auto;}
blockquote{margin:.8em 0;padding:.2em .8em;border-left:3px solid #cfd8e3;color:#555;}
`
}

func contentOPF(b *Book, imgExts map[string]string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>` + html.EscapeString(b.Title) + `</dc:title>
    <dc:language>zh</dc:language>
    <dc:identifier id="bookid">urn:uuid:littlescholar-` + fmt.Sprintf("%d", len(b.Chapters)) + `</dc:identifier>`)
	if b.Author != "" {
		sb.WriteString("\n    <dc:creator>" + html.EscapeString(b.Author) + "</dc:creator>")
	}
	if b.Cover != "" {
		sb.WriteString("\n    <meta name=\"cover\" content=\"cover-image\"/>")
	}
	sb.WriteString(`
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="css" href="style.css" media-type="text/css"/>`)

	// 图片清单（排序，保证同一本书每次生成的 OPF 稳定）
	names := make([]string, 0, len(imgExts))
	for n := range imgExts {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		id := "img_" + sanitizeID(n)
		props := ""
		if n == b.Cover {
			id = "cover-image"
			props = ""
		}
		sb.WriteString(fmt.Sprintf("\n    <item id=\"%s\" href=\"images/%s\" media-type=\"%s\"%s/>",
			id, n, imgExts[n], props))
	}

	for i := range b.Chapters {
		sb.WriteString(fmt.Sprintf("\n    <item id=\"ch%d\" href=\"text/chapter_%04d.xhtml\" media-type=\"application/xhtml+xml\"/>",
			i+1, i+1))
	}
	sb.WriteString(`
  </manifest>
  <spine toc="ncx">`)
	for i := range b.Chapters {
		sb.WriteString(fmt.Sprintf("\n    <itemref idref=\"ch%d\"/>", i+1))
	}
	sb.WriteString(`
  </spine>
</package>
`)
	return sb.String()
}

func tocNCX(b *Book) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head><meta name="dtb:uid" content="littlescholar"/></head>
  <docTitle><text>` + html.EscapeString(b.Title) + `</text></docTitle>
  <navMap>`)
	for i, ch := range b.Chapters {
		title := ch.Title
		if strings.TrimSpace(title) == "" {
			title = fmt.Sprintf("第 %d 章", i+1)
		}
		sb.WriteString(fmt.Sprintf(`
    <navPoint id="np%d" playOrder="%d">
      <navLabel><text>%s</text></navLabel>
      <content src="text/chapter_%04d.xhtml"/>
    </navPoint>`, i+1, i+1, html.EscapeString(title), i+1))
	}
	sb.WriteString(`
  </navMap>
</ncx>
`)
	return sb.String()
}

func imageMIME(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

func guessImageExt(data []byte) string {
	switch {
	case len(data) > 8 && bytes.HasPrefix(data, []byte("\x89PNG")):
		return ".png"
	case len(data) > 4 && bytes.HasPrefix(data, []byte("GIF8")):
		return ".gif"
	case len(data) > 12 && bytes.HasPrefix(data, []byte("RIFF")):
		return ".webp"
	case len(data) > 3 && bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return ".jpg"
	}
	return ".jpg"
}

var nonID = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeID(s string) string { return nonID.ReplaceAllString(s, "_") }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// CleanHTML 把一段可能很脏的 HTML 片段规整成 XHTML 良构的片段。
//
// 为什么必须做：EPUB 里的章节是 **XML**，一个没闭合的 <br> 或裸 & 就会让
// epub.js 整章解析失败（表现为"目录有、正文空白"）。Kindle 书里的 HTML
// 是 SGML 时代的产物，靠人肉修是不可能的，交给解析器重排。
//
// 同时把 <img recindex="00001"> / src="kindle:embed:0001" 这类
// MOBI 专有的图片引用，改写成 EPUB 里的实际路径。
func CleanHTML(src string, imgRef map[string]string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	doc, err := xhtml.Parse(strings.NewReader("<div id=\"root\">" + src + "</div>"))
	if err != nil {
		return html.EscapeString(stripTags(src))
	}
	root := findByID(doc, "root")
	if root == nil {
		return html.EscapeString(stripTags(src))
	}
	rewriteNodes(root, imgRef)

	var buf bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		_ = xhtml.Render(&buf, c)
	}
	return buf.String()
}

func findByID(n *xhtml.Node, id string) *xhtml.Node {
	if n.Type == xhtml.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findByID(c, id); r != nil {
			return r
		}
	}
	return nil
}

// rewriteNodes 修图片引用 + 去掉会在 EPUB 里炸的标签。
func rewriteNodes(n *xhtml.Node, imgRef map[string]string) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == xhtml.ElementNode {
			switch c.DataAtom {
			case atom.Script, atom.Style, atom.Iframe, atom.Object, atom.Embed:
				// 书籍正文里的脚本/内嵌对象没有任何阅读价值，
				// 留着还可能被 epub.js 的沙箱拦下来报错
				n.RemoveChild(c)
				c = next
				continue
			case atom.Img:
				rewriteImg(c, imgRef)
			}
			rewriteNodes(c, imgRef)
		}
		c = next
	}
}

func rewriteImg(n *xhtml.Node, imgRef map[string]string) {
	idx := ""
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "recindex":
			idx = a.Val
		case "src":
			if strings.HasPrefix(a.Val, "kindle:embed:") {
				idx = strings.TrimPrefix(a.Val, "kindle:embed:")
				if i := strings.IndexAny(idx, "?&"); i >= 0 {
					idx = idx[:i]
				}
			}
		}
	}
	if idx == "" {
		return
	}
	name, ok := imgRef[normalizeImgIndex(idx)]
	if !ok {
		return
	}
	// recindex 是 MOBI 私有属性，留在 XHTML 里虽然不致命，
	// 但它会暴露"这张图是从第几条记录来的"，而且严格校验器会报未定义属性。
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "recindex") {
			continue
		}
		kept = append(kept, a)
	}
	n.Attr = kept

	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, "src") {
			n.Attr[i].Val = "../images/" + name
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: "src", Val: "../images/" + name})
}

// normalizeImgIndex 把 "00001" / "1" / "0001" 统一成 "1"。
func normalizeImgIndex(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string {
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}
