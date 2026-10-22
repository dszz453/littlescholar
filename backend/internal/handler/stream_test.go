package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
)

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

// ============ 多连接分块：必须"边下边发" ============
//
// 这条测试盯的是一个**上过线的真实故障**（2026-10-22）：
// serveMultiConn 原本把每个 4MiB 分块 io.ReadAll 读完才按序写出，
// 于是首块 256KiB 之后，客户端要干等一整块（单连接限速下 7~13 秒）。
// 真机表现是"打开视频慢 + 播放不流畅"，而服务端日志一切正常。
//
// 断言方式：让上游每 64KiB 慢 10ms，然后测量**客户端两次收到数据之间的
// 最大间隔**。旧实现这里会出现约 640ms 的空窗（一整块 4MiB），
// 新实现应该始终有数据在流。
func TestMultiConnStreamsWithoutStall(t *testing.T) {
	const (
		size       = 6 << 20
		block      = 64 << 10
		blockDelay = 10 * time.Millisecond
	)

	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		if end >= size || end < 0 {
			end = size - 1
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(http.StatusPartialContent)
		for p := start; p <= end; p += block {
			e := p + block - 1
			if e > end {
				e = end
			}
			if _, err := w.Write(data[p : e+1]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(blockDelay)
		}
	}))
	defer upstream.Close()

	h := &StreamHandler{client: upstream.Client()}
	link := &adapter.DirectLink{URL: upstream.URL, Size: size, AcceptRanges: true}
	// Ext 非空 → learnContainerType 直接返回，不需要数据库
	res := &model.Resource{Base: model.Base{ID: 1}, Ext: "mp4"}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/s", func(c *gin.Context) {
		if !h.serveMultiConn(c, res, link, 4) {
			c.String(http.StatusInternalServerError, "没接管")
		}
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/s")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200", resp.StatusCode)
	}
	if got := resp.ContentLength; got != size {
		t.Fatalf("Content-Length = %d，期望 %d", got, size)
	}

	got := make([]byte, 0, size)
	buf := make([]byte, block)
	last := time.Now()
	var maxGap time.Duration
	reads := 0
	for {
		n, err := io.ReadFull(resp.Body, buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			now := time.Now()
			// 第一次读包含响应头 + 首块，不计入间隔
			if reads > 0 {
				if gap := now.Sub(last); gap > maxGap {
					maxGap = gap
				}
			}
			last = now
			reads++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatalf("读取中断: %v（已收 %d 字节）", err, len(got))
		}
	}

	if len(got) != size {
		t.Fatalf("收到 %d 字节，期望 %d", len(got), size)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("内容与源站不一致")
	}
	// 一整块 4MiB 要 64 个 64KiB 块 × 10ms ≈ 640ms；这里卡到 250ms 就说明
	// 又退回了"憋整块"
	t.Logf("最大空窗 %.0fms，共 %d 次读", float64(maxGap)/float64(time.Millisecond), reads)
	if maxGap > 250*time.Millisecond {
		t.Fatalf("数据流出现 %.0fms 的空窗（>250ms）—— 说明又在整块缓冲", float64(maxGap)/float64(time.Millisecond))
	}
}

// 上游中途断流时要能续：pumpChunk 应从已发出的偏移重新请求，
// 而不是把整块丢掉让播放器重开一次。
func TestPumpChunkResumesAfterBodyBreak(t *testing.T) {
	const size = 1 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}

	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(http.StatusPartialContent)

		// 第一次请求只发 3 个 64KiB 就把连接掐掉
		if atomic.AddInt32(&calls, 1) == 1 {
			limit := start + 3*streamBlock
			if limit > end {
				limit = end
			}
			w.Write(data[start : limit+1])
			return
		}
		w.Write(data[start : end+1])
	}))
	defer upstream.Close()

	h := &StreamHandler{client: upstream.Client()}
	ck := adapter.Chunk{Index: 0, Start: 0, End: size - 1}
	out := make(chan []byte, 64)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := h.pumpChunk(context.Background(), &adapter.DirectLink{URL: upstream.URL}, ck, out); err != nil {
			t.Errorf("pumpChunk 失败: %v", err)
		}
	}()

	var got []byte
	for piece := range out {
		got = append(got, piece...)
	}
	wg.Wait()

	if len(got) != size {
		t.Fatalf("续传后收到 %d 字节，期望 %d", len(got), size)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("续传内容错位")
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("没有发起续传请求（calls=%d）", calls)
	}
}
