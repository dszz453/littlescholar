package ebook

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrUnsupported 这本书的格式我们暂时解不开（会原样告诉用户）。
var ErrUnsupported = errors.New("暂不支持这个电子书格式")

// ErrDRM 带 DRM 的书解不开。
var ErrDRM = errors.New("这本书有版权保护（DRM），无法转换")

// ErrHUFF HUFF/CDIC 压缩暂不支持。
var ErrHUFF = errors.New("这本书用了 HUFF/CDIC 压缩，暂时读不了")

const (
	compressionNone = 1
	compressionPalm = 2
	compressionHUFF = 17480
)

// ParseMOBI 解析 MOBI / AZW / AZW3（KF8）为统一的书结构。
//
// 只实现 Kindle 格式里"能读懂内容"的最小集：
//   - PalmDB 容器解析（记录表）
//   - PalmDOC 头（压缩方式、文本长度、记录数）
//   - MOBI 头（文本编码、首个非文本记录、首个图片记录、书名）
//   - EXTH 元数据（作者、书名）
//   - PalmDOC LZ77 解压
//
// 有意没做：HUFF/CDIC、DRM、索引表、以及 KF8 的 FDST/skeleton 精确拼装。
// KF8 是"尽力而为"——按文本记录直接拼接，多数书能读出正文，
// 章节结构可能不如 MOBI6 精确，但比"打不开"好。
func ParseMOBI(data []byte) (*Book, error) {
	pdb, err := parsePDB(data)
	if err != nil {
		return nil, err
	}
	if len(pdb.records) < 2 {
		return nil, fmt.Errorf("MOBI 记录数不足")
	}

	r0 := pdb.records[0]
	if len(r0) < 16 {
		return nil, fmt.Errorf("MOBI 头部过短")
	}
	compression := binary.BigEndian.Uint16(r0[0:2])
	textLength := int(binary.BigEndian.Uint32(r0[4:8]))
	textRecords := int(binary.BigEndian.Uint16(r0[8:10]))
	encType := binary.BigEndian.Uint16(r0[12:14])
	if encType != 0 {
		return nil, ErrDRM
	}
	if compression == compressionHUFF {
		return nil, ErrHUFF
	}
	if compression != compressionNone && compression != compressionPalm {
		return nil, fmt.Errorf("%w（compression=%d）", ErrUnsupported, compression)
	}

	// ---- MOBI 头 ----
	var (
		textEncoding uint32 = 65001
		firstNonBook = 0
		firstImage   = 0xFFFFFFFF
		fullNameOff  = 0
		fullNameLen  = 0
		fileVersion  uint32
		exthStart    = 0
	)
	mobi := r0[16:]
	if len(mobi) >= 0x18 && string(mobi[0:4]) == "MOBI" {
		headerLen := int(binary.BigEndian.Uint32(mobi[4:8]))
		fileVersion = binary.BigEndian.Uint32(mobi[0x14:0x18])
		textEncoding = binary.BigEndian.Uint32(mobi[0x0C:0x10])
		if len(mobi) >= 0x48 {
			firstNonBook = int(binary.BigEndian.Uint32(mobi[0x44:0x48]))
		}
		if len(mobi) >= 0x50 {
			fullNameOff = int(binary.BigEndian.Uint32(mobi[0x48:0x4C]))
			fullNameLen = int(binary.BigEndian.Uint32(mobi[0x4C:0x50]))
		}
		if len(mobi) >= 0x64 {
			firstImage = int(binary.BigEndian.Uint32(mobi[0x60:0x64]))
		}
		// EXTH 紧跟 MOBI 头
		if headerLen > 0 && 16+headerLen+12 <= len(r0) {
			exthStart = 16 + headerLen
		}
	}

	if textRecords <= 0 || textRecords >= len(pdb.records) {
		// 有些文件 textRecords 写错，退回 firstNonBook 推算
		if firstNonBook > 1 && firstNonBook <= len(pdb.records) {
			textRecords = firstNonBook - 1
		} else {
			textRecords = len(pdb.records) - 1
		}
	}

	// ---- 文本 ----
	var out bytes.Buffer
	out.Grow(textLength + 1024)
	for i := 1; i <= textRecords && i < len(pdb.records); i++ {
		rec := pdb.records[i]
		var chunk []byte
		if compression == compressionNone {
			chunk = rec
		} else {
			chunk = palmDocDecompress(rec)
		}
		out.Write(chunk)
		if textLength > 0 && out.Len() >= textLength {
			break
		}
	}
	raw := out.Bytes()
	if textLength > 0 && len(raw) > textLength {
		raw = raw[:textLength]
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("这本书没有正文")
	}
	text := decodeText(raw, textEncoding)

	// ---- 图片 ----
	imgs, imgRef := extractMOBIImages(pdb, firstImage, firstNonBook)

	title := pdb.name
	if fullNameOff > 0 && fullNameLen > 0 && fullNameOff+fullNameLen <= len(r0) {
		if t := strings.TrimSpace(decodeText(r0[fullNameOff:fullNameOff+fullNameLen], textEncoding)); t != "" {
			title = t
		}
	}

	author := ""
	if exthStart > 0 && exthStart+12 <= len(r0) && string(r0[exthStart:exthStart+4]) == "EXTH" {
		author, title = parseEXTH(r0[exthStart:], title, textEncoding)
	}
	_ = fileVersion // KF8 也走"尽力拼接"这条路（见函数注释），不需要分支

	b := &Book{
		Title:  title,
		Author: author,
		Images: imgs,
	}
	b.Chapters = splitChapters(text, imgRef)
	if len(b.Chapters) == 0 {
		return nil, fmt.Errorf("这本书没有可读内容")
	}
	if len(b.Images) > 0 {
		b.Cover = b.Images[0].Name
	}
	return b, nil
}

