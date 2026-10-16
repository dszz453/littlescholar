// 后台「资源映射」页的真渲染回归 + 截图取证。
//
// 为什么值得单独一个脚本：本轮新加的两个按钮里，「解绑全部」是**破坏性操作**，
// 而它有三件事只有真渲染才验证得到：
//
//   1. 按钮真的出现在工具栏上 —— 模板编译通过 ≠ 渲染出来（改错了层级、
//      被 flex 挤没了、v-if 条件写反了，构建一样是绿的）；
//   2. 点下去真的有二次确认弹窗，且弹窗里的**数字来自后端 dryRun** ——
//      预览数不是前端把 resourceCount 加一遍算出来的（那样会漏掉历史资源，
//      线上绝大多数资源都没有 mapping_id，自己加必然少算）；
//   3. 确认词校验真的拦得住 —— 这是这个按钮不被误点的唯一保障。
//
// 用 DevTools 协议 + **拦截 /api/** 喂 mock 数据**：这一页要验的是"渲染与交互"，
// 不是"后端逻辑对不对"（后者由 go test 覆盖）。所以不用起 postgres。
//
// mock 数据里的 resourceCount 之和（2+1=3）**故意**与 dryRun 报的
// removedResources（5）不一致 —— 弹窗里显示 5 才说明它读的是后端的数。
//
// 用法：
//   node scripts/admin-ui-check.cjs
//   CHROME_PATH=... OUT_DIR=... node scripts/admin-ui-check.cjs
//
// ⚠️ Chrome 会读系统代理，沙箱里连 127.0.0.1 都会被转出去 → 必须
// --no-proxy-server（与 edge-ui-shot.cjs 同源的问题）。
// ⚠️ Fetch.fulfillRequest 的 body 必须是 base64，直接传字符串会静默返回空响应，
// 表现是页面所有接口都拿到空 body、界面一片空 —— 看起来像"接口都挂了"。

const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = process.env.CHROME_PATH
  || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const PORT = Number(process.env.CDP_PORT || 9341);
const ROOT = path.join(__dirname, '..');
const DIST = process.env.DIST_DIR || path.join(ROOT, 'frontend-admin', 'dist');
const OUT = process.env.OUT_DIR || path.join(ROOT, '.workbuddy', 'tmp', 'admin-ui');
const PAGE = '/resources/mapping';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let pass = 0;
let fail = 0;
const ok = (m) => { pass++; console.log('  \u2713 ' + m); };
const no = (m) => { fail++; console.log('  \u2717 ' + m); };
const eq = (m, a, b) => (a === b ? ok(m) : no(`${m} —— 实际=[${a}] 期望=[${b}]`));
const truthy = (m, v) => (v ? ok(m) : no(m));

/* ============ mock 数据 ============ */

