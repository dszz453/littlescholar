# API 接口契约

> Base URL（后台）：`/api/admin`
> Base URL（C 端）：`/api/app`
> 统一响应格式见文末

---

## 一、认证 Auth

### 后台登录
```
POST /api/admin/auth/login
Body: { "username": "admin", "password": "xxx" }
Resp: { "code":0, "data": { "token":"...", "refreshToken":"...", "expiresAt": 1234567890, "user": {...} } }
```

### C 端登录（孩子账号）
```
POST /api/app/auth/login
Body: { "username": "xiaoming", "password": "xxx" }
Resp: 同上
```

### 刷新 Token
```
POST /api/auth/refresh
Body: { "refreshToken": "..." }
```

### 当前用户信息
```
GET /api/admin/auth/profile      (后台)
GET /api/app/auth/profile        (C端)
Resp: { "code":0, "data": { "id":1, "username":"admin", "role":"admin", "grants":[...] } }
```

---

## 二、目录管理 Categories

### 获取目录树
```
GET /api/admin/categories/tree?withResourceCount=true
Resp: {
  "code": 0,
  "data": [
    {
      "id": 1, "parentId": null, "name": "乐学英语",
      "icon": "english", "level": 1, "path": "/1/",
      "resourceCount": 42, "sortOrder": 0,
      "children": [ { "id": 11, "name": "新概念英语", "children": [...] } ]
    }
  ]
}
```

### 创建目录
```
POST /api/admin/categories
Body: {
  "parentId": 1,          // null 表示根
  "name": "新概念英语",
  "icon": "book",
  "description": "新概念英语第一册~第四册",
  "sortOrder": 0
}
Resp: { "code":0, "data": { "id": 11 } }
```

### 更新目录
```
PUT /api/admin/categories/:id
Body: { "name":"...", "icon":"...", "description":"...", "sortOrder":0, "isPublic": false }
```

### 移动目录
```
PUT /api/admin/categories/:id/move
Body: { "targetParentId": 2, "sortOrder": 0 }
```

### 删除目录
```
DELETE /api/admin/categories/:id?cascade=true
cascade=true 时递归删除子目录（需二次确认）
```

### 可访问的目录列表（C 端，按权限过滤）
```
GET /api/app/categories/tree
Resp: 只返回该用户有权限的目录子树
```

### 可用图标列表
```
GET /api/admin/categories/icons
Resp: { "code":0, "data": ["english","chinese","math","story","music","book","star","science","art","sport", ...] }
```

---

## 三、资源管理 Resources

### 分页查询资源
```
GET /api/admin/resources?categoryId=1&keyword=新概念&diskType=quark&page=1&pageSize=20
Resp: {
  "code":0,
  "data": {
    "total": 100,
    "list": [
      {
        "id": 1, "title":"新概念英语第一册",
        "categoryId": 11, "categoryName":"新概念英语",
        "diskType": "quark",
        "shareUrl": "https://pan.quark.cn/s/xxx",
        "passcode": "1234",
        "fileCount": 30, "totalSize": 1073741824,
        "cover": "https://...",
        "status": "alive",        // alive / dead / unknown
        "lastCheckedAt": "2026-10-12T10:00:00Z",
        "playUrl": "/api/play/1",
        "createdAt": "..."
      }
    ]
  }
}
```

### 资源详情
```
GET /api/admin/resources/:id
```

### 新增资源（手动）
```
POST /api/admin/resources
Body: {
  "categoryId": 11,
  "title": "新概念英语第一册",
  "diskType": "quark",
  "shareUrl": "https://pan.quark.cn/s/xxx",
  "passcode": "1234",
  "cover": "",
  "description": ""
}
```

### 批量删除
```
DELETE /api/admin/resources/batch
Body: { "ids": [1,2,3] }
```

### 移动到目录
```
PUT /api/admin/resources/move
Body: { "ids":[1,2], "categoryId": 12 }
```

### 批量存活检测
```
POST /api/admin/resources/check-alive
Body: { "ids": [1,2,3] }
Resp: { "code":0, "data": { "taskId": "..." } }   // 异步
```

### 从搜索结果导入
```
POST /api/admin/resources/import
Body: {
  "categoryId": 11,
  "items": [
    { "title":"...", "diskType":"quark", "shareUrl":"...", "passcode":"", "note":"..." }
  ]
}
Resp: { "code":0, "data": { "created": 3, "skipped": 1 } }
```

---

