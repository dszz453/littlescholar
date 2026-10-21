// 校验"原生音频（wma）在页面侧真的走了原生路径，且切后台不会重复接管"。
//
// 背景：Chromium 完全解不了 wma（`canPlayType('audio/x-ms-wma')` 返回空串），
// 所以 wma 由壳里的 libVLC 播，页面退化成"遥控器 + 字幕层"。这条链路有三处
// **只会在真跑起来时才暴露**的问题，接口层全绿看不出来：
//
//   ① 页面没切到原生模式（还挂着 <audio> 去拉转码地址）→ 用户看到的是
//      "点开等半分钟才出声"，而不是报错；
//   ② 原生模式下仍判定"可以交接"→ 切后台时把 nativeUrl（原文件）交给壳的
//      MediaPlayer：支持 wma 的机型**同时出声**，不支持的机型 prepare 失败、
//      面板闪一下退回暂停；
//   ③ 页面没注册 `window.__lsNative` → 原生推来的 position 全丢，进度条不动、
//      字幕不跟（且没有任何报错）。
//
// 用法（令牌自己给，脚本不存任何凭据）：
//   KID_TOKEN=<孩子端令牌> node scripts/verify-native-audio.cjs
//   KID_TOKEN=… SITE_URL=https://study.19860819.xyz:8616 \
//     WMA_ID=8415 MP3_ID=8750 node scripts/verify-native-audio.cjs
//
// 断言的是**页面行为**，不是源码文本：源码里写没写 `nativeAudioMode` 无所谓，
// 关键是 DOM 里有没有 <audio>、桥收到了什么指令。

const { spawn } = require('child_process')
const fs = require('fs')
const os = require('os')
const path = require('path')
const http = require('http')

const SITE = (process.env.SITE_URL || 'https://study.19860819.xyz:8616').replace(/\/+$/, '')
const TOKEN = (process.env.KID_TOKEN || '').trim()
const WMA_ID = process.env.WMA_ID || '8415'
const MP3_ID = process.env.MP3_ID || '8750'
const CHROME =
  process.env.CHROME_PATH || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'
const CDP_PORT = 9700 + Math.floor(Math.random() * 90)
const WORK = path.join(os.tmpdir(), 'kid-native-audio-' + Date.now())
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

if (!TOKEN) {
  console.error('缺 KID_TOKEN：先登录孩子端，把 access token 用环境变量传进来。')
  process.exit(2)
}

// 页面对桥只有一个要求：存在且有 nativeAudio 方法。其余方法按真实壳补齐，
// 这样页面走到的每一条分支都和真机一致（少了 mediaState 会走"没有桥"的兜底）。
//
// ⚠️ 关键：桩桥必须**像真引擎一样回推状态**。
//    只记指令不回推的话，页面里 `isPlaying` 永远是 false，
//    "切后台要不要交接"那条判据（它要求正在播）就永远不成立 ——
//    于是无论页面对不对，断言都通过，等于没测。
//    所以这里收到 play 就按时钟推 onState，pause/seek/stop 也照做。
const STUB = `
window.__nativeLog = [];
window.__fakeClock = { timer: null, pos: 0, dur: 200, playing: false };
window.__pushNative = function () {
  if (!window.__lsNative || typeof window.__lsNative.onState !== 'function') { return; }
  window.__lsNative.onState({
    playing: window.__fakeClock.playing,
    position: window.__fakeClock.pos,
    duration: window.__fakeClock.dur,
    ended: false,
    error: ''
  });
};
window.__tick = function () {
  if (window.__fakeClock.playing) {
    window.__fakeClock.pos = Math.min(window.__fakeClock.pos + 0.5, window.__fakeClock.dur);
  }
  window.__pushNative();
};
window.__startFake = function () {
  if (window.__fakeClock.timer) { return; }
  window.__fakeClock.timer = setInterval(window.__tick, 500);
};
window.__stopFake = function () {
  if (window.__fakeClock.timer) { clearInterval(window.__fakeClock.timer); }
  window.__fakeClock.timer = null;
};
window.KidNative = {
  nativeAudio: function (json) {
    window.__nativeLog.push({ kind: 'nativeAudio', data: json });
    var o = {};
    try { o = JSON.parse(json || '{}'); } catch (e) {}
    var c = window.__fakeClock;
    if (o.cmd === 'play') {
      c.pos = Number(o.position) || 0;
      if (Number(o.duration) > 0) { c.dur = Number(o.duration); }
      c.playing = true;
      window.__startFake();
      window.__pushNative();
    } else if (o.cmd === 'pause') {
      c.playing = false;
      window.__pushNative();
    } else if (o.cmd === 'resume') {
      c.playing = true;
      window.__startFake();
      window.__pushNative();
    } else if (o.cmd === 'seek') {
      c.pos = Number(o.position) || 0;
      window.__pushNative();
    } else if (o.cmd === 'stop') {
      c.playing = false;
      window.__stopFake();
      window.__pushNative();
    }
  },
  takeOver: function (json) { window.__nativeLog.push({ kind: 'takeOver', data: json }); },
  giveBack: function () { window.__nativeLog.push({ kind: 'giveBack' }); },
  mediaState: function (json) { window.__nativeLog.push({ kind: 'mediaState', data: json }); },
  setPlaying: function () {},
  setKeepAlive: function () {},
  play: function () {}
};
`

