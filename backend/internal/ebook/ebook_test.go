package ebook

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这组用例的原则：**用真文件**。
//
// 之前离线缓存那次踩过"假数据让功能看起来是坏的"（喂 Buffer.alloc 当视频），
// 所以这里不构造"看起来像 MOBI 的字节"，而是按 PalmDB + MOBI 规范
// 逐字段拼出真能解析的文件，再用真 PNG / 真 zip 做输入。

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 60), G: 120, B: uint8(y * 60), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// buildMOBI 拼一个最小但**结构合法**的 MOBI 6 文件。
//
// 布局：record 0 = PalmDOC 头 + MOBI 头 + 书名；
// record 1 = 正文（不压缩）；record 2.. = 图片。
func buildMOBI(title, text string, images [][]byte) []byte {
	const mobiHeaderLen = 232

	nameOff := 16 + mobiHeaderLen
	r0 := make([]byte, nameOff+len(title))
	binary.BigEndian.PutUint16(r0[0:2], compressionNone)
	binary.BigEndian.PutUint32(r0[4:8], uint32(len(text)))
	binary.BigEndian.PutUint16(r0[8:10], 1) // textRecords
	binary.BigEndian.PutUint16(r0[10:12], 4096)
	binary.BigEndian.PutUint16(r0[12:14], 0) // 无 DRM

	copy(r0[16:20], "MOBI")
	binary.BigEndian.PutUint32(r0[20:24], mobiHeaderLen)
	binary.BigEndian.PutUint32(r0[24:28], 2)     // mobi type: book
	binary.BigEndian.PutUint32(r0[28:32], 65001) // UTF-8
	binary.BigEndian.PutUint32(r0[16+0x14:16+0x18], 6) // file version
	binary.BigEndian.PutUint32(r0[16+0x44:16+0x48], 2) // first non-book
	binary.BigEndian.PutUint32(r0[16+0x48:16+0x4C], uint32(nameOff))
	binary.BigEndian.PutUint32(r0[16+0x4C:16+0x50], uint32(len(title)))
	binary.BigEndian.PutUint32(r0[16+0x60:16+0x64], 2) // first image
	copy(r0[nameOff:], title)

	records := [][]byte{r0, []byte(text)}
	records = append(records, images...)

	n := len(records)
	header := make([]byte, 78)
	copy(header[0:32], "test-book")
	copy(header[60:64], "BOOK")
	copy(header[64:68], "MOBI")
	binary.BigEndian.PutUint16(header[76:78], uint16(n))

	tableLen := 78 + n*8
	offsets := make([]int, n)
	pos := tableLen
	for i, r := range records {
		offsets[i] = pos
		pos += len(r)
	}
	out := make([]byte, 0, pos+16)
	out = append(out, header...)
	for i := 0; i < n; i++ {
		var b [8]byte
		binary.BigEndian.PutUint32(b[0:4], uint32(offsets[i]))
		out = append(out, b[:]...)
	}
	for i, r := range records {
		for len(out) < offsets[i] {
			out = append(out, 0)
		}
		out = append(out, r...)
	}
	return out
}

func TestParseMOBIRealStructure(t *testing.T) {
	pngData := tinyPNG(t)
	text := `<html><body><h1>第一章</h1><p>这是正文。</p>` +
		`<p><img recindex="00001"/></p><h1>第二章</h1><p>第二段。</p></body></html>`

	data := buildMOBI("测试绘本", text, [][]byte{pngData})
	dir := t.TempDir()
	src := filepath.Join(dir, "book.mobi")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := ParseMOBI(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if b.Title != "测试绘本" {
		t.Errorf("书名 = %q，期望 %q", b.Title, "测试绘本")
	}
	if len(b.Images) != 1 {
		t.Fatalf("图片数 = %d，期望 1", len(b.Images))
	}
	if len(b.Chapters) < 2 {
		t.Fatalf("章节数 = %d，期望按 <h1> 切出至少 2 章", len(b.Chapters))
	}
	// 图片引用必须被改写成 EPUB 内路径，否则正文里是空图
	all := ""
	for _, c := range b.Chapters {
		all += c.HTML
	}
	if !strings.Contains(all, "../images/") {
		t.Errorf("图片引用没有被改写: %s", all)
	}
	if strings.Contains(all, "recindex") {
		t.Errorf("recindex 应该已被消费掉")
	}

	// 走完整转换链路，产出必须是**能打开的** zip（mimetype 第一且不压缩）
	dst := filepath.Join(dir, "out.epub")
	if err := Convert(src, "mobi", dst); err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	assertValidEPUB(t, dst)
}

// assertValidEPUB 按 EPUB 的硬性要求校验产物。
func assertValidEPUB(t *testing.T, path string) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("产物不是合法 zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		t.Fatal("空 EPUB")
	}
	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Errorf("第一个条目是 %q，规范要求 mimetype", first.Name)
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype 必须不压缩")
	}
	rc, _ := first.Open()
	buf := new(bytes.Buffer)
	buf.ReadFrom(rc)
	rc.Close()
	if buf.String() != "application/epub+zip" {
		t.Errorf("mimetype 内容 = %q", buf.String())
	}

	want := map[string]bool{
		"META-INF/container.xml": false,
		"OEBPS/content.opf":      false,
		"OEBPS/toc.ncx":          false,
	}
	chapters := 0
	for _, f := range zr.File {
		if _, ok := want[f.Name]; ok {
			want[f.Name] = true
		}
		if strings.HasPrefix(f.Name, "OEBPS/text/chapter_") {
			chapters++
			rc, _ := f.Open()
			body := new(bytes.Buffer)
			body.ReadFrom(rc)
			rc.Close()
			if !strings.Contains(body.String(), "<html") {
				t.Errorf("%s 不是完整 xhtml", f.Name)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("缺少 %s", name)
		}
	}
	if chapters == 0 {
		t.Error("没有任何章节")
	}
}