## 四、资源搜索 Search（对接 pansou）

### 聚合搜索
```
POST /api/admin/search
Body: {
  "keyword": "新概念英语",
  "diskTypes": ["quark","baidu","uc","tianyi"],   // 可选，不传则全部
  "res": "merge",                                  // merge / all
  "src": "all",                                    // tg / plugin / all
  "checkAlive": true,                              // 是否顺带做存活检测
  "page": 1,
  "pageSize": 20
}
Resp: {
  "code":0,
  "data": {
    "keyword": "新概念英语",
    "total": 128,
    "costMs": 1200,
    "list": [
      {
        "url": "https://pan.quark.cn/s/xxx",
        "password": "1234",
        "note": "新概念英语全四册",
        "diskType": "quark",
        "datetime": "2026-09-01T10:00:00Z",
        "source": "plugin:qupansou",
        "alive": "ok",          // ok/bad/locked/unsupported/uncertain
        "aliveSummary": "链接有效"
      }
    ]
  }
}
```

### 仅存活检测
```
POST /api/admin/search/check
Body: {
  "items": [
    { "diskType":"quark", "url":"https://pan.quark.cn/s/xxx", "password":"1234" }
  ]
}
Resp: { "code":0, "data": { "results": [ { "url":"...", "state":"ok", "summary":"链接有效" } ] } }
```

### 健康检查（pansou 服务）
```
GET /api/admin/search/health
Resp: { "code":0, "data": { "pansouOnline": true, "pancheckOnline": true, "plugins": 16 } }
```

---

## 五、转存 Transfer

### 创建转存任务
```
POST /api/admin/transfer
Body: {
  "shareUrl": "https://pan.quark.cn/s/xxx",
  "passcode": "1234",
  "diskType": "quark",
  "diskAccountId": 1,        // 用哪个网盘账号
  "targetDirId": "/folder/xxx",   // 网盘内的目标目录ID
  "categoryId": 11,          // 转存成功后，资源归属的系统目录
  "title": "新概念英语第一册"  // 可选，不传则自动取分享标题
}
Resp: { "code":0, "data": { "taskId": 1001 } }
```

### 转存任务列表
```
GET /api/admin/transfer/tasks?status=pending&page=1&pageSize=20
Resp: {
  "code":0,
  "data": {
    "total": 10,
    "list": [
      {
        "id": 1001,
        "title": "新概念英语第一册",
        "diskType": "quark",
        "shareUrl": "...",
        "sharePassword": "1234",
        "targetDir": "/我的资源/乐学英语",
        "status": "success",       // pending/running/success/failed
        "progress": 100,
        "message": "转存成功，共 30 个文件",
        "createdAt": "...",
        "finishedAt": "..."
      }
    ]
  }
}
```

### 重试失败任务
```
POST /api/admin/transfer/tasks/:id/retry
```

### 取消任务
```
POST /api/admin/transfer/tasks/:id/cancel
```

### 批量转存（从搜索结果）
```
POST /api/admin/transfer/batch
Body: {
  "diskAccountId": 1,
  "targetDirId": "/folder/xxx",
  "categoryId": 11,
  "items": [ { "shareUrl":"...", "passcode":"", "diskType":"quark", "title":"..." } ]
}
Resp: { "code":0, "data": { "taskIds": [1001, 1002] } }
```

---

## 六、网盘账号 Disk Accounts

### 账号列表
```
GET /api/admin/disk/accounts
Resp: {
  "code":0,
  "data": [
    {
      "id": 1, "diskType":"quark", "nickname":"我的夸克",
      "avatar": "https://...",
      "status": "valid",         // valid / invalid / expired
      "capacityUsed": 10737418240,
      "capacityTotal": 10995116277760,
      "lastCheckAt": "...",
      "createdAt": "..."
    }
  ]
}
```

### 获取扫码二维码
```
POST /api/admin/disk/accounts/qr
Body: { "diskType": "quark" }
Resp: {
  "code":0,
  "data": {
    "sessionId": "sess_xxx",
    "qrContent": "https://...",       // 二维码内容（前端生成图）
    "qrImage": "data:image/png;base64,...",  // 或直接给图片
    "expiresIn": 180
  }
}
```

### 轮询扫码状态
```
GET /api/admin/disk/accounts/qr/:sessionId
Resp: {
  "code":0,
  "data": {
    "status": "waiting",     // waiting / scanned / confirmed / expired / failed
    "accountId": null        // confirmed 时返回新建的账号ID
  }
}
```

