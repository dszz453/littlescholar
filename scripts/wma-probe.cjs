// WMA 打开链路探针（标准版形态：页面来自 https 远端站点）。
//
// 要回答的问题只有一个：**播放页到底有没有走进"原生引擎"那条路**。
// 标准版和 PRO 用的是同一份孩子端产物，代码里对 wma 的处理是
//   playInfo.nativeAudio + nativeUrl  →  判断 window.KidNative 有没有 nativeAudio 方法
//   →  有就把原文件地址交给壳的 libVLC，页面里**不再有 <audio>**
// 所以只要这一路断了，页面就会退回 <audio>，而 Chromium 解不了 wma，
// 表现就是"点播放按钮没有任何反应"（错误事件在部分内核上根本不触发）。
//
// 做法：/api/* 由 CDP 的 Fetch 域伪造（不需要账号），并在页面脚本之前
// 注入一个**假的** window.KidNative —— 它只记录调用，不真的播放。
//
// 用法：node scripts/wma-probe.cjs [base_url] [resource_id]

const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const CHROME = process.env.CHROME_PATH
  || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const PORT = Number(process.env.CDP_PORT || 9345);
const BASE = (process.argv[2] || process.env.BASE || 'https://study.19860819.xyz:8616')
  .replace(/\/$/, '');