// ⚠️ 这里必须**有层级**，不能是"一个扁平节点"。
//
// 原来只有 `英语` 一个 `children: []` 的节点，于是「刷新后展开状态保持」
// 那节只能验到**第一层** —— 而真正的 bug 恰好只发生在第二层往下：
// el-tree 懒加载的 loadData 在 resolve 里 `childNodes = []` 再重建，
// 子节点是全新 Node 对象、expanded 一律 false，第一层因为是被直接重载的
// 那个节点所以保得住，第二层往上全被收回去。
// 扁平 mock 让"断言全绿、线上照错"变成了必然。
const CATEGORIES = [
  {
    id: 17, name: '英语', parentId: 0, path: '/17/',
    children: [
      {
        // ⚠️ 名字**故意**不叫「牛津树」/「RAZ」：映射节点的显示名就是远端目录名
        // （正好是这两个词）。同名会让"按名字找节点"的用例选中系统目录，
        // 那边没有「共享」按钮 —— 表现是共享那节莫名其妙报"弹层没打开"。
        id: 18, name: '分级读物', parentId: 17, path: '/17/18/',
        children: [
          { id: 19, name: '第一册', parentId: 18, path: '/17/18/19/', children: [] },
        ],
      },
      { id: 20, name: '绘本', parentId: 17, path: '/17/20/', children: [] },
    ],
  },
  { id: 21, name: '国学经典', parentId: 0, path: '/21/', children: [] },
];
const ACCOUNTS = [
  { id: 1, nickname: '我的夸克', diskType: 'quark' },
  { id: 2, nickname: 'NAS', diskType: 'webdav' },
];
// resourceCount 之和 = 3；dryRun 会说 5（含 2 个历史资源）—— 见文件头说明
const MAPPINGS = [
  {
    id: 1, name: '牛津树-夸克', accountId: 1, remoteDirId: 'dir1', remoteDirName: '牛津树',
    categoryId: 17, recursive: true, enabled: true, resourceCount: 2,
    account: { id: 1, nickname: '我的夸克', diskType: 'quark' },
    category: { id: 17, name: '英语' },
    lastSyncAt: '2026-10-16T02:00:00Z', lastSyncCreated: 2, lastSyncSkipped: 0,
  },
  {
    id: 2, name: 'RAZ-WebDAV', accountId: 2, remoteDirId: 'dir2', remoteDirName: 'RAZ',
    categoryId: 17, recursive: true, enabled: false, resourceCount: 1,
    account: { id: 2, nickname: 'NAS', diskType: 'webdav' },
    category: { id: 17, name: '英语' },
    lastSyncAt: null, lastSyncCreated: 0, lastSyncSkipped: 0,
  },
];
const RESOURCES = [
  { id: 11, mappingId: 1, title: '第01集', ext: 'mp4', mediaType: 'video', totalSize: 10485760, fileId: 'f11' },
  { id: 12, mappingId: 1, title: '第02集', ext: 'mp4', mediaType: 'video', totalSize: 10485760, fileId: 'f12' },
  // 这一条故意做成"已共享"：界面上必须能看出区别（差一个「已共享」角标、
  // 按钮文案也从「共享」变成「链接」）。全都不共享的话，两种形态里
  // 只有一种被测到，另一种出错也看不出来。
  {
    id: 13, mappingId: 2, title: 'RAZ-A', ext: 'pdf', mediaType: 'pdf',
    totalSize: 5242880, fileId: 'f13',
    shareUrl: 'https://pan.quark.cn/s/existing', passcode: 'a1b2',
  },
];

// 设置共享的返回。数字刻意与 RESOURCES 里"未共享"的条数（2 条）一致 ——
// 编一个对不上的数字，界面上那句汇总就成了没人能核对的噪音。
const SHARE_CREATED = {
  dryRun: false, total: 2, created: 2, skipped: 0, failed: 0,
  groups: [{ accountId: 4, accountName: '家长夸克', diskType: 'quark', planned: 2, created: 2, skipped: 0, failed: 0 }],
  failures: [],
  message: '成功 2 条，跳过 0 条，失败 0 条',
};

// 预览与执行都报 5 个资源，其中 2 个是历史资源
const PURGE_STATS = {
  mappings: 2, withResources: true, removedResources: 5, removedScoped: 3, removedLegacy: 2,
};