### 手动添加（粘贴 Cookie）
```
POST /api/admin/disk/accounts/manual
Body: { "diskType":"baidu", "cookie":"BDUSS=xxx; ...", "nickname":"我的百度" }
```

### 校验账号
```
POST /api/admin/disk/accounts/:id/validate
```

### 更新 Cookie
```
PUT /api/admin/disk/accounts/:id/cookie
Body: { "cookie": "..." }
```

### 删除账号
```
DELETE /api/admin/disk/accounts/:id
```

### 浏览网盘目录（用于选转存目标）
```
GET /api/admin/disk/accounts/:id/browse?dirId=0
Resp: {
  "code":0,
  "data": {
    "dirId": "0", "dirName": "根目录",
    "items": [ { "id":"...", "name":"乐学英语", "isDir":true, "size":0 } ]
  }
}
```

### 新建网盘目录
```
POST /api/admin/disk/accounts/:id/mkdir
Body: { "parentId": "0", "name": "乐学英语" }
Resp: { "code":0, "data": { "dirId": "..." } }
```

---

## 七、用户管理 Users

### 用户列表
```
GET /api/admin/users?keyword=&page=1&pageSize=20
Resp: {
  "code":0,
  "data": {
    "total": 5,
    "list": [
      {
        "id": 2, "username":"xiaoming", "nickname":"小明",
        "role": "kid", "grade":"三年级",
        "status": 1,
        "expiresAt": "2027-01-01T00:00:00Z",
        "grantCount": 3,
        "lastLoginAt": "...",
        "createdAt": "..."
      }
    ]
  }
}
```

### 创建用户
```
POST /api/admin/users
Body: {
  "username": "xiaoming",
  "password": "xxx",
  "nickname": "小明",
  "grade": "三年级",
  "expiresAt": "2027-01-01T00:00:00Z",
  "grants": [
    { "categoryId": 1, "canDownload": true, "expiresAt": "2027-01-01T00:00:00Z" },
    { "categoryId": 3, "canDownload": false, "expiresAt": null }
  ]
}
```

### 更新用户
```
PUT /api/admin/users/:id
Body: { "nickname":"...", "grade":"...", "status":1, "expiresAt":"..." }
```

### 重置密码
```
PUT /api/admin/users/:id/password
Body: { "password": "newpass" }
```

### 设置目录授权
```
PUT /api/admin/users/:id/grants
Body: {
  "grants": [
    { "categoryId": 1, "canDownload": true, "expiresAt": "..." }
  ]
}
```

### 删除用户
```
DELETE /api/admin/users/:id
```

---

## 八、播放 Play

### 获取播放信息（后台 + C端通用）
```
GET /api/play/:resourceId
Header: Authorization
Resp: {
  "code":0,
  "data": {
    "resourceId": 1,
    "title": "新概念英语第一册 01.mp4",
    "mediaType": "video",        // video / audio / pdf / image
    "playMode": "hls",           // hls / direct / proxy
    "url": "https://cdn.example.com/hls/1/index.m3u8",
    "poster": "https://...",
    "duration": 1800,
    "lastPosition": 320,         // 断点续播位置（秒）
    "subtitle": null,
    "qualities": [ { "label":"720p", "url":"..." } ]
  }
}
```

### 上报播放进度
```
POST /api/app/play/:resourceId/progress
Body: { "position": 320, "duration": 1800, "playedSeconds": 15 }
```

### 播放完成
```
POST /api/app/play/:resourceId/finish
Body: { "position": 1800 }
```

### 获取播放列表（某资源下的文件）
```
GET /api/app/play/:resourceId/playlist
Resp: { "code":0, "data": [ { "id":1, "name":"01.mp4", "duration":1800, "playUrl":"..." } ] }
```

---

## 九、学习记录与 AI 分析 Study & AI

### 学习统计概览
```
GET /api/admin/study/overview?userId=2&startDate=2026-10-01&endDate=2026-10-12
Resp: {
  "code":0,
  "data": {
    "totalMinutes": 480,
    "totalDays": 8,
    "byCategory": [ { "categoryId":1, "categoryName":"乐学英语", "minutes":300 } ],
    "byDay": [ { "date":"2026-10-12", "minutes": 60 } ],
    "byHour": [ ... ],
    "finishedResources": 12,
    "inProgressResources": 3
  }
}
```

