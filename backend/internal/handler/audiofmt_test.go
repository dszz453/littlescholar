package handler

import (
	"testing"

	"github.com/littlescholar/core/internal/model"
)

// 音频转码的判定：只看扩展名，不看 media_type。
//
// 这条判据的由来（2026-10-20 线上）：用户报「大猫目录下 wma 格式的音频无法播放」。
// 根因是早年导入的资源 media_type 一律写成 video（那是后补列，存量行没回填），
// 而这里原来写的是 `res.MediaType != audio → return false`：
// 于是一个 wma 被判成"不需要转换"，直链直接进了 <video>，
// Android 的 WebView 解不了 wma —— 孩子看到的就是"点开没反应/放不出来"。
//
// 扩展名才是格式的权威来源：AudioNeedsTranscodeExt 里全是音频专用格式，
// 不存在"把视频误判成音频"的风险。
func TestNeedsConvertUsesExtNotMediaType(t *testing.T) {
	h := &AudioFormatHandler{}
	cases := []struct {
		name string
		res  model.Resource
		want bool
	}{
		{
			"存量行 media_type 还是 video 的 wma",
			model.Resource{MediaType: model.MediaTypeVideo, Ext: "wma"},
			true,
		},
		{
			"media_type 空的 wma",
			model.Resource{Ext: "wma"},
			true,
		},
		{
			"只有标题带后缀（Ext 空）",
			model.Resource{MediaType: model.MediaTypeVideo, Title: "The Red Coat.wma"},
			true,
		},
		{
			"本来就是音频的 wma",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "wma"},
			true,
		},
		{
			"ape / amr 同理",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "ape"},
			true,
		},
		{
			"mp3 浏览器能放，不转",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "mp3"},
			false,
		},
		{
			"电子书走自己的转换链，别抢",
			model.Resource{MediaType: model.MediaTypeEbook, Ext: "mobi"},
			false,
		},
		{
			"图片走自己的转换链",
			model.Resource{MediaType: model.MediaTypeImage, Ext: "wma"},
			false,
		},
		{
			"格式确实未知：判不了就别乱转",
			model.Resource{MediaType: model.MediaTypeVideo},
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := c.res
			if got := h.NeedsConvert(&res); got != c.want {
				t.Fatalf("NeedsConvert(%+v) = %v, want %v", c.res, got, c.want)
			}
		})
	}
}

func TestNeedsConvertNilSafe(t *testing.T) {
	var h *AudioFormatHandler
	if h.NeedsConvert(&model.Resource{Ext: "wma"}) {
		t.Fatal("nil handler 不该判需要转换")
	}
	if (&AudioFormatHandler{}).NeedsConvert(nil) {
		t.Fatal("nil resource 不该判需要转换")
	}
}
