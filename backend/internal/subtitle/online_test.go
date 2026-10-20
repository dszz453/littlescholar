package subtitle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 假字幕站：照 OpenSubtitles REST v1 的形状回，一个走通全流程，一个空结果。
//
// 为什么非得搭个假站：真站点要 key、要配额、还联网。不搭的话这条链路
// 只能靠"人工上线点一下"来验证，而这条链路上有三个必须自己拼对的字符串
// （/subtitles、/download、files[0].file_id），任何一个拼错都表现为
// "搜不到字幕"，看着跟"网上真没有"一模一样。
type fakeSubSite struct {
	srv      *httptest.Server
	searched []string // 记录被查过的 query
	download int      // 被请求下载的 file_id
}

func newFakeSubSite(t *testing.T, hits bool) *fakeSubSite {
	t.Helper()
	f := &fakeSubSite{}
	mux := http.NewServeMux()

	mux.HandleFunc("/subtitles", func(w http.ResponseWriter, r *http.Request) {
		f.searched = append(f.searched, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		if !hits {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		io.WriteString(w, `{"data":[
			{"attributes":{"language":"zh-cn","download_count":4321,
			  "files":[{"file_id":"777","file_name":"Peppa.Pig.S01E01.srt"}],
			  "feature_details":{"title":"Peppa Pig","year":2004}}}
		]}`)
	})

	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			File int64 `json:"file_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.download = int(body.File)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"link":"`+f.srv.URL+`/file/777"}`)
	})

	mux.HandleFunc("/file/777", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\n小猪佩奇你好\n")
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestSearchFetchesAndConvertsToVTT(t *testing.T) {
	f := newFakeSubSite(t, true)
	o := NewOnline("fake-key")
	o.Base = f.srv.URL

	vtt, src, err := o.Search(context.Background(), "小猪佩奇", 1, 1)
	if err != nil {
		t.Fatalf("搜索应当成功，实际：%v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(vtt), "WEBVTT") {
		head := vtt
		if len(head) > 20 {
			head = head[:20]
		}
		t.Fatalf("产物不是 WebVTT：%q", head)
	}
	// 只断言"有内容"会放过编码全坏的情况 —— 必须断言正文里那句话
	if !strings.Contains(vtt, "小猪佩奇你好") {
		t.Fatalf("字幕正文丢了：%q", vtt)
	}
	if !strings.Contains(src, "zh-cn") {
		t.Fatalf("来源没带语言：%q", src)
	}
	if f.download != 777 {
		t.Fatalf("下载用的 file_id 不对：%d", f.download)
	}
	if len(f.searched) != 1 || f.searched[0] == "" {
		t.Fatalf("查询词是空的，等于没搜：%#v", f.searched)
	}
}

func TestSearchReportsNothingFound(t *testing.T) {
	f := newFakeSubSite(t, false)
	o := NewOnline("fake-key")
	o.Base = f.srv.URL

	_, _, err := o.Search(context.Background(), "小猪佩奇", 1, 1)
	if err == nil {
		t.Fatal("没搜到却报成功 —— 采集器会把它当成失败反复重试")
	}
	// 采集器靠这句话区分"正常没有"和"真出错"，改了这里要一起改
	if !strings.Contains(err.Error(), "没有匹配到") {
		t.Fatalf("错误文案变了，采集器的分类会失效：%v", err)
	}
}

func TestSearchDisabledWithoutKey(t *testing.T) {
	o := NewOnline("  ")
	if o.Enabled() {
		t.Fatal("没有 key 却认为可用")
	}
	if _, _, err := o.Search(context.Background(), "x", 0, 0); err == nil {
		t.Fatal("没配 key 却搜成功了")
	}
}
