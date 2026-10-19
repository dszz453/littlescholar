package ebook

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// maxBookBytes 单本书读进内存的上限。
//
// 漫画包几百 MB 很常见，但服务端只有 ~1.9GB 可用内存（飞牛上还跑着别的容器），
// 全读进来会把它拖垮。超过这个尺寸就让用户先在本地解包。
const maxBookBytes = 256 << 20

// ParseComic 把漫画包（CBZ = zip / CBR = rar）转成"一页一张图"的书。
//
// 图片按**文件名自然序**排列，不是压缩包里的存储顺序 ——
// 很多打包工具是乱序写入的，按存储顺序会得到"第10页在第2页前面"。
func ParseComic(file string, ext string) (*Book, error) {
	entries, err := readComicEntries(file, ext)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("这个漫画包里没有图片")
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return naturalLess(entries[i].name, entries[j].name)
	})

	b := &Book{
		Title:       strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)),
		FixedLayout: true,
	}
	for i, e := range entries {
		if !looksLikeImage(e.data) {
			continue
		}
		name := fmt.Sprintf("page_%05d%s", i+1, guessImageExt(e.data))
		b.Images = append(b.Images, Image{Name: name, Data: e.data})
		b.Chapters = append(b.Chapters, Chapter{
			Title: fmt.Sprintf("第 %d 页", len(b.Chapters)+1),
			HTML:  `<img src="../images/` + name + `" alt=""/>`,
		})
	}
	if len(b.Chapters) == 0 {
		return nil, fmt.Errorf("这个漫画包里没有浏览器能显示的图片")
	}
	b.Cover = b.Images[0].Name
	return b, nil
}

type comicEntry struct {
	name string
	data []byte
}

func readComicEntries(file, ext string) ([]comicEntry, error) {
	switch strings.ToLower(ext) {
	case "cbz":
		return readZipEntries(file)
	case "cbr":
		return readRarEntries(file)
	default:
		return nil, fmt.Errorf("%w：%s", ErrUnsupported, ext)
	}
}

func readZipEntries(file string) ([]comicEntry, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("打不开 zip: %w", err)
	}
	defer zr.Close()
	var out []comicEntry
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isImageName(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxBookBytes))
		rc.Close()
		if err != nil {
			continue
		}
		out = append(out, comicEntry{name: path.Base(f.Name), data: data})
	}
	return out, nil
}

func readRarEntries(file string) ([]comicEntry, error) {
	rr, err := rardecode.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("打不开 rar: %w", err)
	}
	defer rr.Close()
	var out []comicEntry
	for {
		hdr, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, nil // 已经读出来的部分照样能用
		}
		if hdr.IsDir || !isImageName(hdr.Name) {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, io.LimitReader(rr, maxBookBytes)); err != nil {
			continue
		}
		out = append(out, comicEntry{name: path.Base(hdr.Name), data: buf.Bytes()})
	}
	return out, nil
}

func isImageName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// naturalLess 自然序比较：page2 < page10（按字典序会反过来）。
func naturalLess(a, b string) bool {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		ca, cb := a[ia], b[ib]
		if isDigit(ca) && isDigit(cb) {
			// 取两边的完整数字段比较数值
			ja, jb := ia, ib
			for ja < len(a) && isDigit(a[ja]) {
				ja++
			}
			for jb < len(b) && isDigit(b[jb]) {
				jb++
			}
			na := strings.TrimLeft(a[ia:ja], "0")
			nb := strings.TrimLeft(b[ib:jb], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ia, ib = ja, jb
			continue
		}
		la, lb := lower(ca), lower(cb)
		if la != lb {
			return la < lb
		}
		ia++
		ib++
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// SaveToTemp 把输入流落到临时文件（rardecode / zip 都要随机访问）。
func SaveToTemp(r io.Reader, dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, maxBookBytes+1))
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if n > maxBookBytes {
		os.Remove(f.Name())
		return "", fmt.Errorf("文件太大（超过 %d MB），请在本地解包后再导入", maxBookBytes>>20)
	}
	return f.Name(), nil
}