### 学习记录明细
```
GET /api/admin/study/records?userId=2&page=1&pageSize=20
```

### 生成 AI 学情报告
```
POST /api/admin/study/analyze
Body: {
  "userId": 2,
  "startDate": "2026-10-01",
  "endDate": "2026-10-12",
  "aiConfigId": 1,          // 用哪套 AI 配置
  "extraPrompt": "重点关注英语学习"   // 可选
}
Resp: { "code":0, "data": { "reportId": 10 } }
```

### 获取 AI 报告
```
GET /api/admin/study/reports/:id
Resp: {
  "code":0,
  "data": {
    "id": 10, "userId":2,
    "periodStart":"...", "periodEnd":"...",
    "summary": "本周期孩子累计学习 8 小时...",
    "highlights": ["英语学习投入最多", "..."],
    "weaknesses": ["数学思维练习偏少"],
    "suggestions": ["建议每天固定 30 分钟英语听读", "..."],
    "rawContent": "AI 返回的完整 markdown",
    "createdAt": "..."
  }
}
```

### AI 智能体问答（C 端）
```
POST /api/app/ai/chat
Body: {
  "message": "三年级的孩子适合学什么英语？",
  "context": { "grade": "三年级", "historyResourceIds": [1,2] },
  "sessionId": "chat_xxx"
}
Resp: {
  "code":0,
  "data": {
    "reply": "对于三年级的孩子，建议从...\n推荐资源：\n1. 新概念英语第一册",
    "recommendResources": [ { "id":1, "title":"新概念英语第一册", "categoryName":"乐学英语" } ],
    "sessionId": "chat_xxx"
  }
}
```

### AI 配置列表
```
GET /api/admin/ai/configs
Resp: {
  "code":0,
  "data": [
    {
      "id": 1, "name":"OpenAI GPT-4o",
      "provider": "openai",         // openai / deepseek / qwen / ollama / custom
      "baseUrl": "https://api.openai.com/v1",
      "model": "gpt-4o",
      "apiKeyMasked": "sk-****abcd",
      "temperature": 0.7,
      "isDefault": true,
      "status": "valid"
    }
  ]
}
```

### 新增 AI 配置
```
POST /api/admin/ai/configs
Body: {
  "name": "DeepSeek",
  "provider": "deepseek",
  "baseUrl": "https://api.deepseek.com/v1",
  "apiKey": "sk-xxx",
  "model": "deepseek-chat",
  "temperature": 0.7,
  "maxTokens": 4096,
  "isDefault": true
}
```

### 测试 AI 连通性
```
POST /api/admin/ai/configs/:id/test
Resp: { "code":0, "data": { "ok": true, "latencyMs": 800, "reply": "pong" } }
```

### 更新 / 删除 AI 配置
```
PUT    /api/admin/ai/configs/:id
DELETE /api/admin/ai/configs/:id
```

---

## 十、系统配置 System

### 获取系统配置
```
GET /api/admin/system/config
Resp: {
  "code":0,
  "data": {
    "pansouUrl": "http://pansou:8888",
    "pancheckUrl": "http://pancheck:6080",
    "enableTranscode": true,
    "transcodeConcurrency": 2,
    "downloadAccelerate": true,
    "downloadConcurrency": 16,
    "playModeDefault": "proxy"    // hls / direct / proxy
  }
}
```

### 更新系统配置
```
PUT /api/admin/system/config
```

### 系统状态
```
GET /api/admin/system/status
Resp: {
  "code":0,
  "data": {
    "version": "1.0.0",
    "uptime": 3600,
    "dbOnline": true,
    "redisOnline": true,
    "pansouOnline": true,
    "diskUsed": 10737418240,
    "transcodeQueue": 2,
    "transferQueue": 0
  }
}
```

### 转码任务列表
```
GET /api/admin/system/transcode/tasks
```

---

## 附：统一响应格式

```json
{
  "code": 0,
  "message": "success",
  "data": {}
}
```

| code | 含义 |
|---|---|
| 0 | 成功 |
| 400 | 参数错误 |
| 401 | 未登录 / Token 失效 |
| 403 | 无权限 |
| 404 | 资源不存在 |
| 429 | 请求过于频繁 |
| 500 | 服务器内部错误 |

分页统一格式：
```json
{ "code":0, "data": { "total": 100, "page": 1, "pageSize": 20, "list": [] } }
```

错误响应：
```json
{ "code": 403, "message": "该账号已过期或无该目录的访问权限", "data": null }
```