let pass = 0
let fail = 0
function ok(msg) {
  pass++
  console.log('  \u2713 ' + msg)
}
function bad(msg) {
  fail++
  console.log('  \u2717 ' + msg)
}

function httpJson(p) {
  return new Promise((res, rej) => {
    const req = http.request({ host: '127.0.0.1', port: CDP_PORT, path: p, method: 'GET' }, (r) => {
      let b = ''
      r.on('data', (c) => (b += c))
      r.on('end', () => {
        try {
          res(JSON.parse(b))
        } catch (e) {
          rej(new Error(b.slice(0, 120)))
        }
      })
    })
    req.on('error', rej)
    req.end()
  })
}

class CDP {
  constructor(ws) {
    this.ws = ws
    this.id = 0
    this.pend = new Map()
  }
  static attach(wsUrl) {
    return new Promise((res, rej) => {
      const ws = new WebSocket(wsUrl)
      ws.addEventListener('open', () => {
        const c = new CDP(ws)
        ws.addEventListener('message', (m) => c._on(m.data))
        res(c)
      })
      ws.addEventListener('error', () => rej(new Error('ws error')))
    })
  }
  _on(m) {
    let d
    try {
      d = JSON.parse(m)
    } catch (e) {
      return
    }
    if (d.id && this.pend.has(d.id)) {
      const { res, rej } = this.pend.get(d.id)
      this.pend.delete(d.id)
      d.error ? rej(new Error(JSON.stringify(d.error))) : res(d.result)
    }
  }
  send(method, params = {}) {
    const id = ++this.id
    return new Promise((res, rej) => {
      this.pend.set(id, { res, rej })
      this.ws.send(JSON.stringify({ id, method, params }))
    })
  }
  // ⚠️ Runtime.evaluate 里的异常**不**走 Runtime.exceptionThrown 事件，
  // 只看它返回的 exceptionDetails —— 否则调用失败是完全静默的。
  async eval(expression) {
    const r = await this.send('Runtime.evaluate', {
      expression,
      returnByValue: true,
      awaitPromise: true,
    })
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 220))
    return r.result && r.result.value
  }
  async json(expression) {
    const raw = await this.eval(`(() => { try { return JSON.stringify(${expression}); } catch (e) { return JSON.stringify({ __err: String(e) }); } })()`)
    return JSON.parse(raw || '{}')
  }
}

async function waitFor(cdp, expr, ms) {
  const deadline = Date.now() + (ms || 20000)
  while (Date.now() < deadline) {
    const v = await cdp.eval(`(() => { try { return !!(${expr}); } catch (e) { return false; } })()`)
    if (v) return true
    await sleep(400)
  }
  return false
}

