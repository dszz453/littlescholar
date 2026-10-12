package task

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Accelerator 非会员下载加速器
//
// 原理：网盘对非会员的限制主要是「单连接限速」而非「总量限速」。
// 通过 HTTP Range 把文件切成 N 块，并发请求，
// 聚合带宽 = 单连接限速 × 并发数。
//
// 注意：无法突破账号级别的总量限速与付费墙，仅缓解单连接瓶颈。
type Accelerator struct {
	// Concurrency 并发数，建议 16~32
	Concurrency int
	// ChunkSize 分块大小，建议 2MB
	ChunkSize int64
	// Client HTTP 客户端
	Client *http.Client
}

// NewAccelerator 创建加速器
func NewAccelerator(concurrency int) *Accelerator {
	if concurrency <= 0 {
		concurrency = 16
	}
	if concurrency > 64 {
		concurrency = 64
	}
	return &Accelerator{
		Concurrency: concurrency,
		ChunkSize:   2 * 1024 * 1024, // 2MB
		Client: &http.Client{
			Timeout: 0, // 不限制总时长，由 context 控制
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: concurrency + 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// DownloadProgress 下载进度
type DownloadProgress struct {
	Downloaded int64
	Total      int64
	Speed      int64 // bytes/s
}

// ProgressFunc 进度回调
type ProgressFunc func(p DownloadProgress)

// ProbeFile 探测文件大小与是否支持 Range
func (a *Accelerator) ProbeFile(ctx context.Context, url string, headers map[string]string) (size int64, acceptRanges bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return 0, false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	size = resp.ContentLength
	acceptRanges = resp.Header.Get("Accept-Ranges") == "bytes"
	return size, acceptRanges, nil
}

// Download 加速下载到指定文件
//
// 若支持 Range 且文件足够大，则并发分块下载；
// 否则退化为普通单连接下载。
func (a *Accelerator) Download(
	ctx context.Context,
	url string,
	headers map[string]string,
	destPath string,
	onProgress ProgressFunc,
) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	size, acceptRanges, err := a.ProbeFile(ctx, url, headers)
	if err != nil {
		return fmt.Errorf("探测文件失败: %w", err)
	}

	// 小文件或服务端不支持 Range，走普通下载
	if size <= 0 || !acceptRanges || size <= a.ChunkSize*2 {
		return a.simpleDownload(ctx, url, headers, destPath, size, onProgress)
	}

	return a.chunkedDownload(ctx, url, headers, destPath, size, onProgress)
}

// simpleDownload 普通单连接下载
func (a *Accelerator) simpleDownload(
	ctx context.Context, url string, headers map[string]string,
	destPath string, size int64, onProgress ProgressFunc,
) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	if size <= 0 {
		size = resp.ContentLength
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return a.copyWithProgress(resp.Body, f, size, onProgress)
}

// chunkedDownload 分块并发下载
func (a *Accelerator) chunkedDownload(
	ctx context.Context, url string, headers map[string]string,
	destPath string, size int64, onProgress ProgressFunc,
) error {
	// 预分配文件
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}

	// 计算分块
	var chunks []chunkRange
	for start := int64(0); start < size; start += a.ChunkSize {
		end := start + a.ChunkSize - 1
		if end >= size-1 {
			end = size - 1
		}
		chunks = append(chunks, chunkRange{start: start, end: end})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var downloaded int64
	var firstErr error
	var errOnce sync.Once

	// 进度上报
	done := make(chan struct{})
	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			var lastBytes int64
			var lastTime = time.Now()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					cur := atomic.LoadInt64(&downloaded)
					now := time.Now()
					elapsed := now.Sub(lastTime).Seconds()
					var speed int64
					if elapsed > 0 {
						speed = int64(float64(cur-lastBytes) / elapsed)
					}
					lastBytes, lastTime = cur, now
					onProgress(DownloadProgress{Downloaded: cur, Total: size, Speed: speed})
				}
			}
		}()
	}

	sem := make(chan struct{}, a.Concurrency)
	var wg sync.WaitGroup

	for _, c := range chunks {
		wg.Add(1)
		go func(cr chunkRange) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			err := a.downloadChunk(ctx, url, headers, f, cr, &downloaded)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("分块 %d-%d 下载失败: %w", cr.start, cr.end, err)
					cancel()
				})
			}
		}(c)
	}

	wg.Wait()
	close(done)

	if firstErr != nil {
		_ = os.Remove(destPath)
		return firstErr
	}
	if onProgress != nil {
		onProgress(DownloadProgress{Downloaded: size, Total: size})
	}
	return nil
}

type chunkRange struct {
	start, end int64
}

// downloadChunk 下载单个分块并写入指定偏移
func (a *Accelerator) downloadChunk(
	ctx context.Context, url string, headers map[string]string,
	f *os.File, cr chunkRange, downloaded *int64,
) error {
	const maxRetry = 3
	var lastErr error

	for attempt := 0; attempt < maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", cr.start, cr.end))

		resp, err := a.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		buf := make([]byte, 256*1024)
		var offset int64
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := f.WriteAt(buf[:n], cr.start+offset); werr != nil {
					resp.Body.Close()
					lastErr = werr
					break
				}
				offset += int64(n)
				atomic.AddInt64(downloaded, int64(n))
			}
			if rerr == io.EOF {
				lastErr = nil
				break
			}
			if rerr != nil {
				lastErr = rerr
				break
			}
		}
		resp.Body.Close()

		if lastErr == nil {
			return nil
		}
		// 重试时回退已计入的进度
		atomic.AddInt64(downloaded, -offset)
	}
	return lastErr
}

// copyWithProgress 边拷贝边上报进度
func (a *Accelerator) copyWithProgress(r io.Reader, w io.Writer, total int64, onProgress ProgressFunc) error {
	buf := make([]byte, 256*1024)
	var written int64
	lastReport := time.Now()

	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if onProgress != nil && time.Since(lastReport) > 500*time.Millisecond {
				onProgress(DownloadProgress{Downloaded: written, Total: total})
				lastReport = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if onProgress != nil {
		onProgress(DownloadProgress{Downloaded: written, Total: total})
	}
	return nil
}

// FetchToReader 把远端文件包装成可流式读取的 ReadCloser（用于边下边播）
// 这里用最简单的单连接实现，若需加速可在此基础上加分块重组。
func (a *Accelerator) FetchToReader(ctx context.Context, url string, headers map[string]string, startByte int64) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if startByte > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startByte))
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}