// parsePDB 解析 PalmDB 容器，返回各记录的数据。
type pdbFile struct {
	name    string
	records [][]byte
}

func parsePDB(data []byte) (*pdbFile, error) {
	if len(data) < 78 {
		return nil, fmt.Errorf("文件太短，不是 MOBI")
	}
	n := int(binary.BigEndian.Uint16(data[76:78]))
	if n <= 0 || n > 65535 {
		return nil, fmt.Errorf("MOBI 记录数异常: %d", n)
	}
	tableLen := 78 + n*8
	if tableLen > len(data) {
		return nil, fmt.Errorf("MOBI 记录表越界")
	}
	offs := make([]int, n+1)
	for i := 0; i < n; i++ {
		p := 78 + i*8
		offs[i] = int(binary.BigEndian.Uint32(data[p : p+4]))
	}
	offs[n] = len(data)
	for i := 0; i < n; i++ {
		if offs[i] < tableLen || offs[i] > len(data) || offs[i] > offs[i+1] {
			return nil, fmt.Errorf("MOBI 记录偏移非法: rec=%d", i)
		}
	}
	recs := make([][]byte, n)
	for i := 0; i < n; i++ {
		recs[i] = data[offs[i]:offs[i+1]]
	}
	name := strings.TrimRight(string(data[0:32]), "\x00 ")
	return &pdbFile{name: name, records: recs}, nil
}

// palmDocDecompress 解 PalmDOC（LZ77 变体）压缩的一整条记录。
//
// 算法本身很短，但边界条件必须严格：源数据里任何一次回引都可能
// 越过已解出的长度，那说明文件坏了，硬拷贝会把后面的内容啃掉一位，
// 表现是"整章文字都是乱的"。
func palmDocDecompress(src []byte) []byte {
	out := make([]byte, 0, len(src)*4)
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == 0x00:
			out = append(out, 0x00)
			i++
		case c <= 0x08:
			// 后面 c 个字节原样拷贝
			k := int(c)
			i++
			if i+k > len(src) {
				k = len(src) - i
			}
			out = append(out, src[i:i+k]...)
			i += k
		case c <= 0x7F:
			out = append(out, c)
			i++
		case c <= 0xBF:
			if i+1 >= len(src) {
				return out
			}
			pair := int(c)<<8 | int(src[i+1])
			dist := (pair >> 3) & 0x07FF
			length := (pair & 0x07) + 3
			i += 2
			if dist == 0 || dist > len(out) {
				// 坏数据：跳过而不是 panic
				continue
			}
			start := len(out) - dist
			for k := 0; k < length; k++ {
				out = append(out, out[start+k])
			}
		default:
			// 0xC0~0xFF：空格 + (c ^ 0x80)
			out = append(out, ' ', c^0x80)
			i++
		}
	}
	return out
}

func decodeText(b []byte, encoding uint32) string {
	s := string(b)
	// Kindle 的中文书普遍标 65001（UTF-8），但 Windows-1252 的标法也常见。
	// 这里只在明显不是 UTF-8 时做一次兜底清洗 —— 真正的转码需要 charset 表，
	// 而 MOBI 里的中文几乎都是 UTF-8，收益不抵复杂度。
	if encoding == 1252 || strings.ContainsRune(s, '\uFFFD') {
		s = strings.ToValidUTF8(s, "")
	}
	return s
}