const RID = Number(process.argv[3] || 9);
const OUT = path.join(__dirname, '..', '.workbuddy', 'tmp', 'wma-probe');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 一条 wma 的 playInfo：字段与后端 handler/play.go 的返回一模一样。
function wmaInfo(id) {
  return {
    resourceId: id,
    title: '探针-wma 音频',
    mediaType: 'audio',
    ext: 'wma',
    playMode: 'audioformat',
    url: `/api/audio/${id}?t=probe`,
    nativeAudio: true,
    nativeUrl: `/api/stream/${id}?t=probe`,
    poster: '',
    duration: 217,
    lastPosition: 0,
    qualities: [],
    categoryName: '探针',
    transcodeTaskId: 0,
    transcodeProgress: 0,
    quality: 'original',
    transcodeAvailable: true,
    smoothUnavailable: false,
    containerUnsupported: false,
    ebookFormat: '',
    subtitleUrl: '',
  };
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--hide-scrollbars',
    '--no-first-run', '--no-default-browser-check', '--disable-extensions',
    '--disable-background-networking', '--autoplay-policy=no-user-gesture-required',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${path.join(OUT, 'profile')}`,
    'about:blank',
  ], { stdio: 'ignore' });

  let wsUrl = null;
  for (let i = 0; i < 60; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
      const page = list.find((t) => t.type === 'page');
      if (page) { wsUrl = page.webSocketDebuggerUrl; break; }
    } catch (e) { /* not up */ }
    await sleep(300);
  }
  if (!wsUrl) { console.error('连不上 DevTools'); chrome.kill(); process.exit(1); }

  const ws = new WebSocket(wsUrl);
  await new Promise((res) => ws.addEventListener('open', res));

  let id = 0;
  const pending = new Map();
  const exceptions = [];
  const consoleErrors = [];
  const failed = [];
  const urlOf = new Map();

  const cmd = (method, params) => new Promise((resolve) => {
    const myId = ++id;
    pending.set(myId, resolve);
    ws.send(JSON.stringify({ id: myId, method, params: params || {} }));
  });

  ws.addEventListener('message', async (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }

    if (m.method === 'Runtime.exceptionThrown') {
      exceptions.push(m.params.exceptionDetails.text
        || (m.params.exceptionDetails.exception
          && m.params.exceptionDetails.exception.description) || 'exception');
    }
    if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
      consoleErrors.push((m.params.args || [])
        .map((a) => a.value ?? a.description ?? '').join(' '));
    }
    if (m.method === 'Network.requestWillBeSent') {
      urlOf.set(m.params.requestId, m.params.request.url);
    }
    if (m.method === 'Network.loadingFailed' && m.params.errorText !== 'Aborted') {
      failed.push(`${m.params.errorText} <${urlOf.get(m.params.requestId) || '?'}>`);
    }

    if (m.method === 'Fetch.requestPaused') {
      const url = m.params.request.url;
      const p = url.replace(BASE, '').split('?')[0];
      if (!/\/api\//.test(p)) {
        await cmd('Fetch.continueRequest', { requestId: m.params.requestId });
        return;
      }
      let data;
      if (p === `/api/app/play/${RID}`) data = wmaInfo(RID);
      else if (p.endsWith('/app/auth/profile')) data = { id: 1, nickname: '小朋友', role: 'kid' };
      else if (p.endsWith('/app/play/' + RID + '/playlist')) data = [];
      else if (p.endsWith('/app/settings')) data = {};

      if (data !== undefined) {
        const payload = JSON.stringify({ code: 0, message: 'success', data });
        await cmd('Fetch.fulfillRequest', {
          requestId: m.params.requestId,
          responseCode: 200,
          responseHeaders: [{ name: 'Content-Type', value: 'application/json; charset=utf-8' }],
          body: Buffer.from(payload, 'utf8').toString('base64'),
        });
        return;
      }
      // 其它接口回一个空信封，避免页面卡在 loading
      const payload = JSON.stringify({ code: 0, message: '', data: null });
      await cmd('Fetch.fulfillRequest', {
        requestId: m.params.requestId,
        responseCode: 200,
        responseHeaders: [{ name: 'Content-Type', value: 'application/json; charset=utf-8' }],
        body: Buffer.from(payload, 'utf8').toString('base64'),
      });
    }
  });

  const evaluate = async (expression) => {
    const r = await cmd('Runtime.evaluate',
      { expression, returnByValue: true, awaitPromise: true });
    if (r.result && r.result.exceptionDetails) {
      return `EVAL_ERROR ${r.result.exceptionDetails.text || ''}`;
    }
    return r.result && r.result.result ? r.result.result.value : undefined;
  };

  await cmd('Page.enable');
  await cmd('Runtime.enable');
  await cmd('Network.enable');
  await cmd('Fetch.enable', { patterns: [{ urlPattern: '*://*/api/*' }] });

  // 桥必须在页面脚本之前就在：壳里是 addJavascriptInterface，页面一加载就有。
  await cmd('Page.addScriptToEvaluateOnNewDocument', {
    source: `
      window.__cmdLog = [];
      try { localStorage.setItem('kid_token', 'probe-token'); } catch (e) {}
      window.KidNative = {
        setKeepAlive: function (on) { window.__cmdLog.push({ fn: 'setKeepAlive', on: !!on }); },
        setPlaying: function (on) { window.__cmdLog.push({ fn: 'setPlaying', on: !!on }); },
        takeOver: function (s) { window.__cmdLog.push({ fn: 'takeOver', s: String(s || '').slice(0, 120) }); },
        giveBack: function () { window.__cmdLog.push({ fn: 'giveBack' }); },
        nativeAudio: function (s) {
          window.__cmdLog.push({ fn: 'nativeAudio', s: String(s || '').slice(0, 300) });
        }
      };
      window.__errors = [];
      window.addEventListener('error', function (e) {
        window.__errors.push(String(e && e.message));
      }, true);
    `,
  });

  await cmd('Emulation.setDeviceMetricsOverride',
    { width: 412, height: 915, deviceScaleFactor: 2, mobile: true });
  await cmd('Emulation.setUserAgentOverride', {
    userAgent: 'Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 '
      + '(KHTML, like Gecko) Chrome/122.0.0.0 Mobile Safari/537.36',
    platform: 'Linux armv8l',
  });

  await cmd('Page.navigate', { url: `${BASE}/play/${RID}` });
  await sleep(4000);

  const before = await evaluate('JSON.stringify(window.__cmdLog || [])');
  const stateBefore = await evaluate(`JSON.stringify({
    hasBridge: !!(window.KidNative && typeof window.KidNative.nativeAudio === 'function'),
    audioEls: document.querySelectorAll('audio').length,
    videoEls: document.querySelectorAll('video').length,
    bodyText: (document.body.innerText || '').replace(/\\s+/g, ' ').slice(0, 300),
  })`);

  // 点一下页面上的播放按钮（原生模式下它是唯一能"让它响"的入口）
  const clicked = await evaluate(`(() => {
    const btns = Array.from(document.querySelectorAll('button, [role=button], .play-btn, .player-toggle'));
    const hit = btns.find((b) => /播放|play/i.test((b.getAttribute('aria-label') || '') + b.className));
    if (hit) { hit.click(); return 'clicked:' + (hit.getAttribute('aria-label') || hit.className); }
    // 兜底：点画面中央
    const area = document.querySelector('.video-wrap, .player, .stage');
    if (area) { area.click(); return 'clicked-area'; }
    return 'no-button';
  })()`);
  await sleep(1500);

  const after = await evaluate('JSON.stringify(window.__cmdLog || [])');
  const pageErrors = await evaluate('JSON.stringify(window.__errors || [])');

  const shot = await cmd('Page.captureScreenshot', { format: 'png' });
  if (shot.result && shot.result.data) {
    fs.writeFileSync(path.join(OUT, 'wma.png'), Buffer.from(shot.result.data, 'base64'));
  }

  const parse = (s) => { try { return JSON.parse(s); } catch { return s; } };
  const log = parse(after) || [];
  const nativeCalls = Array.isArray(log) ? log.filter((x) => x.fn === 'nativeAudio') : [];

  console.log('=== 页面状态 ===');
  console.log(stateBefore);
  console.log('=== 点击 ===', clicked);
  console.log('=== 桥调用（点播放前） ===');
  console.log(before);
  console.log('=== 桥调用（点播放后） ===');
  console.log(after);
  console.log('=== 判定 ===');
  console.log('nativeAudio 调用次数:', nativeCalls.length);
  console.log('页面内 <audio> 数量:', JSON.parse(stateBefore || '{}').audioEls);
  console.log('=== 异常 ===', exceptions.slice(0, 5));
  console.log('=== console.error ===', consoleErrors.slice(0, 5));
  console.log('=== 失败请求 ===', failed.slice(0, 5));
  console.log('=== 页面 error ===', pageErrors);
  console.log('截图:', path.join(OUT, 'wma.png'));

  chrome.kill();
}

main().catch((e) => { console.error(e); process.exit(1); });