function mockFor(method, pathAndQuery) {
  const url = new URL('http://x' + pathAndQuery);
  const p = url.pathname;
  if (!p.startsWith('/api/')) return null;

  if (p === '/api/admin/auth/profile') {
    return { code: 0, data: { id: 1, username: 'admin', nickname: '管理员', role: 'admin' } };
  }
  if (p === '/api/admin/categories/tree') return { code: 0, data: CATEGORIES };
  if (p === '/api/admin/categories/icons') return { code: 0, data: [] };
  if (p === '/api/admin/disk/accounts') return { code: 0, data: ACCOUNTS };
  if (p === '/api/admin/mappings' && method === 'GET') return { code: 0, data: MAPPINGS };
  if (p === '/api/admin/mappings' && method === 'DELETE') {
    const dry = url.searchParams.get('dryRun') === '1';
    return {
      code: 0,
      message: dry ? '将解绑全部 2 条映射…' : '已解绑全部 2 条映射…',
      data: { dryRun: dry, ...PURGE_STATS },
    };
  }
  // 顶栏的状态徽标。MainLayout 拿到结果后直接读 status.value.pansouOnline，
  // 兜底返回 null 会让它抛 TypeError（真实踩到过）—— 所以这里给一个真结构。
  //
  // version / commit / buildTime 三个字段是概览页「版本」项的断言依赖：
  // 这一项以前直接显示后端写死的 "1.0.0"（后端从项目第一个提交起没改过），
  // 所以必须有真值可断，否则"页面上有东西"和"显示的是真版本"分不开。
  if (p === '/api/admin/system/status') {
    return {
      code: 0,
      data: {
        pansouOnline: true,
        dbOnline: true,
        version: '1.8.0',
        commit: 'a1b2c3d',
        buildTime: '2026-10-16T06:41:00Z',
      },
    };
  }
  if (p === '/api/admin/resources') {
    const mid = Number(url.searchParams.get('mappingId')) || 0;
    const list = RESOURCES.filter((r) => r.mappingId === mid);
    return { code: 0, data: { list, total: list.length, page: 1, pageSize: 200 } };
  }
  // 单条资源详情：设置共享成功后前端要用它把**真实**的链接/提取码回显出来。
  if (/^\/api\/admin\/resources\/\d+$/.test(p)) {
    const id = Number(p.split('/').pop());
    const r = RESOURCES.find((x) => x.id === id) || RESOURCES[0];
    return { code: 0, data: { ...r, shareUrl: 'https://pan.quark.cn/s/created001', passcode: 'k3m9' } };
  }
  if (p === '/api/admin/shares' && method === 'POST') {
    // 建完之后，这些资源在后续列表里就**该有**分享链接了。
    // 不模拟这一步的话，界面上的"回显链接"永远测不到 ——
    // 而它恰恰是用户唯一能核对的东西。
    // （这里不解析请求体去区分范围：拦截层拿不到 body，
    //   而对这几条断言来说"建完就有了"已经足够。）
    for (const r of RESOURCES) {
      r.shareUrl = 'https://pan.quark.cn/s/created001';
      r.passcode = 'k3m9';
    }
    return { code: 0, data: SHARE_CREATED };
  }
  if (p === '/api/admin/shares/verify') {
    return {
      code: 0,
      data: { checked: 2, found: 2, missing: 0, unshared: 0, items: [], truncated: false },
    };
  }
  // 其余一律给空对象：避免某个次要请求报错、弹全局提示干扰断言。
  // ⚠️ 兜底必须是 `{}` 而不是 `null` —— 页面里到处是 `res.xxx` 这种取值，
  // 给 null 会直接抛 TypeError，而这类异常在真机上只会表现为"某个角标没了"。
  return { code: 0, data: {} };
}

/* ============ 静态服务器（history 模式要 SPA fallback） ============ */

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.woff2': 'font/woff2', '.woff': 'font/woff', '.ico': 'image/x-icon',
  '.json': 'application/json', '.map': 'application/json',
};

function serveStatic(dir) {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const url = new URL(req.url, 'http://x');
      let rel = decodeURIComponent(url.pathname);
      let file = path.join(dir, rel);
      // 目录穿越保护：只服务 dist 目录内的文件
      if (!path.resolve(file).startsWith(path.resolve(dir))) {
        res.writeHead(403).end('forbidden');
        return;
      }
      if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) {
        file = path.join(dir, 'index.html'); // 深链接回 index.html
      }
      res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
      fs.createReadStream(file).pipe(res);
    });
    srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port }));
  });
}