;(async () => {
  fs.mkdirSync(WORK, { recursive: true })
  const chrome = spawn(
    CHROME,
    [
      '--headless=new',
      `--remote-debugging-port=${CDP_PORT}`,
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-gpu',
      '--hide-scrollbars',
      '--autoplay-policy=no-user-gesture-required',
      '--window-size=430,900',
      `--user-data-dir=${WORK}`,
      '--ignore-certificate-errors',
      'about:blank',
    ],
    { stdio: 'ignore' }
  )

  let cdp = null
  try {
    let page = null
    for (let i = 0; i < 40; i++) {
      await sleep(300)
      try {
        const list = await httpJson('/json/list')
        page = (list || []).find((t) => t.type === 'page' && t.webSocketDebuggerUrl)
        if (page) break
      } catch (e) {
        /* 还没起来 */
      }
    }
    if (!page) throw new Error('拿不到页面目标（Chrome 没起来？）')
    cdp = await CDP.attach(page.webSocketDebuggerUrl)
    await cdp.send('Runtime.enable')
    await cdp.send('Page.enable')
    // 桥必须在页面脚本之前就位：Play.vue 在 setup 阶段就会读 window.KidNative。
    await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: STUB })

    console.log('站点:', SITE)

    // 先落地到一个同源页面，把令牌写进 localStorage，再跳播放页。
    await cdp.eval(`location.href = ${JSON.stringify(SITE + '/')}`)
    await sleep(1800)
    await cdp.eval(`(() => {
      localStorage.setItem('kid_token', ${JSON.stringify(TOKEN)});
      localStorage.setItem('kid_refresh_token', ${JSON.stringify(TOKEN)});
      return true;
    })()`)

    // ---------------- wma：必须走原生 ----------------
    console.log(`\n[1] wma 资源 ${WMA_ID}（应走原生引擎）`)
    await cdp.eval(`location.href = ${JSON.stringify(SITE + '/play/' + WMA_ID)}`)

    const gotPlay = await waitFor(
      cdp,
      `(window.__nativeLog || []).some(e => e.kind === 'nativeAudio')`,
      25000
    )
    if (gotPlay) {
      ok('页面确实把播放交给了原生桥（收到 nativeAudio 指令）')
    } else {
      bad('20 秒内没收到 nativeAudio 指令 —— 页面没进原生模式（会退回服务端转码那条慢路）')
    }

    const log = await cdp.json('(window.__nativeLog || [])')
    const play = (Array.isArray(log) ? log : []).find((e) => e.kind === 'nativeAudio')
    let cmd = {}
    try {
      cmd = JSON.parse((play && play.data) || '{}')
    } catch (e) {
      cmd = {}
    }
    if (cmd.cmd === 'play') {
      ok('指令是 play')
    } else {
      bad(`第一条指令应是 play，实得 ${JSON.stringify(cmd.cmd)}`)
    }
    const u = String(cmd.url || '')
    if (/\/api\/stream\//.test(u)) {
      ok('交给原生的是原文件直链（/api/stream/…）：服务端不用转码')
    } else {
      bad(`交给原生的地址不是原文件直链：${u.slice(0, 120)}`)
    }
    if (/\/api\/audio\//.test(u)) {
      bad('地址是转码产物 /api/audio/… —— 又回到"等半分钟"那条路了')
    }

    const dom = await cdp.json(
      `({ audio: document.querySelectorAll('audio').length, path: location.pathname })`
    )
    if (dom.audio === 0) {
      ok('原生模式不挂 <audio>（走了就没有第二个声源）')
    } else {
      bad(`原生模式下仍有 ${dom.audio} 个 <audio> —— 会和原生引擎抢声音`)
    }
    if (dom.path === '/play/' + WMA_ID) {
      ok('停在播放页 ' + dom.path)
    } else {
      bad('被重定向到了 ' + dom.path + '（登录态或资源 id 有问题？）')
    }

    const hasHook = await cdp.eval(
      `!!(window.__lsNative && typeof window.__lsNative.onState === 'function')`
    )
    if (hasHook) {
      ok('页面注册了 window.__lsNative.onState（进度条/字幕靠它驱动）')
    } else {
      bad('没注册 __lsNative —— 原生推来的进度会全部丢掉，页面会一直转圈')
    }

    // ---------------- 切后台：不许再交接 ----------------
    //
    // 这是本脚本最重要的一条。原生模式下把 nativeUrl 交给壳的 MediaPlayer，
    // 支持 wma 的机型会与 VLC **同时出声**，不支持的机型则 prepare 失败、
    // 面板闪一下退回暂停 —— 两种都像"后台播放坏了"。
    //
    // 前置条件是"页面以为自己在播"（桩桥一直在推 playing=true），
    // 否则这条判据永远不成立、断言恒通过（等于没测）。
    await waitFor(cdp, `window.__fakeClock && window.__fakeClock.playing === true`, 6000)
    const playing = await cdp.eval(`!!(window.__fakeClock && window.__fakeClock.playing)`)
    if (playing) {
      ok('桩引擎在推"正在播"（这条断言的前置条件成立）')
    } else {
      bad('桩引擎没在播 —— 后面那条"不该交接"会失去意义，先查页面有没有真的下发 play')
    }

    await cdp.eval(`(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      document.dispatchEvent(new Event('visibilitychange'));
      return true;
    })()`)
    await sleep(1500)
    const afterHide = await cdp.json(
      `(window.__nativeLog || []).filter(e => e.kind === 'takeOver').length`
    )
    if (afterHide === 0) {
      ok('切后台没有请求交接（原生引擎自己就是常驻播放器）')
    } else {
      bad(`切后台仍调了 ${afterHide} 次 takeOver —— 会再起一个 MediaPlayer，可能双声道`)
    }

    // ---------------- 对照：能直放的音频不许走原生 ----------------
    console.log(`\n[2] 对照：mp3 资源 ${MP3_ID}（应留在网页里播）`)
    await cdp.eval(`location.href = ${JSON.stringify(SITE + '/play/' + MP3_ID)}`)
    const hasAudio = await waitFor(cdp, `document.querySelectorAll('audio').length > 0`, 25000)
    if (hasAudio) {
      ok('普通音频仍然用 <audio>（原生只接管解不了的格式）')
    } else {
      bad('普通音频没有 <audio> —— 不该走原生，也不该什么都不播')
    }
    const mp3Native = await cdp.json(
      `(window.__nativeLog || []).filter(e => e.kind === 'nativeAudio').length`
    )
    if (mp3Native === 0) {
      ok('普通音频没有误发 nativeAudio 指令')
    } else {
      bad(`普通音频被误判成原生：${mp3Native} 条 nativeAudio 指令`)
    }
  } catch (e) {
    fail++
    console.log('  \u2717 探针自身出错：' + e.message)
  } finally {
    try {
      cdp && cdp.ws.close()
    } catch (e) {
      /* noop */
    }
    try {
      chrome.kill()
    } catch (e) {
      /* noop */
    }
    try {
      fs.rmSync(WORK, { recursive: true, force: true })
    } catch (e) {
      /* 临时目录删不掉不算失败 */
    }
  }

  console.log(`\n通过 ${pass} 项，失败 ${fail} 项`)
  process.exit(fail === 0 ? 0 : 1)
})()
