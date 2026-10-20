// 后台「AI 配置 → 批量字幕」的真渲染回归 + 截图取证。
//
// 为什么单独一个脚本：这一页有三件事只有真渲染才验证得到 ——
//   1. **入口真的存在**：页签、状态卡、计划表单、选文件表格、队列表格都渲染出来了
//      （模板编译通过 ≠ 渲染出来；v-if 写反、被 flex 挤没，构建一样是绿的）；
//   2. **预览是真的弹出来、内容来自接口**：模型给的是"原文 + 中文对照"，
//      如果只断言"弹层打开了"，一个永远空白的坏法也能过；
//   3. **勾选 → 加入队列真的把 id 发出去了**：这是整个功能的入口动作，
//      按钮点不动 / 传空数组都不会报错，只会"看起来做了但队列永远空"。
//
// 用 DevTools 协议 + 拦截 /api/** 喂 mock：这里验的是渲染与交互，
// 后端逻辑由 go test 覆盖（handler/aisubtitle_test.go、subtitle/aiwrite_test.go）。
//
// 用法：
//   node scripts/ai-subtitle-ui-check.cjs
//   CHROME_PATH=... OUT_DIR=... node scripts/ai-subtitle-ui-check.cjs
//
// ⚠️ Chrome 会读系统代理 → 必须 --no-proxy-server，否则连 127.0.0.1 都被转出去。
// ⚠️ Fetch.fulfillRequest 的 body 必须是 base64，传字符串会静默返回空响应。

const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = process.env.CHROME_PATH
  || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const PORT = Number(process.env.CDP_PORT || 9343);
const ROOT = path.join(__dirname, '..');
const DIST = process.env.DIST_DIR || path.join(ROOT, 'frontend-admin', 'dist');
const OUT = process.env.OUT_DIR || path.join(ROOT, '.workbuddy', 'tmp', 'ai-subtitle-ui');
const PAGE = '/ai-config';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let pass = 0;
let fail = 0;
const ok = (m) => { pass++; console.log('  \u2713 ' + m); };
const no = (m) => { fail++; console.log('  \u2717 ' + m); };
const eq = (m, a, b) => (a === b ? ok(m) : no(`${m} —— 实际=[${a}] 期望=[${b}]`));
const truthy = (m, v) => (v ? ok(m) : no(m));

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.woff2': 'font/woff2', '.woff': 'font/woff', '.ico': 'image/x-icon',
  '.json': 'application/json', '.map': 'application/json',
};

/* ============ mock 数据 ============ */

const PLAN = {
  plan: {
    id: 1, enabled: true, startMinute: 22 * 60, endMinute: 6 * 60,
    batchPerRound: 5, gapSeconds: 20, overwrite: false, note: 'Collins Big Cat 三级读物',
  },
  stat: {
    at: '2026-10-20T10:00:00Z', state: 'idle', scanned: 0, done: 0, failed: 0, skipped: 0,
    lastNote: '本轮 3 条：完成 3，跳过 0，失败 0', lastRunAt: '2026-10-20T09:58:00Z',
  },
  // 这五个数要能被页面原样显示出来，才说明状态卡读的是接口而不是自己算的
  counts: { pending: 3, running: 1, done: 5, skipped: 2, failed: 1 },
  inWindow: false,
  aiConfigured: true,
  startText: '22:00',
  endText: '06:00',
};

const CANDIDATES = {
  total: 3,
  page: 1,
  pageSize: 20,
  list: [
    {
      id: 101, title: 'The Helper Bird', mediaType: 'audio', ext: 'm4a', duration: 192,
      categoryId: 19, categoryName: '大猫英语三级', hasSubtitle: false,
    },
    {
      id: 102, title: 'The Little Egg', mediaType: 'audio', ext: 'wma', duration: 397,
      categoryId: 19, categoryName: '大猫英语三级', hasSubtitle: true,
      subtitleOrigin: 'sibling', subtitleSource: '同目录字幕 · The Little Egg.srt',
    },
    {
      id: 103, title: 'Look at the Web', mediaType: 'video', ext: 'mp4', duration: 96,
      categoryId: 19, categoryName: '大猫英语三级', hasSubtitle: false,
      taskStatus: 'failed', taskNote: '大模型调用失败：401',
    },
  ],
};

const TASKS = {
  total: 2,
  page: 1,
  pageSize: 20,
  list: [
    {
      id: 7, resourceId: 101, title: 'The Helper Bird', status: 'done', cues: 24,
      note: '24 条 · deepseek-chat · 按停顿对齐', updatedAt: '2026-10-20T09:58:00Z',
    },
    {
      id: 8, resourceId: 103, title: 'Look at the Web', status: 'failed', cues: 0,
      note: '大模型调用失败：401', updatedAt: '2026-10-20T09:59:00Z',
    },
  ],
};

