# -*- coding: utf-8 -*-
"""线上 /api/stream 的「流畅度」实测。

为什么要有这个脚本
------------------
"打开视频慢 / 播放不流畅"这类投诉，**服务端日志是完全干净的**：每个请求
都是 200，没有任何错误。2026-10-22 那次真实的故障就是 `serveMultiConn`
把每个 4MiB 分块 `io.ReadAll` 读满才按序写出 —— 首块 256KiB 之后要干等
一整块，单连接被网盘限到 300~600KB/s 时就是 7~13 秒一个字节都没有，然后
又突然灌进 16MiB。播放器把缓冲吃干 → 转圈 → 再灌一批。

所以判据不能是"有没有报错"，只能是**数据到达的连续性**。本脚本用和
浏览器 `<video>` 完全一样的读法（GET + `Range: bytes=0-`，每次读 64KiB），
记两个数：

  1. 首字节时间（TTFB）—— 对应"打开慢不慢"
  2. 两次读到数据之间的最大空窗 —— 对应"播着卡不卡"

经验值（局域网 / 家宽都适用）：
  · TTFB 应在 1.5 秒内；
  · 最大空窗应远小于 1 秒（新实现实测 12~50ms；旧实现 600~700ms 起，
    线上环境会更夸张）。

用法
----
    # 默认读 25 秒
    python scripts/probe-stream.py 8775

    # 指定时长
    python scripts/probe-stream.py 8750 40

令牌：优先读环境变量 KID_TOKEN；没有就读 `.workbuddy/tmp/.kidtoken`
（本地调试用的那份，登录脚本写在那个路径）。**注意别用 `/tmp/xxx`
这种路径跨工具传 token** —— Git Bash 的 /tmp 和 Windows 侧 Python 的
/tmp 不是同一个目录，会读到上一次的过期令牌，表现为莫名其妙的 401。
"""
import json
import os
import sys
import time
import urllib.request

BASE = os.environ.get("SITE_URL", "https://study.19860819.xyz:8616").rstrip("/")


def load_token():
    t = (os.environ.get("KID_TOKEN") or "").strip()
    if t:
        return t
    here = os.path.dirname(os.path.abspath(__file__))
    for rel in ("../.workbuddy/tmp/.kidtoken", ".kidtoken"):
        p = os.path.normpath(os.path.join(here, rel))
        if os.path.exists(p):
            with open(p, encoding="utf-8") as f:
                return f.read().strip()
    return ""


TOKEN = load_token()
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def api_get(path):
    req = urllib.request.Request(BASE + path)
    if TOKEN:
        req.add_header("Authorization", "Bearer " + TOKEN)
    with OPENER.open(req, timeout=60) as resp:
        return json.load(resp)


def probe(rid, seconds=25):
    if not TOKEN:
        print("缺令牌：设 KID_TOKEN 环境变量，或先跑一次登录脚本")
        return 2

    d = (api_get("/api/app/play/%d" % rid) or {}).get("data") or {}
    url = d.get("url") or ""
    if not url:
        print("这个资源当前没有可直接播的地址（playMode=%s，可能在转码）" % d.get("playMode"))
        return 2
    if not url.startswith("http"):
        url = BASE + url

    print("资源 %d  %s  ext=%s  playMode=%s"
          % (rid, (d.get("title") or "")[:24], d.get("ext"), d.get("playMode")))
    print("串流地址：%s" % url[:96])

    req = urllib.request.Request(url)
    req.add_header("Range", "bytes=0-")
    t0 = time.time()
    resp = OPENER.open(req, timeout=120)
    ttfb = None
    total = 0
    last = None
    gaps = []
    chunk = 64 << 10
    try:
        while time.time() - t0 < seconds:
            buf = resp.read(chunk)
            now = time.time()
            if not buf:
                break
            if ttfb is None:
                ttfb = now - t0
            else:
                gaps.append((now - last, total))
            last = now
            total += len(buf)
    finally:
        resp.close()

    el = time.time() - t0
    print("状态=%s  Content-Length=%s"
          % (getattr(resp, "status", "?"), resp.headers.get("Content-Length")))
    print("首字节 %.2f s   已收 %.1f MiB   用时 %.1f s   均速 %.0f KiB/s"
          % (ttfb or -1, total / 1048576.0, el, total / 1024.0 / max(el, .001)))

    if not gaps:
        print("只读到一块，样本不足")
        return 0

    mg = max(gaps, key=lambda x: x[0])
    over1 = [g for g in gaps if g[0] > 1.0]
    print("最大空窗 %.0f ms（发生在已收 %.1f MiB 处）" % (mg[0] * 1000, mg[1] / 1048576.0))
    print("超过 1 秒的空窗：%d 个" % len(over1))
    for g in over1[:5]:
        print("   %.2f s @ %.1f MiB" % (g[0], g[1] / 1048576.0))

    # 判据：TTFB < 1.5s 且没有超过 1 秒的空窗
    if (ttfb or 99) > 1.5 or over1:
        print("→ 不合格：还在憋块或上游太慢")
        return 1
    print("→ 合格：首字节快、数据连续")
    return 0


if __name__ == "__main__":
    rid = int(sys.argv[1]) if len(sys.argv) > 1 else 8775
    secs = float(sys.argv[2]) if len(sys.argv) > 2 else 25
    sys.exit(probe(rid, secs))