func TestPalmDocDecompress(t *testing.T) {
	// 0xC0..0xFF = 空格 + (c^0x80)；0x01 0x41 = 拷贝 1 字节
	src := []byte{0xC1, 0x01, 'A', 0x02, 'B', 'C'}
	// 0xC1 → " A"；0x01 → 拷贝 1 字节 'A'；0x02 → 拷贝 2 字节 "BC"
	got := string(palmDocDecompress(src))
	if got != " AABC" {
		t.Fatalf("解压结果 = %q，期望 %q", got, " AABC")
	}

	// 回引：输出 "ab" 后引用距离 2、长度 3 → "ababa"
	// pair = (dist<<3)|(len-3) = (2<<3)|0 = 0x10 → 0x80,0x10？高位在 c<=0xBF 分支
	// 用 (0x80|0x00)=0x80 表示 dist=0x10>>3=2, len=3 → 两次 "ab" 后得到 "ab"+"aba"
	back := []byte{'a', 'b', 0x80, 0x10}
	if got := string(palmDocDecompress(back)); got != "ab"+"aba" {
		t.Fatalf("回引解压 = %q", got)
	}

	// 坏数据不能 panic
	_ = palmDocDecompress([]byte{0xBF})
}

func TestParseComicNaturalOrder(t *testing.T) {
	dir := t.TempDir()
	cbz := filepath.Join(dir, "comic.cbz")
	f, err := os.Create(cbz)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	pngData := tinyPNG(t)
	// 故意乱序写入，顺序还要靠自然序纠正
	for _, name := range []string{"page10.png", "page2.png", "page1.png"} {
		w, _ := zw.Create(name)
		w.Write(pngData)
	}
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("not an image"))
	zw.Close()
	f.Close()

	b, err := ParseComic(cbz, "cbz")
	if err != nil {
		t.Fatalf("解析漫画失败: %v", err)
	}
	if len(b.Chapters) != 3 {
		t.Fatalf("页数 = %d，期望 3", len(b.Chapters))
	}
	if !b.FixedLayout {
		t.Error("漫画必须是固定版式")
	}
	// 第一页必须是 page1（自然序），而不是存储顺序里的 page10
	first := b.Chapters[0].HTML
	if !strings.Contains(first, b.Images[0].Name) {
		t.Errorf("第一章图片引用不匹配")
	}

	dst := filepath.Join(dir, "comic.epub")
	if err := Convert(cbz, "cbz", dst); err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	assertValidEPUB(t, dst)
}

func TestNaturalLess(t *testing.T) {
	cases := []struct{ a, b string; want bool }{
		{"page2.png", "page10.png", true},
		{"page10.png", "page2.png", false},
		{"1.jpg", "1.jpg", false},
		{"Chapter 1", "chapter 2", true},
		{"009.png", "10.png", true},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCleanHTMLFixesDirtyMarkup(t *testing.T) {
	// 未闭合 <br>、裸 & —— 直接塞进 XHTML 会让 EPUB 整章打不开
	out := CleanHTML(`<p>a<br>b & c<img recindex="00002">`, map[string]string{"2": "img_00002.jpg"})
	if !strings.Contains(out, "img_00002.jpg") {
		t.Errorf("图片没被改写: %s", out)
	}
	if strings.Contains(out, "recindex") {
		t.Errorf("recindex 残留: %s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("裸 & 没被转义: %s", out)
	}
	if strings.Contains(out, "<script") {
		t.Errorf("脚本没被清掉")
	}
}

func TestParseFB2(t *testing.T) {
	pngData := tinyPNG(t)
	fb2 := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description><title-info><book-title>小书童童话</book-title>
<author><first-name>张</first-name><last-name>三</last-name></author></title-info></description>
<body>
<section><title><p>第一章</p></title><p>从前有座山。</p></section>
<section><title><p>第二章</p></title><p>山里有座庙。</p>
<image l:href="#cover.png"/></section>
</body>
<binary id="cover.png" content-type="image/png">` + base64Of(pngData) + `</binary>
</FictionBook>`
	dir := t.TempDir()
	src := filepath.Join(dir, "a.fb2")
	os.WriteFile(src, []byte(fb2), 0o644)

	b, err := ParseFB2(src)
	if err != nil {
		t.Fatalf("解析 FB2 失败: %v", err)
	}
	if b.Title != "小书童童话" {
		t.Errorf("书名 = %q", b.Title)
	}
	if len(b.Chapters) != 2 {
		t.Errorf("章节数 = %d，期望 2", len(b.Chapters))
	}
	if len(b.Images) != 1 {
		t.Errorf("图片数 = %d，期望 1", len(b.Images))
	}
	dst := filepath.Join(dir, "a.epub")
	if err := Convert(src, "fb2", dst); err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	assertValidEPUB(t, dst)
}

func base64Of(b []byte) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		sb.WriteByte(tbl[(n>>18)&0x3F])
		sb.WriteByte(tbl[(n>>12)&0x3F])
		if rem > 1 {
			sb.WriteByte(tbl[(n>>6)&0x3F])
		} else {
			sb.WriteByte('=')
		}
		if rem > 2 {
			sb.WriteByte(tbl[n&0x3F])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}