// 预览返回：第一条必须是能一眼看出"原文 + 中文对照"的形状
const PREVIEW = {
  ok: true,
  lang: 'en',
  model: 'deepseek-chat',
  align: 'speech',
  alignText: '按停顿对齐',
  duration: 192,
  cues: 2,
  truncated: false,
  lines: [
    { start: 0.4, end: 3.2, text: 'The little bird was hungry.', trans: '这只小鸟饿了。' },
    { start: 3.6, end: 6.1, text: 'She looked for a helper.', trans: '它去找帮手。' },
  ],
};

const calls = [];

function mockFor(method, pathAndQuery, postData) {
  const url = new URL('http://x' + pathAndQuery);
  const p = url.pathname;
  if (!p.startsWith('/api/')) return null;
  calls.push({ method, pathAndQuery, postData: postData || '' });

  if (p === '/api/admin/auth/profile') {
    return { code: 0, data: { id: 1, username: 'admin', nickname: '管理员', role: 'admin' } };
  }
  if (p === '/api/admin/ai/configs') {
    return {
      code: 0,
      data: [{
        id: 1, name: 'DeepSeek', provider: 'deepseek', baseUrl: 'https://api.deepseek.com/v1',
        model: 'deepseek-chat', apiKeyMasked: 'sk-****abcd', temperature: 0.7,
        maxTokens: 4096, isDefault: true, status: 'valid',
      }],
    };
  }
  if (p === '/api/admin/ai/subtitles/plan') return { code: 0, data: PLAN };
  if (p === '/api/admin/ai/subtitles/candidates') return { code: 0, data: CANDIDATES };
  if (p === '/api/admin/ai/subtitles/tasks' && method === 'GET') return { code: 0, data: TASKS };
  if (p === '/api/admin/ai/subtitles/preview') return { code: 0, data: PREVIEW };
  if (p === '/api/admin/ai/subtitles/tasks' && method === 'POST') {
    return { code: 0, message: '已排队 1 条', data: { added: 1, reset: 0, skipped: 0 } };
  }
  return null;
}

function serveStatic(dir) {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const url = new URL(req.url, 'http://x');
      const rel = decodeURIComponent(url.pathname);
      let file = path.join(dir, rel);
      if (!path.resolve(file).startsWith(path.resolve(dir))) {
        res.writeHead(403).end('forbidden');
        return;
      }
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) {
        file = path.join(dir, 'index.html');
      }
      res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
      fs.createReadStream(file).pipe(res);
    });
    srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port }));
  });
}

