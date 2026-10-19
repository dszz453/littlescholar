package ebook

import (
	"fmt"
	"os"
	"strings"
)

// Convert 把一本需要转换的电子书转成 EPUB，写到 dst。
//
// 只处理 ebookNeedsConvertExts 里的格式；epub/txt/cbz/pdf 由前端直接读，
// 不该走到这里（走了也只会白转一次）。
func Convert(src, ext, dst string) error {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	// CBZ 本身浏览器也读不了（zip），但转换成本极低，
	// 统一转成 EPUB 反而让前端只有一条渲染路径。
	if !isConvertible(ext) {
		return fmt.Errorf("%w：%s", ErrUnsupported, ext)
	}

	if st, err := os.Stat(src); err != nil {
		return err
	} else if st.Size() > maxBookBytes {
		return fmt.Errorf("文件太大（%d MB），请在本地转成 EPUB 后再导入", st.Size()>>20)
	}

	var (
		b   *Book
		err error
	)
	switch ext {
	case "mobi", "azw", "azw3":
		var data []byte
		data, err = os.ReadFile(src)
		if err != nil {
			return err
		}
		b, err = ParseMOBI(data)
	case "cbz", "cbr":
		b, err = ParseComic(src, ext)
	case "fb2":
		b, err = ParseFB2(src)
	default:
		return fmt.Errorf("%w：%s", ErrUnsupported, ext)
	}
	if err != nil {
		return err
	}
	if b.Title == "" {
		b.Title = strings.TrimSuffix(baseName(src), "."+ext)
	}
	return BuildEPUB(b, dst)
}

// isConvertible 这个扩展名是不是"必须服务端转换"的那一类。
func isConvertible(ext string) bool {
	switch ext {
	case "mobi", "azw", "azw3", "cbr", "cb7", "fb2", "cbz":
		return true
	}
	return false
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
