package handler

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/subtitle"
)

// 听写的"要不要起、起几份"这些判断，全在 StartASR 里。
//
// 它们错一个都不会报错，只会表现为"额度被重复烧光"或"字幕永远不出来"：
//   - 不去重：孩子切一次前后台就多发一次请求，几分钟内能起七八个转写；
//   - 不判媒体类型：给 PDF/图片也去抽音轨，必然失败还占住并发位；
//   - 不设冷却：静音资料孩子每点一次都重新烧一遍额度。
func newASRHandler(t *testing.T) *SubtitleHandler {
	t.Helper()
	h := &SubtitleHandler{outDir: t.TempDir()}
	h.WithASR(subtitle.NewTranscriber("http://asr.local/v1", "k", "", "en"))
	return h
}

func videoRes(id uint64) model.Resource {
	return model.Resource{Base: model.Base{ID: id}, MediaType: model.MediaTypeVideo}
}

func TestStartASRGating(t *testing.T) {
	t.Run("没配转写就不起", func(t *testing.T) {
		h := &SubtitleHandler{outDir: t.TempDir()}
		if h.StartASR(&model.Resource{MediaType: model.MediaTypeVideo}) {
			t.Fatal("没挂转写器却起了任务")
		}
	})

	t.Run("只有 base 没 key 也不起", func(t *testing.T) {
		h := &SubtitleHandler{outDir: t.TempDir()}
		h.WithASR(subtitle.NewTranscriber("http://asr.local/v1", "", "", "en"))
		if h.StartASR(&model.Resource{MediaType: model.MediaTypeVideo}) {
			t.Fatal("没有 key 却起了任务")
		}
	})

	t.Run("PDF 不去听写", func(t *testing.T) {
		h := newASRHandler(t)
		var called int32
		h.asrRunner = func(model.Resource) { atomic.AddInt32(&called, 1) }
		r := model.Resource{Base: model.Base{ID: 1}, MediaType: model.MediaTypePDF}
		if h.StartASR(&r) {
			t.Fatal("PDF 也去听写了")
		}
		time.Sleep(30 * time.Millisecond)
		if atomic.LoadInt32(&called) != 0 {
			t.Fatal("PDF 竟然真的进了转写")
		}
	})

	t.Run("视频会起，且只起一份", func(t *testing.T) {
		h := newASRHandler(t)
		var called int32
		release := make(chan struct{})
		done := make(chan struct{})
		h.asrRunner = func(model.Resource) {
			atomic.AddInt32(&called, 1)
			<-release
			close(done)
		}

		r := videoRes(7)
		if !h.StartASR(&r) {
			t.Fatal("视频应当起听写")
		}
		// 转写还在跑，再来三次（模拟切前后台）
		for i := 0; i < 3; i++ {
			if !h.StartASR(&r) {
				t.Fatal("转写进行中应当告诉前端\"等一下\"，而不是当成没有")
			}
		}
		time.Sleep(50 * time.Millisecond)
		if n := atomic.LoadInt32(&called); n != 1 {
			t.Fatalf("起了 %d 份转写，同一条资源必须只有一份", n)
		}
		close(release)
		<-done
		// 跑完之后锁要放开，下次还能起
		time.Sleep(30 * time.Millisecond)
		h2 := newASRHandler(t)
		if _, busy := h2.asrLocks.Load(uint64(7)); busy {
			t.Fatal("任务结束后锁没释放")
		}
	})
}

// 失败冷却：静音/纯音乐资料转不出字幕是常态，不能每点一次都重烧额度。
func TestStartASRSkipsAfterFailure(t *testing.T) {
	h := newASRHandler(t)
	var called int32
	h.asrRunner = func(model.Resource) { atomic.AddInt32(&called, 1) }

	r := videoRes(11)
	h.markASRFailed(r.ID, "音频里没有人声")

	if h.StartASR(&r) {
		t.Fatal("刚失败过又起了任务")
	}
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("失败过却还是进了转写")
	}
}

// 冷却只在 TTL 内有效，过了一天要允许重试（模型换好了、资料换了都可能）。
func TestASRFailMarkerExpires(t *testing.T) {
	h := newASRHandler(t)
	h.markASRFailed(12, "音频里没有人声")
	if !h.asrFailed(12) {
		t.Fatal("刚写的失败标记应当生效")
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(h.asrFailMarker(12), old, old); err != nil {
		t.Fatal(err)
	}
	if h.asrFailed(12) {
		t.Fatal("过了冷却期仍然被挡住 —— 修好了也永远不给试")
	}
}

// file:// 地址的三种形态：Linux、百分号编码的中文名、Windows 少冒号。
// 抽音轨和读字幕文件都走它，错一个就是"文件找不到"。
func TestFileURLToPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///vol1/xuexi/%E7%AC%AC01%E9%9B%86.mp4", "/vol1/xuexi/第01集.mp4"},
		{"file://C/Users/zhang/第01集.mp4", "C:/Users/zhang/第01集.mp4"},
		{"/C:/Users/zhang/a.mp4", "C:/Users/zhang/a.mp4"},
	}
	for _, c := range cases {
		raw := c.in
		if c.in == "/C:/Users/zhang/a.mp4" {
			raw = "file://" + c.in
		}
		if got := fileURLToPath(raw); got != c.want {
			t.Errorf("fileURLToPath(%q) = %q，期望 %q", raw, got, c.want)
		}
	}
}

// 体积没超限就不切片（绝大多数课文/绘本音频都走这条）。
func TestSplitIfNeededSmallFile(t *testing.T) {
	h := &SubtitleHandler{outDir: t.TempDir()}
	p := filepath.Join(h.outDir, "a.mp3")
	if err := os.WriteFile(p, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	parts, offsets, err := h.splitIfNeeded(p, h.outDir, "a")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if len(parts) != 1 || parts[0] != p {
		t.Fatalf("小文件不该被切：%#v", parts)
	}
	if offsets[0] != 0 {
		t.Fatalf("第一片偏移必须是 0：%v", offsets)
	}
}

var _ = sync.Mutex{}