/* ============ 主流程 ============ */

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
  const apiCalls = [];   // { method, pathAndQuery, at }

  const cmd = (method, params) => new Promise((resolve) => {
    const myId = ++id;
    pending.set(myId, resolve);
    ws.send(JSON.stringify({ id: myId, method, params: params || {} }));
  });

  // 用 function 声明，保证消息处理里能引用到下面才定义的 cmd
  function onRequestPaused(params) {
    const { requestId, request } = params;
    const pathAndQuery = request.url.replace(/^https?:\/\/[^/]+/, '');
    const body = mockFor(request.method, pathAndQuery);
    if (body === null) {
      cmd('Fetch.continueRequest', { requestId });
      return;
    }
    apiCalls.push({ method: request.method, pathAndQuery, at: Date.now() });
    cmd('Fetch.fulfillRequest', {
      requestId,
      responseCode: 200,
      responseHeaders: [
        { name: 'Content-Type', value: 'application/json' },
        { name: 'Access-Control-Allow-Origin', value: '*' },
      ],
      // ⚠️ 必须 base64，否则响应体是空的
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

  const shot = async (name, full) => {
    const r = await cmd('Page.captureScreenshot', full
      ? { format: 'png', captureBeyondViewport: true }
      : { format: 'png' });
    if (r.result && r.result.data) {
      fs.writeFileSync(path.join(OUT, name), Buffer.from(r.result.data, 'base64'));
      console.log(`  · 截图 ${name}`);
      return true;
    }
    return false;
  };

  /** 轮询直到表达式为真，返回是否等到。 */
  const waitFor = async (expr, timeout = 15000) => {
    const t0 = Date.now();
    while (Date.now() - t0 < timeout) {
      if (await evaluate(expr)) return true;
      await sleep(200);
    }
    return false;
  };

  await cmd('Page.enable');
  await cmd('Runtime.enable');
  await cmd('Fetch.enable', { patterns: [{ urlPattern: '*/api/*' }] });
  await cmd('Emulation.setDeviceMetricsOverride', {
    width: 1440, height: 900, deviceScaleFactor: 1, mobile: false,
  });

  // 登录态：路由守卫只认 localStorage 里的 token
  await cmd('Page.addScriptToEvaluateOnNewDocument', {
    source: `localStorage.setItem('ls_token','ui-check-token');
             localStorage.setItem('ls_refresh_token','ui-check-refresh');
             localStorage.setItem('ls_profile', JSON.stringify({id:1,username:'admin',nickname:'管理员',role:'admin'}));`,
  });

  console.log(`\n[1] 打开 ${base}${PAGE}`);
  await cmd('Page.navigate', { url: base + PAGE });
  if (!await waitFor(`!!document.querySelector('.mp-bar-actions')`)) {
    no('页面没渲染出工具栏（.mp-bar-actions）');
    await shot('a-fail.png', true);
    chrome.kill(); srv.close(); process.exit(1);
  }
  if (!await waitFor(`document.querySelectorAll('.mp-bar-actions button').length >= 3`)) {
    no('工具栏按钮没出来');
  }

  console.log('[2] 工具栏按钮');
  const buttons = await evaluate(
    `Array.from(document.querySelectorAll('.mp-bar-actions button')).map(b => b.textContent.trim())`
  );
  const joined = (buttons || []).join('|');
  truthy(`有「全部同步」（实际：${joined}）`, joined.includes('全部同步'));
  truthy('有「导出快照」', joined.includes('导出快照'));
  truthy('有「解绑全部」', joined.includes('解绑全部'));

  console.log('[3] 数据渲染');
  const mapRows = await evaluate(`document.querySelectorAll('.mp-detail .el-table__row').length`);
  truthy(`映射明细表有 ${mapRows} 行（期望 2）`, mapRows === 2);

  console.log('[4] 点「解绑全部」→ 二次确认弹窗');
  await evaluate(`
    (Array.from(document.querySelectorAll('.mp-bar-actions button'))
      .find(b => b.textContent.includes('解绑全部')) || {}).click();
    true
  `);
  if (!await waitFor(`!!document.querySelector('.el-message-box')`)) {
    no('点了「解绑全部」但没弹出确认框');
    await shot('b-no-dialog.png', true);
    chrome.kill(); srv.close(); process.exit(1);
  }
  // 弹窗里的数字来自 mock 的 dryRun（5），而 resourceCount 之和是 3 ——
  // 显示 5 才说明它读的是后端预览，不是前端自己加出来的
  const dialogText = await evaluate(
    `document.querySelector('.el-message-box__message').innerText.replace(/\\s+/g,' ')`
  );
  truthy(`弹窗写出映射条数（${(dialogText || '').slice(0, 60)}…）`,
    /将解绑全部\s*2\s*条映射/.test(dialogText || ''));
  truthy('弹窗里的资源数来自后端 dryRun（5，不是前端 resourceCount 之和 3）',
    (dialogText || '').includes('5 个'));
  truthy('弹窗提示了历史资源（说明前端没自己算）', (dialogText || '').includes('历史资源'));
  truthy('弹窗说明了资源是软删除、可恢复', (dialogText || '').includes('软删除'));

  const dryCalls = apiCalls.filter(
    (c) => c.method === 'DELETE' && c.pathAndQuery.includes('dryRun=1')
  );
  eq('先发了一次 dryRun 预览请求', dryCalls.length, 1);

  const dlgShot = await shot('c-confirm-dialog.png', false);

  console.log('[5] 确认词校验：不输 / 输错都不放行');
  // ⚠️ 先断言输入框**真的可见**。这一条是踩出来的：用普通 `ElMessageBox({...})`
  // 时，Element Plus 既不会渲染输入框（渲染与否由 options.showInput 决定，
  // inputType 只是 props），也不会执行 inputValidator（校验分支判断的是
  // `props.boxType === 'prompt'`）。整个弹窗看上去一点问题没有 ——
  // 标题、说明、勾选框、两个按钮俱全 —— 点确定就直接清空整张表。
  //
  // 注意断言的是"可见"而不是"DOM 里有"：容器是 v-show，用 querySelector
  // 永远能查到，写成存在性判断会假通过（第一版就是这么被骗过去的）。
  truthy('确认词输入框可见（少了它 = 确认防护整体失效）',
    await evaluate(`
      (function(){
        var i = document.querySelector('.el-message-box__input input');
        if (!i) return false;
        return !!(i.offsetWidth || i.offsetHeight || i.getClientRects().length);
      })()
    `));
  // ⚠️ 按**文字**找确认按钮，不按 `.el-button--primary` —— Element Plus 的
  // MessageBox 按钮类名随版本变过，用类名选择器在这次的版本上直接是 null，
  // 报出来的错还是"表达式里读 null"，很容易误判成页面坏了。
  const clickConfirm = `
    (function(){
      var b = Array.from(document.querySelectorAll('.el-message-box__btns button'))
        .find(function(x){ return x.textContent.trim() === '确定解绑全部'; });
      if (!b) return false;
      b.click();
      return true;
    })()
  `;
  truthy('弹窗里有「确定解绑全部」按钮', await evaluate(`
    Array.from(document.querySelectorAll('.el-message-box__btns button'))
      .some(function(x){ return x.textContent.trim() === '确定解绑全部'; })
  `));
  await evaluate(clickConfirm);
  await sleep(600);
  truthy('空着点确定 → 弹窗还在（被 inputValidator 拦住）',
    await evaluate(`!!document.querySelector('.el-message-box')`));
  const err = await evaluate(
    `(document.querySelector('.el-message-box__errormsg') || {}).innerText || ''`
  );
  truthy(`弹窗给出校验提示（${err}）`, String(err).includes('全部解绑'));

  const setInput = (v) => evaluate(`
    (function(){
      var i = document.querySelector('.el-message-box__input input');
      if (!i) return false;
      i.value = ${JSON.stringify(v)};
      i.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    })()
  `);
  truthy('确认词能敲进输入框', await setInput('随便写点'));
  await evaluate(clickConfirm);
  await sleep(600);
  truthy('输错也拦住', await evaluate(`!!document.querySelector('.el-message-box')`));

  console.log('[6] 输对 → 执行');
  await setInput('全部解绑');
  await evaluate(clickConfirm);
  const closed = await waitFor(`!document.querySelector('.el-message-box')`, 6000);
  truthy('输对后弹窗关闭（真的下发了解绑）', closed);

  const realCalls = apiCalls.filter(
    (c) => c.method === 'DELETE' && c.pathAndQuery.startsWith('/api/admin/mappings')
      && !c.pathAndQuery.includes('dryRun=1')
  );
  eq('最终打到集合路由 DELETE /api/admin/mappings', realCalls.length, 1);
  // 这条是**同类路由器事故**的护栏：集合被 /mappings/:id 吞掉，
  // 症状是"解绑全部"变成 id 解析失败
  truthy(`请求路径没有多余的 /:id（${(realCalls[0] || {}).pathAndQuery}）`,
    !!realCalls[0] && /^\/api\/admin\/mappings(\?|$)/.test(realCalls[0].pathAndQuery));

  // 页面自己的异常与 console.error 都算失败。这条看着无关，其实很有用：
  // 上面的 mock 只要漏掉某个接口、返回结构缺字段，页面就会在某些角标上抛
  // TypeError —— 那种错**不会**让任何断言失败（按钮照样在、弹窗照样开），
  // 在真机上只表现为"某个数字/角标莫名其妙没了"。
  truthy('全程没有未捕获 JS 异常', exceptions.length === 0);
  if (exceptions.length) console.log('    ' + exceptions.join('\n    '));
  truthy('全程没有 console.error', consoleErrors.length === 0);
  if (consoleErrors.length) console.log('    ' + consoleErrors.join(' | '));
  await shot('d-page-full.png', true);
  if (!dlgShot) no('弹窗截图没拿到');

  // ============ 箭头方向 + 左树展开状态 ============
  //
  // 这两条都是用户 2026-10-16 反馈的，而且**只有真跑才验得出来**：
  //   · 箭头方向 —— 两个按钮都在、都能点，符号写反了照样"功能正常"，
  //     静态看代码也看不出对错（得先想明白"数据从哪流到哪"）；
  //   · 展开状态 —— 它是**交互的结果**，属于"点一下再看"的东西，
  //     任何不驱动 DOM 的断言都够不着。
  console.log('[7] 箭头方向：数据流向必须"右 → 左"');
  const arrowTexts = await evaluate(`
    Array.from(document.querySelectorAll('.mp-arrows .mp-arrow')).map(function(b){
      return (b.textContent || '').trim();
    })
  `);
  eq('挂载按钮是 ←（把网盘内容挂进系统目录）', (arrowTexts || [])[0], '←');
  eq('放回按钮是 →（把系统目录里的东西放回网盘）', (arrowTexts || [])[1], '→');

  console.log('[8] 展开状态保持：刷新后不该把展开的树收回去');
  // 左树是 `.mp-cols` 下的第一个 `.mp-col`（中间是箭头、右边是接入源），
  // 不加 :first-child 会连右树一起数进来。
  const LEFT_EXPANDED = `document.querySelectorAll('.mp-cols > .mp-col:first-child .el-tree-node.is-expanded').length`;
  // 展开着的节点**名单**（只取自身，不把子孙算到父节点头上）。
  // 只数个数是不够的：数目对得上、换了一批节点，一样是"用户的东西被动了"。
  const LEFT_NAMES = `
    Array.from(document.querySelectorAll('.mp-cols > .mp-col:first-child .el-tree-node.is-expanded'))
      .map(n => { const x = n.querySelector(':scope > .el-tree-node__content .mp-node-name'); return x ? x.textContent.trim() : '?'; })
  `;
  const clickExpandIcons = async (max) => evaluate(`
    (function(){
      var col = document.querySelector('.mp-cols > .mp-col:first-child');
      var icons = Array.prototype.slice.call(col.querySelectorAll('.el-tree-node'))
        .filter(function(n){ return !n.classList.contains('is-expanded'); })
        .map(function(n){ return n.querySelector(':scope > .el-tree-node__content .el-tree-node__expand-icon'); })
        .filter(function(i){ return i && !i.classList.contains('is-leaf'); });
      icons.slice(0, ${max}).forEach(function(i){ i.click(); });
      return icons.length;
    })()
  `);

  // ⚠️ 展开**三层**，不是一层。第二层往下才是这个 bug 真正藏身的地方
  // （见 CATEGORIES 上的说明）。只展开一层的话，旧实现的 `afterCount`
  // 也不会归零，这条用例就成了"永远绿"的摆设。
  await clickExpandIcons(1);
  await sleep(700);
  await clickExpandIcons(2);
  await sleep(700);

  const beforeNames = await evaluate(LEFT_NAMES);
  const beforeCount = await evaluate(LEFT_EXPANDED);
  const beforeName = await evaluate(`
    (function(){
      var n = document.querySelector('.mp-cols > .mp-col:first-child .el-tree-node.is-expanded .mp-node-name');
      return n ? n.textContent.trim() : '';
    })()
  `);
  truthy(`左树展开了多层（展开数 ${beforeCount}：${(beforeNames || []).join(' / ')}）`,
    beforeCount >= 2);
  truthy(`确实是展开状态（目录「${beforeName}」）`, !!beforeName);

  // 触发一次刷新。用工具栏最右那个只有图标没有文字的按钮 ——
  // 它走的正是 `loadAll()`，也就是"每挂完一个映射"必然要走的那条路。
  await evaluate(`
    (function(){
      var btns = document.querySelectorAll('.mp-bar-actions button');
      var b = btns[btns.length - 1];
      if (!b) return false;
      b.click();
      return true;
    })()
  `);
  await sleep(1500);

  const afterCount = await evaluate(LEFT_EXPANDED);
  const afterNames = await evaluate(LEFT_NAMES);
  const afterName = await evaluate(`
    (function(){
      var n = document.querySelector('.mp-cols > .mp-col:first-child .el-tree-node.is-expanded .mp-node-name');
      return n ? n.textContent.trim() : '';
    })()
  `);
  // 这里是整条断言的核心：旧实现每次刷新都把展开的第二层往下整批收回去
  // （父节点的 loadData 会 `childNodes = []` 重建），用户就得一级一级重点。
  eq('刷新后展开的节点数不变（不再整棵重建）', afterCount, beforeCount);
  eq('刷新后展开的还是原来那个目录', afterName, beforeName);
  // 名单级断言：个数对得上但"换了一批节点"同样是要抓的。
  const lost = (beforeNames || []).filter((x) => !(afterNames || []).includes(x));
  truthy(`刷新后展开的节点名单一个不少（丢失 ${lost.length} 个${lost.length ? '：' + lost.join(' / ') : ''}）`,
    lost.length === 0);
  await shot('f-tree-expanded.png', true);

  // ============ 概览页：版本项 ============
  //
  // 单列一节的理由：这行版本号以前是**后端写死的 "1.0.0"**
  // （`system.go` 的 gin.H 字面量，从项目第一个提交起没动过），
  // 前端怎么做都救不回来 —— 而"线上到底跑的哪一版"恰恰是排查时
  // 第一个要回答的问题。所以断言必须落在**具体值**上：
  // 只断言"这一项存在"的话，1.0.0 和 1.8.0 都能过。
  console.log('[9] 概览页：版本 / 构建信息');
  await cmd('Page.navigate', { url: base + '/' });
  if (!await waitFor(`!!document.querySelector('.el-descriptions .ver')`)) {
    no('概览页「系统状态」里没有版本项（.ver 没渲染出来）');
    await shot('e-dashboard-fail.png', true);
  } else {
    const ver = ((await evaluate(
      `document.querySelector('.el-descriptions .ver').textContent`
    )) || '').trim();
    eq('版本项显示后端给的版本号', ver, 'v1.8.0');
    truthy('版本号不是那个写死的 1.0.0', !ver.includes('1.0.0'));

    // 构建信息（短 commit + 构建时刻）。取不到时后端字段为空、
    // 前端整段不渲染 —— 那是本地 go run 的正常形态，不是错误；
    // 但 mock 里给了值，所以这里必须真渲染出来。
    const build = ((await evaluate(
      `(document.querySelector('.el-descriptions .build') || {}).textContent || ''`
    )) || '').trim();
    truthy(`构建信息渲染出来了（${build}）`, build.includes('a1b2c3d'));
  }
  await shot('e-dashboard.png', true);

  // ============ 设置共享 ============
  //
  // 这一节的必要性：整条"孩子端加速播放"链路上，后台唯一要做的事就是
  // **给资源建一条分享链接**。而这一步的失败方式极其隐蔽 ——
  // 分享建成了、链接也能打开，但孩子端是按 fileId 在分享里挑文件的，
  // 对不上时它只会说"分享里没有找到指定的文件"，后台看起来一切正常。
  // 所以这里必须真点、真渲染，并且断言**回显出来的链接不是空的**：
  // 只断言"弹层打开了"的话，一个永远显示空白输入框的坏法也能过。
  console.log('[10] 设置共享：入口 → 弹层 → 回显链接与提取码');
  await cmd('Page.navigate', { url: base + '/resources/mapping' });
  if (!await waitFor(
    `!!document.querySelector('.mp-cols > .mp-col:first-child .el-tree-node__expand-icon')`
  )) {
    no('映射页没渲染出来，后面的共享断言全部跳过');
  } else {
    // 展开「英语」→ 两条映射节点出现
    await evaluate(`
      (function(){
        var ic = document.querySelector('.mp-cols > .mp-col:first-child .el-tree-node__expand-icon');
        if (ic) ic.click();
      })()
    `);
    await sleep(700);

    // 按名字点某个映射节点上的那颗按钮。
    // ⚠️ 必须用 :scope > 逐层限定：el-tree 的节点是嵌套的，
    // 直接 node.querySelector('.mp-node-name') 会先命中**子节点**，
    // 于是"点牛津树"变成"点了它下面的第一条资源"。
    //
    // 找不到时把**实际看到的节点名**一起返回：这类"选择器没命中"
    // 的断言如果只说"没点到"，排查的人只能靠猜。
    const clickNodeBtn = (name, sel) => `
      (function(){
        var nodes = document.querySelectorAll('.mp-cols > .mp-col:first-child .el-tree-node');
        var seen = [];
        var nameMatched = false;
        for (var i = 0; i < nodes.length; i++) {
          var nm = nodes[i].querySelector(':scope > .el-tree-node__content .mp-node-name');
          if (!nm) continue;
          var t = nm.textContent.trim();
          seen.push(t);
          if (t === ${JSON.stringify(name)}) {
            var b = nodes[i].querySelector(':scope > .el-tree-node__content ' + ${JSON.stringify(sel)});
            // ⚠️ 名字撞车是**正常**的：系统目录和"挂上来的远端目录"可能同名。
            // 所以"名字对上但没这个按钮"不能直接判失败，要继续往后找 ——
            // 直接 return 会把"选错了同名节点"误报成"按钮不存在"。
            if (b) { b.click(); return { ok: true, seen: seen }; }
            nameMatched = true;
          }
        }
        return { ok: false, why: nameMatched
          ? '同名节点找到了但没有 ' + ${JSON.stringify(sel)}
          : '没找到名为 ' + ${JSON.stringify(name)} + ' 的节点', seen: seen };
      })()
    `;

    truthy('映射节点上有「共享」入口', await evaluate(`
      document.querySelectorAll('.mp-cols > .mp-col:first-child .mp-act-share').length >= 2
    `));

    const clickRes = await evaluate(clickNodeBtn('牛津树', '.mp-act-share'));
    truthy(
      '点开了「牛津树」这条映射的共享',
      clickRes && clickRes.ok
    );
    if (!clickRes || !clickRes.ok) {
      console.log(`      · 实际看到的节点：${JSON.stringify((clickRes && clickRes.seen) || [])}`);
      console.log(`      · 原因：${(clickRes && clickRes.why) || '未知'}`);
    }
    if (!await waitFor(`!!document.querySelector('.sh-body')`)) {
      no('共享弹层没打开');
    } else {
      const title = ((await evaluate(
        `(document.querySelector('.el-dialog__title') || {}).textContent || ''`
      )) || '').trim();
      truthy(`弹层标题带上了目标名（${title}）`, title.includes('设置共享') && title.includes('牛津树'));

      const radios = await evaluate(`
        Array.from(document.querySelectorAll('.sh-form .el-radio-button__inner')).map(function(e){
          return e.textContent.trim();
        })
      `);
      eq('有效期只给已验证的两档', (radios || []).join('/'), '30 天/永久');
      truthy('有自动生成提取码的输入框', await evaluate(
        `!!document.querySelector('.sh-form input[placeholder="留空自动生成"]')`
      ));
      truthy('映射批量时有「重新生成」开关', await evaluate(`
        (document.body.textContent || '').indexOf('重新生成') >= 0
      `));

      // 创建
      await evaluate(`
        (function(){
          var btns = document.querySelectorAll('.el-dialog__footer button');
          var b = btns[btns.length - 1];
          if (b) b.click();
        })()
      `);
      if (!await waitFor(`!!document.querySelector('.sh-result input')`, 6000)) {
        no('创建后没有回显结果区');
      } else {
        const link = ((await evaluate(`document.querySelector('.sh-result input').value`)) || '');
        const code = ((await evaluate(`
          (document.querySelectorAll('.sh-result input')[1] || {}).value || ''
        `)) || '');
        // 这两条是本节的核心：界面上必须给出**真的**链接与提取码。
        truthy(`回显了分享链接（${link}）`, link.indexOf('pan.quark.cn') >= 0);
        truthy(`回显了提取码（${code}）`, !!code);

        truthy('有「验证共享」入口', await evaluate(`
          (document.body.textContent || '').indexOf('验证共享') >= 0
        `));
      }
      await shot('g-share-dialog.png', true);
    }
  }

  console.log(`\n通过 ${pass}，失败 ${fail}`);
  console.log(`截图目录：${OUT}`);

  ws.close();
  chrome.kill();
  srv.close();
  process.exit(fail ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });
