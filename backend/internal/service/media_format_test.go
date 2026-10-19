package service

import (
	"testing"

	"github.com/littlescholar/core/internal/model"
)

// 这组用例锁的是两个真实线上故障：
//
//  1. 夸克/UC 等网盘的直链必须带 Cookie，否则 CDN 回 412；
//     （在网络层，单元测试覆盖不到，这里只覆盖判定逻辑）
//  2. 资源入库时 Title 被剥掉了扩展名，于是"按扩展名决定是否转码"
//     这个判断永远不成立 —— NAS 上的 rmbv 动画点开只有一句
//     「播放地址可能已过期」。修复后判定必须从 ResourceExt 走。
func TestResourceExt(t *testing.T) {
	cases := []struct {
		name string
		res  *model.Resource
		want string
	}{
		{
			// 线上真实数据：WebDAV 导入的《神奇之钥》第13集
			name: "老数据：标题无后缀，FileID 是带后缀的 WebDAV 路径",
			res: &model.Resource{
				Title:  "【公益知识库zscc.club】神奇之钥第2季13集-Underwater World",
				FileID: "/kuake/牛津树最全资源 合集/牛津阅读树动画The Magic Key/神奇之钥第2季13集.rmvb",
			},
			want: "rmvb",
		},
		{
			name: "标题里的点不是扩展名，不能挡住 FileID 的真后缀",
			res: &model.Resource{
				Title:  "神奇之钥 第2季.13",
				FileID: "/kids/magic-key/ep13.rmvb",
			},
			want: "rmvb",
		},
		{
			name: "括号里的版本号同样是干扰项",
			res: &model.Resource{
				Title:  "Biff and Chip (1.2)",
				FileID: "/kids/biff/01.mkv",
			},
			want: "mkv",
		},
		{
			name: "新数据：Ext 显式写入，优先级最高",
			res: &model.Resource{
				Ext:    "MKV", // 大小写不敏感
				Title:  "小脏猪.wrongext",
				FileID: "3b1f0e2a9c8d7e6f5a4b3c2d1e0f9a8b",
			},
			want: "mkv",
		},
		{
			name: "标题自带后缀时直接用",
			res: &model.Resource{Title: "小猪佩奇 S1E01.mp4"},
			want: "mp4",
		},
		{
			name: "网盘短 ID 取不出后缀，返回空",
			res: &model.Resource{
				Title:  "牛津树L1-L9",
				FileID: "7e54a6adedbe40dba64276460ab606e3",
			},
			want: "",
		},
		{name: "nil 安全", res: nil, want: ""},
	}
	for _, c := range cases {
		if got := ResourceExt(c.res); got != c.want {
			t.Errorf("%s: ResourceExt() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNeedsTranscode(t *testing.T) {
	cases := []struct {
		name string
		res  *model.Resource
		want bool
	}{
		{
			name: "【回归】WebDAV 的 rmvb 必须转码",
			res: &model.Resource{
				MediaType: model.MediaTypeVideo,
				Title:     "神奇之钥第2季13集-Underwater World",
				FileID:    "/kids/magic-key/ep13.rmvb",
				TotalSize: 114299653, // 约 109 MB，不足以触发"大文件"分支
			},
			want: true,
		},
		{
			name: "mp4 且体积不大 → 直链即可",
			res: &model.Resource{
				MediaType: model.MediaTypeVideo,
				Ext:       "mp4",
				TotalSize: 50 << 20,
			},
			want: false,
		},
		{
			name: "m3u8 已经是流，不再转",
			res: &model.Resource{MediaType: model.MediaTypeVideo, Ext: "m3u8"},
			want: false,
		},
		{
			name: "mp4 但超过 300MB → 转（拖动体验差）",
			res: &model.Resource{MediaType: model.MediaTypeVideo, Ext: "mp4", TotalSize: 301 << 20},
			want: true,
		},
		{
			name: "mp4 但时长超过 10 分钟 → 转",
			res: &model.Resource{MediaType: model.MediaTypeVideo, Ext: "mp4", Duration: 601},
			want: true,
		},
		{
			name: "音频一律不转",
			res: &model.Resource{MediaType: model.MediaTypeAudio, Ext: "m4a"},
			want: false,
		},
		{
			name: "旧行为保留：webm 直链可播，不转",
			res: &model.Resource{MediaType: model.MediaTypeVideo, Ext: "webm"},
			want: false,
		},
		{name: "nil 安全", res: nil, want: false},
	}
	for _, c := range cases {
		if got := NeedsTranscode(c.res); got != c.want {
			t.Errorf("%s: NeedsTranscode() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestContainerUnsupported(t *testing.T) {
	cases := []struct {
		name string
		res  *model.Resource
		want bool
	}{
		{
			name: "rmvb 浏览器放不出来",
			res: &model.Resource{
				MediaType: model.MediaTypeVideo,
				FileID:    "/kids/a.rmvb",
			},
			want: true,
		},
		{name: "mp4 可以放", res: &model.Resource{MediaType: model.MediaTypeVideo, Ext: "mp4"}, want: false},
		// 2026-10 扩展：音频和图片也参与这个判定。
		// WMA/APE 在 WebView 里就是一句"播放失败"，HEIC 是一张空白图 ——
		// 它们和 rmvb 属于同一类问题，必须有同样的引导。
		{
			name: "ape 音频浏览器放不出来",
			res:  &model.Resource{MediaType: model.MediaTypeAudio, Ext: "ape"},
			want: true,
		},
		{
			name: "wma 音频浏览器放不出来",
			res:  &model.Resource{MediaType: model.MediaTypeAudio, Ext: "WMA"},
			want: true,
		},
		{name: "m4a 可以放", res: &model.Resource{MediaType: model.MediaTypeAudio, Ext: "m4a"}, want: false},
		{name: "flac 可以放", res: &model.Resource{MediaType: model.MediaTypeAudio, Ext: "flac"}, want: false},
		{
			name: "heic 图片浏览器解不了",
			res:  &model.Resource{MediaType: model.MediaTypeImage, Ext: "heic"},
			want: true,
		},
		{name: "jpg 可以显示", res: &model.Resource{MediaType: model.MediaTypeImage, Ext: "jpg"}, want: false},
		{name: "格式未知时不误报", res: &model.Resource{MediaType: model.MediaTypeVideo}, want: false},
		{name: "nil 安全", res: nil, want: false},
	}
	for _, c := range cases {
		if got := ContainerUnsupported(c.res); got != c.want {
			t.Errorf("%s: ContainerUnsupported() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFormatTablesConsistent 保证四张扩展名表与 MediaTypeByExt 不分叉。
//
// 这是"兼容大部分格式"这个需求最容易出错的地方：往 videoExts 里加一个
// 格式却忘了更新分类，表现是**导入成功、点开按错的播放器渲染**。
// 遍历整张表而不是挑几个样本来断言，漏一项就会红。
func TestFormatTablesConsistent(t *testing.T) {
	sets := []struct {
		name string
		tbl  map[string]bool
		want string
	}{
		{"video", videoExts, model.MediaTypeVideo},
		{"audio", audioExts, model.MediaTypeAudio},
		{"ebook", ebookExts, model.MediaTypeEbook},
		{"image", imageExts, model.MediaTypeImage},
	}
	for _, s := range sets {
		for ext := range s.tbl {
			if got := MediaTypeByExt(ext); got != s.want {
				t.Errorf("%s 表里的 %q 被分类成 %q，期望 %q", s.name, ext, got, s.want)
			}
			if !IsMediaFile("x." + ext) {
				t.Errorf("%s 表里的 %q 不在可导入白名单里", s.name, ext)
			}
		}
	}
	if MediaTypeByExt("pdf") != model.MediaTypePDF {
		t.Errorf("pdf 必须仍是独立类型")
	}
}

// TestMediaTypeByExt 锁住用户实际点到的那几个格式的归类。
func TestMediaTypeByExt(t *testing.T) {
	cases := map[string]string{
		"mp4": "video", "mkv": "video", "m2ts": "video", "rmvb": "video", "ogv": "video",
		"mp3": "audio", "wma": "audio", "ape": "audio", "mka": "audio", "opus": "audio",
		"epub": "ebook", "mobi": "ebook", "azw3": "ebook", "cbz": "ebook", "cbr": "ebook", "txt": "ebook",
		"jpg": "image", "heic": "image", "svg": "video", // svg 不在表里 → 历史行为按视频
		"pdf": "pdf",
	}
	for ext, want := range cases {
		if got := MediaTypeByExt(ext); got != want {
			t.Errorf("MediaTypeByExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

// TestNeedsTranscodeForType 三类资源的"直接播 / 要转码"判定。
func TestNeedsTranscodeForType(t *testing.T) {
	cases := []struct {
		mt, ext string
		want    bool
	}{
		{"video", "mp4", false},
		{"video", "mkv", true},
		{"video", "m2ts", true},
		{"audio", "mp3", false},
		{"audio", "flac", false},
		{"audio", "wma", true},
		{"audio", "ape", true},
		{"image", "jpg", false},
		{"image", "heic", true},
		{"ebook", "epub", false},
		{"ebook", "mobi", false}, // 电子书的问题不是解码，走 EbookNeedsConvertExt
	}
	for _, c := range cases {
		if got := NeedsTranscodeForType(c.mt, c.ext); got != c.want {
			t.Errorf("NeedsTranscodeForType(%q,%q) = %v, want %v", c.mt, c.ext, got, c.want)
		}
	}
}

// TestEbookNeedsConvert 哪些电子书要服务端转换。
func TestEbookNeedsConvert(t *testing.T) {
	need := []string{"mobi", "azw", "azw3", "cbr", "cb7", "fb2"}
	direct := []string{"epub", "txt", "cbz", "pdf"}
	for _, e := range need {
		if !EbookNeedsConvertExt(e) {
			t.Errorf("%s 应该需要服务端转换", e)
		}
	}
	for _, e := range direct {
		if EbookNeedsConvertExt(e) {
			t.Errorf("%s 前端能直接读，不该走转换", e)
		}
	}
}

// TestDiskImportSetsExt 确认导入链路真的把扩展名落库了。
//
// 这是那个 bug 的源头：Title 被 strip 之后，如果不额外存一份 Ext，
// 后面所有"按格式"的决策都失去了依据。
func TestDiskImportSetsExt(t *testing.T) {
	if got := ExtOfName("神奇之钥第2季13集.rmvb"); got != "rmvb" {
		t.Fatalf("ExtOfName 取扩展名失败: %q", got)
	}
	res := &model.Resource{
		Title:  "神奇之钥第2季13集",
		Ext:    ExtOfName("神奇之钥第2季13集.rmvb"),
		FileID: "3b1f0e2a9c8d7e6f5a4b3c2d1e0f9a8b",
	}
	if ResourceExt(res) != "rmvb" {
		t.Fatalf("导入后应能判定出 rmvb，得到 %q", ResourceExt(res))
	}
	if !NeedsTranscode(&model.Resource{
		MediaType: model.MediaTypeVideo, Title: res.Title, Ext: res.Ext, FileID: res.FileID,
	}) {
		t.Fatal("导入后的 rmvb 资源必须触发转码")
	}
}