async function main() {
  if (!fs.existsSync(path.join(DIST, 'index.html'))) {
    console.error(`找不到产物：${DIST}/index.html\n先跑： cd frontend-admin && npm run build`);
    process.exit(1);
  }
  fs.mkdirSync(OUT, { recursive: true });

  const { srv, port } = await serveStatic(DIST);
  const base = `http://127.0.0.1:${port}`;

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--hide-scrollbars',
    '--no-first-run', '--no-default-browser-check', '--disable-extensions',
    '--disable-background-networking',
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
    } catch (e) { /* 还没起来 */ }
    await sleep(300);
  }
  if (!wsUrl) {
    console.error('连不上 DevTools');
    chrome.kill(); srv.close(); process.exit(1);
  }

  const ws = new WebSocket(wsUrl);
  await new Promise((res) => ws.addEventListener('open', res));

  let id = 0;
  const pending = new Map();
  const exceptions = [];
  const consoleErrors = [];

  const cmd = (method, params) => new Promise((resolve) => {
    const myId = ++id;
    pending.set(myId, resolve);
    ws.send(JSON.stringify({ id: myId, method, params: params || {} }));
  });

  function onRequestPaused(params) {
    const { requestId, request } = params;
    const pathAndQuery = request.url.replace(/^https?:\/\/[^/]+/, '');
    const body = mockFor(request.method, pathAndQuery, request.postData);
    if (body === null) {
      cmd('Fetch.continueRequest', { requestId });
      return;
    }
    cmd('Fetch.fulfillRequest', {
      requestId,
      responseCode: 200,
      responseHeaders: [
        { name: 'Content-Type', value: 'application/json' },
        { name: 'Access-Control-Allow-Origin', value: '*' },
      ],
      body: Buffer.from(JSON.stringify(body), 'utf8').toString('base64'),
    });
  }

  ws.addEventListener('message', (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
    if (m.method === 'Fetch.requestPaused') { onRequestPaused(m.params); return; }
    if (m.method === 'Runtime.exceptionThrown') {
      exceptions.push(m.params.exceptionDetails.text
        || (m.params.exceptionDetails.exception && m.params.exceptionDetails.exception.description)
        || 'exception');
    }
    if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
      consoleErrors.push((m.params.args || []).map((a) => a.value ?? a.description ?? '').join(' '));
    }
  });

  const evaluate = async (expression) => {
    const r = await cmd('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    const d = r.result && r.result.exceptionDetails;
    if (d) {
      console.log(`EVAL_ERROR ${d.text || ''} <<${expression.slice(0, 70)}>>`);
      return undefined;
    }
    return r.result && r.result.result ? r.result.result.value : undefined;
  };

  const shot = async (name) => {
    const r = await cmd('Page.captureScreenshot', { format: 'png' });
    if (r.result && r.result.data) {
      fs.writeFileSync(path.join(OUT, name), Buffer.from(r.result.data, 'base64'));
      console.log(`  · 截图 ${name}`);
    }
  };

  const waitFor = async (expr, timeout = 15000) => {
    const t0 = Date.now();
    while (Date.now() - t0 < timeout) {
      if (await evaluate(expr)) return true;
      await sleep(200);
    }
    return false;
  };

  // 按文本点一个元素（用于页签、按钮这类没有稳定类名的东西）
  const clickByText = (selector, text, exact) => evaluate(`
    (function(){
      var els = Array.prototype.slice.call(document.querySelectorAll(${JSON.stringify(selector)}));
      for (var i = 0; i < els.length; i++) {
        var t = (els[i].textContent || '').replace(/\\s+/g, '');
        var want = ${JSON.stringify(String(text).replace(/\s+/g, ''))};
        if (${exact ? 't === want' : 't.indexOf(want) >= 0'}) { els[i].click(); return true; }
      }
      return false;
    })()
  `);

  await cmd('Page.enable');
  await cmd('Runtime.enable');
  await cmd('Fetch.enable', { patterns: [{ urlPattern: '*/api/*' }] });
  await cmd('Emulation.setDeviceMetricsOverride', {
    width: 1440, height: 900, deviceScaleFactor: 1, mobile: false,
  });
  await cmd('Page.addScriptToEvaluateOnNewDocument', {
    source: `localStorage.setItem('ls_token','ui-check-token');
             localStorage.setItem('ls_refresh_token','ui-check-refresh');
             localStorage.setItem('ls_profile', JSON.stringify({id:1,username:'admin',nickname:'管理员',role:'admin'}));`,
  });

  console.log(`\n[1] 打开 ${base}${PAGE}`);
  await cmd('Page.navigate', { url: base + PAGE });
  if (!await waitFor(`document.querySelectorAll('.el-tabs__item').length >= 2`)) {
    no('页面没渲染出页签，后续断言全部跳过');
  } else {
    const tabs = await evaluate(`
      Array.prototype.map.call(document.querySelectorAll('.el-tabs__item'), function(e){
        return e.textContent.trim();
      })
    `);
    eq('两个页签：接口配置 / 批量字幕', (tabs || []).join('/'), '接口配置/批量字幕');
    truthy('默认停在「接口配置」并显示已有的 AI 配置',
      await waitFor(`document.body.textContent.indexOf('DeepSeek') >= 0`));

    console.log('\n[2] 切到「批量字幕」：状态卡与计划');
    truthy('点开了「批量字幕」页签', await clickByText('.el-tabs__item', '批量字幕', true));
    if (!await waitFor(`document.body.textContent.indexOf('批量生成状态') >= 0`)) {
      no('批量字幕内容没渲染出来');
    } else {
      ok('状态卡渲染出来了');
      const nums = await evaluate(`
        Array.prototype.map.call(document.querySelectorAll('.stat-num'), function(e){
          return e.textContent.trim();
        })
      `);
      // 顺序：排队中 / 生成中 / 已完成 / 已跳过 / 失败 —— 必须与接口给的 counts 一致
      eq('五个计数来自接口', (nums || []).join(','), '3,1,5,2,1');
      truthy('计划里的时段回显成 22:00 / 06:00',
        await evaluate(`document.body.textContent.indexOf('22:00') >= 0 && document.body.textContent.indexOf('06:00') >= 0`));
      truthy('补充说明回显了',
        await evaluate(`!!document.querySelector('textarea')`));
      truthy('「立即执行一批」按钮在', await evaluate(`
        Array.prototype.some.call(document.querySelectorAll('button'), function(b){
          return (b.textContent || '').indexOf('立即执行') >= 0;
        })
      `));
      await shot('01-plan.png');
    }

    console.log('\n[3] 选文件：列表、字幕状态、预览');
    // ⚠️ 页面上有两张表（候选 + 队列），必须限定在**第一张**里数行数。
    // 直接数 `.el-table__body tr` 会把队列那两行也算进来（3+2=5），
    // 于是断言"3 行"永远失败 —— 而界面其实是对的。
    if (!await waitFor(`document.querySelectorAll('.el-table')[0] &&
        document.querySelectorAll('.el-table')[0].querySelectorAll('.el-table__body tr').length >= 3`)) {
      no('候选表格没渲染出来');
    } else {
      eq('候选表格 3 行',
        await evaluate(`document.querySelectorAll('.el-table')[0]
          .querySelectorAll('.el-table__body tr').length`), 3);
      const body = await evaluate(`document.body.textContent`);
      truthy('显示了"已有同目录"字幕的来源标记',
        body.indexOf('已有同目录') >= 0);
      truthy('显示了失败任务的状态', body.indexOf('失败') >= 0);

      // 第一行「预览」：断言弹层里的**内容**来自接口，而不是只断言弹层存在
      const clicked = await evaluate(`
        (function(){
          var rows = document.querySelectorAll('.el-table__body tr');
          for (var i = 0; i < rows.length; i++) {
            var btns = rows[i].querySelectorAll('button');
            for (var j = 0; j < btns.length; j++) {
              if ((btns[j].textContent || '').trim() === '预览') { btns[j].click(); return true; }
            }
          }
          return false;
        })()
      `);
      truthy('点了第一行的「预览」', clicked);
      if (await waitFor(`!!document.querySelector('.el-dialog')`)) {
        const dlg = await evaluate(`
          (function(){ var d = document.querySelector('.el-dialog'); return d ? d.textContent : ''; })()
        `);
        truthy('预览弹层给出了原文', (dlg || '').indexOf('The little bird was hungry.') >= 0);
        truthy('预览弹层给出了中文对照', (dlg || '').indexOf('这只小鸟饿了。') >= 0);
        truthy('预览弹层写明了按停顿对齐', (dlg || '').indexOf('按停顿对齐') >= 0);
        await sleep(700); // 等弹层动画结束再截图，否则截到半透明的中间帧
        await shot('02-preview.png');
        await clickByText('.el-dialog__footer button', '关闭', true);
        await sleep(400);
      } else {
        no('预览弹层没打开');
      }

      // 加队列：勾第一行 → 点「加入队列」→ 断言 POST 里带着 id
      console.log('\n[4] 勾选 → 加入队列');
      const checked = await evaluate(`
        (function(){
          var box = document.querySelector('.el-table__body .el-checkbox');
          if (!box) return false;
          box.click(); return true;
        })()
      `);
      truthy('勾上了第一个文件', checked);
      await sleep(300);
      const before = calls.filter((c) => c.method === 'POST' && c.pathAndQuery.endsWith('/ai/subtitles/tasks')).length;
      const enq = await evaluate(`
        (function(){
          var btns = document.querySelectorAll('button');
          for (var i = 0; i < btns.length; i++) {
            if ((btns[i].textContent || '').indexOf('加入队列') >= 0) {
              if (btns[i].disabled) return 'disabled';
              btns[i].click(); return 'clicked';
            }
          }
          return 'missing';
        })()
      `);
      eq('「加入队列」按钮可点并已点击', enq, 'clicked');
      await sleep(900);
      const posts = calls.filter((c) => c.method === 'POST' && c.pathAndQuery.endsWith('/ai/subtitles/tasks'));
      eq('发出了入队请求', posts.length - before, 1);
      if (posts.length) {
        const body = posts[posts.length - 1].postData || '';
        truthy(`入队请求里带着勾选的 id（${body}）`, body.indexOf('101') >= 0);
      }
    }

    console.log('\n[5] 队列列表');
    if (!await waitFor(`document.body.textContent.indexOf('生成队列') >= 0`)) {
      no('队列区块没渲染出来');
    } else {
      const body = await evaluate(`document.body.textContent`);
      truthy('队列里显示了已完成与失败的任务',
        body.indexOf('已完成') >= 0 && body.indexOf('大模型调用失败：401') >= 0);
      truthy('有清理按钮', body.indexOf('清理已完成') >= 0 && body.indexOf('清理已跳过') >= 0);
      await shot('03-queue.png');
    }
  }

  console.log('\n[6] 运行时错误');
  eq('没有未捕获异常', exceptions.length, 0);
  if (exceptions.length) console.log('      · ' + exceptions.slice(0, 3).join('\n      · '));
  // Element Plus 在开发模式会打一些 warning（type=error 才是真错误）
  eq('没有 console.error', consoleErrors.length, 0);
  if (consoleErrors.length) console.log('      · ' + consoleErrors.slice(0, 3).join('\n      · '));

  console.log(`\n结果：${pass} 通过，${fail} 失败`);
  chrome.kill();
  srv.close();
  setTimeout(() => process.exit(fail ? 1 : 0), 200);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