// parseEXTH 从 EXTH 段里取作者（100）与书名（503）。
func parseEXTH(rec []byte, title string, encoding uint32) (author, newTitle string) {
	if len(rec) < 12 {
		return "", title
	}
	count := int(binary.BigEndian.Uint32(rec[8:12]))
	p := 12
	for i := 0; i < count && p+8 <= len(rec); i++ {
		typ := int(binary.BigEndian.Uint32(rec[p : p+4]))
		l := int(binary.BigEndian.Uint32(rec[p+4 : p+8]))
		if l < 8 || p+l > len(rec) {
			break
		}
		val := rec[p+8 : p+l]
		switch typ {
		case 100:
			if author == "" {
				author = strings.TrimSpace(decodeText(val, encoding))
			}
		case 503:
			if t := strings.TrimSpace(decodeText(val, encoding)); t != "" {
				newTitle = t
			}
		}
		p += l
	}
	return author, newTitle
}

// extractMOBIImages 从 firstImageIndex 起逐条记录识别图片。
//
// MOBI 的图片没有索引表，就是一条 record 一张图、靠 magic 判断类型。
// 返回的 map 是 "HTML 里的 recindex 数字" → "EPUB 内的文件名"。
func extractMOBIImages(pdb *pdbFile, firstImage, firstNonBook int) ([]Image, map[string]string) {
	var (
		imgs   []Image
		imgRef = map[string]string{}
	)
	if firstImage <= 0 || firstImage >= len(pdb.records) {
		return imgs, imgRef
	}
	// 上限就是记录总数：firstNonBook 之后是索引表，靠 magic 会被自然过滤掉。
	// 反过来把 firstNonBook 当硬上界是危险的 —— 不少文件的这个字段是错的，
	// 会把整段图片区切掉。
	limit := len(pdb.records)
	if limit-firstImage > 4096 {
		limit = firstImage + 4096
	}
	n := 0
	for r := firstImage; r < limit; r++ {
		data := pdb.records[r]
		if !looksLikeImage(data) {
			continue
		}
		n++
		ext := guessImageExt(data)
		idx := r - firstImage + 1
		name := fmt.Sprintf("img_%05d%s", n, ext)
		imgs = append(imgs, Image{Name: name, Data: data})
		imgRef[strconv.Itoa(idx)] = name
	}
	return imgs, imgRef
}

func looksLikeImage(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	return bytes.HasPrefix(b, []byte("\xff\xd8\xff")) || // jpeg
		bytes.HasPrefix(b, []byte("\x89PNG")) || // png
		bytes.HasPrefix(b, []byte("GIF8")) || // gif
		(bytes.HasPrefix(b, []byte("RIFF")) && len(b) > 12 && string(b[8:12]) == "WEBP")
}

var (
	pageBreakRe = regexp.MustCompile(`(?i)<mbp:pagebreak[^>]*>`)
	h1Re        = regexp.MustCompile(`(?i)<h1[^>]*>`)
	bodyRe      = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
)

// splitChapters 把整本书的 HTML 切成章节。
//
// Kindle 书用 <mbp:pagebreak> 标记分页，这是最可靠的切点；
// 没有的话退到 <h1>。两者都没有就整本一章 —— 能读总比不能读好，
// 只是目录里只有一项。
func splitChapters(text string, imgRef map[string]string) []Chapter {
	if m := bodyRe.FindStringSubmatch(text); len(m) == 2 {
		text = m[1]
	}

	var parts []string
	if locs := pageBreakRe.FindAllStringIndex(text, -1); len(locs) > 1 {
		prev := 0
		for _, l := range locs {
			parts = append(parts, text[prev:l[0]])
			prev = l[1]
		}
		parts = append(parts, text[prev:])
	} else if locs := h1Re.FindAllStringIndex(text, -1); len(locs) > 1 {
		prev := 0
		for _, l := range locs {
			parts = append(parts, text[prev:l[0]])
			prev = l[0]
		}
		parts = append(parts, text[prev:])
	} else {
		parts = []string{text}
	}

	var chapters []Chapter
	for _, p := range parts {
		html := CleanHTML(p, imgRef)
		if strings.TrimSpace(stripTags(html)) == "" && !strings.Contains(html, "<img") {
			continue
		}
		chapters = append(chapters, Chapter{
			Title: chapterTitle(p),
			HTML:  html,
		})
	}
	return chapters
}

var titleTagRe = regexp.MustCompile(`(?is)<h[1-3][^>]*>(.*?)</h[1-3]>`)

func chapterTitle(frag string) string {
	if m := titleTagRe.FindStringSubmatch(frag); len(m) == 2 {
		t := strings.TrimSpace(stripTags(m[1]))
		if len([]rune(t)) > 60 {
			t = string([]rune(t)[:60])
		}
		return t
	}
	return ""
}
