package handler

import "testing"

// contentTypeExt 是"老资源自愈"的关键一环：网盘资源的 FileID 是短 hash、
// Title 又被剥了后缀，唯一还能告诉我们格式的就是上游 CDN 的 Content-Type。
// 线上那批夸克动画返回的正是 application/vnd.rn-realmedia-vbr。
func TestContentTypeExt(t *testing.T) {
	cases := []struct{ ct, want string }{
		// RealMedia 家族 —— 这次故障的主角，绝不能退化成空
		{"application/vnd.rn-realmedia-vbr", "rmvb"},
		{"application/vnd.rn-realmedia", "rmvb"},
		{"application/vnd.rn-realmedia-real", "rm"},
		{"application/x-pn-realmedia", "rm"},
		// 带参数的 Content-Type 要能剥掉 charset 等后缀
		{"application/vnd.rn-realmedia-vbr; charset=utf-8", "rmvb"},
		{"text/html; charset=UTF-8", ""},
		{"video/mp4", "mp4"},
		{"Video/MP4 ", "mp4"},
		{"video/x-matroska", "mkv"},
		{"video/x-msvideo", "avi"},
		{"video/quicktime", "mov"},
		{"audio/mpeg", "mp3"},
		{"audio/mp4", "m4a"},
		{"application/pdf", "pdf"},
		// 这些是"我们认识但不确定"或"通用字节流"，都不能乱猜
		{"application/octet-stream", ""},
		{"application/json; charset=utf-8", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := contentTypeExt(c.ct); got != c.want {
			t.Errorf("contentTypeExt(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}
