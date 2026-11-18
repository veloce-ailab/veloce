# 令牌授权（无 client id / client secret）

本文描述 Veloce 新增的第三方应用令牌授权机制：外部应用无需注册 `client_id`、
无需持有 `client_secret`，即可在用户同意后拿到用户的一个 API 令牌（`sk-...`）。
整体流程参考 PKCE：应用自己生成一个高熵 `state`，用它开启授权，**回调只带回同一个
`state`**，应用再用这个 `state` 换取真正的令牌。

## 1. 设计目标

| 目标 | 做法 |
| --- | --- |
| 不需要注册应用 | 没有 `client_id` / `client_secret`，也没有应用后台配置项 |
| 令牌不出现在跳转链接里 | 回调只带 `state`，令牌由应用服务端用 `state` 兑换 |
| 用户明确知情 | 授权页写明应用名、回调域名，并警告「该应用能拿到你所选令牌的完整权限」 |
| 用户可复用现有令牌 | 授权页可直接选择一个已有令牌 |
| 创建令牌体验一致 | 授权页的「创建新令牌」直接复用令牌页的同一个弹窗组件，字段与校验完全一致 |
| 短生命周期、一次性 | 授权结果只在内存里保留 5 分钟，兑换一次即作废 |

## 2. 参与者

- **应用（client）**：任意第三方程序，有自己的回调地址（HTTPS 或自有 scheme）。
- **授权页（前端 `/authorize`）**：Veloce Web 的页面，需要用户已登录。
- **后端（`/api/user/oauth/authorize`、`/api/oauth/token`）**：签发与兑换授权。

## 3. 完整流程

```
应用                          浏览器/用户                      Veloce 后端
 |                               |                              |
 | 1. 生成 state（32+ 字符随机） |                              |
 | 2. 打开 /authorize?state=&    |                              |
 |    redirect_uri=&app_name=    |                              |
 |------------------------------>|                              |
 |                               | 3. 未登录？→ /login?return_to=…
 |                               | 4. 选择已有令牌或新建令牌      |
 |                               |    （新建走 POST /api/user/api-keys）
 |                               | 5. 点击「授权并返回应用」      |
 |                               |----------------------------->| POST /api/user/oauth/authorize
 |                               |                              |   {state, redirect_uri, api_key_id}
 |                               |<-----------------------------| {redirect_url, expires_in}
 |                               | 6. 302/跳转 redirect_url?state=…（不含令牌）
 |<------------------------------|                              |
 | 7. 服务端 POST /api/oauth/token {state}                       |
 |-------------------------------------------------------------->| 校验并消费 grant
 |<--------------------------------------------------------------| {token, token_id, name, ...}
```

关键点在第 3 步和第 6 步之间：令牌在这之前从未离开 Veloce；回调只携带应用自己的
`state`，因此令牌不会进入浏览器历史、Referer 头或第三方访问日志。

## 4. 参数规范

### 4.1 授权请求（应用 → 授权页）

```
GET /authorize?state=<state>&redirect_uri=<callback>&app_name=<可选显示名>
```

| 参数 | 必填 | 规则 |
| --- | --- | --- |
| `state` | 是 | `^[A-Za-z0-9._~-]{32,256}$`，由应用生成，**必须高熵随机**（建议 32 字节随机数的 base64url） |
| `redirect_uri` | 是 | 绝对地址：`https://…`；本地开发可用 `http://localhost`、`http://127.0.0.1`、`http://[::1]`；或自有协议 `com.example.app://callback`（scheme 必须含点）。禁止 `javascript:`/`data:`/`file:` 等，禁止带 fragment、禁止带用户名密码 |
| `app_name` | 否 | 展示名，去控制字符后截断到 64 字符 |

`state` 长度下限 32 是硬性的：它同时是「关联值」和「兑换凭据」，短了就能被猜。

### 4.2 授权（授权页 → 后端，需要登录）

```http
POST /api/user/oauth/authorize
Authorization: Bearer <用户 JWT>
Content-Type: application/json

{"state": "<同上>", "redirect_uri": "<同上>", "api_key_id": 12}
```

成功（200）：

```json
{"redirect_url": "https://app.example.com/callback?state=<state>", "expires_in": 300}
```

`redirect_url` 由后端用规范化后的 `redirect_uri` 拼上 `state` 生成，前端只负责跳转，
避免客户端自行拼接导致参数被篡改。`redirect_uri` 原有 query 会保留。

拒绝时后端不参与：前端直接跳回 `redirect_uri`，附带
`error=access_denied&state=<state>`（客户端已按同样规则校验过回调地址）。

### 4.3 兑换令牌（应用服务端 → 后端，公开接口）

```http
POST /api/oauth/token
Content-Type: application/json

{"state": "<同一个 state>"}
```

成功（200）：

```json
{
  "token": "sk-...",
  "token_id": 12,
  "name": "我的应用",
  "key_prefix": "sk-abc12...wxyz",
  "expires_at": null,
  "api_base_url": "https://veloce.example.com/v1"
}
```

失败（400）：`{"error": "authorization is unknown, expired, or already redeemed"}`。
未知、已过期、已兑换三种情况返回同一句错误，避免被用来探测。

## 5. 服务端实现

| 文件 | 内容 |
| --- | --- |
| `internal/service/token_auth.go` | 参数校验、回调拼接、授权记录的**内存**存储（一次性、TTL 5 分钟） |
| `internal/api/token_auth.go` | `TokenAuthAPI.Authorize` / `TokenAuthAPI.Exchange` 两个处理函数 |
| `internal/app/app.go` | 路由注册与限流（`token-auth-authorize-ip`、`token-auth-exchange-ip`，各 20 次/分钟/IP） |

要点：

- 授权记录只存在内存（`map[state]TokenAuthGrant`），进程重启即失效，不落库。
- 写入时先清理过期项；条目数上限 4096，超出时返回 `too many pending authorizations`。
- 兑换时**再次校验**令牌状态（启用、未过期、可导出原文），因为授权与兑换之间令牌
  可能被停用或删除。
- 两次操作都写审计日志（`token_authorize_success` / `token_exchange_success` /
  `token_exchange_failed`），带着用户、令牌 ID 与回调地址。
- 兑换接口不需要客户端凭证，所以只接受 POST + 限流，`state` 本身就是能力凭据。

### 5.1 前端实现

| 文件 | 内容 |
| --- | --- |
| `src/pages/TokenAuthorize.tsx` | 授权页：账号信息、信任警告、令牌选择、创建入口、授权/取消/换账号 |
| `src/components/api-key-create-dialog.tsx` | 从令牌页抽出的共享创建弹窗，令牌页与授权页都用它，保证字段、校验、创建后展示原文的行为完全一致 |
| `src/lib/token-authorize.ts` | 入参解析与前端侧的同样规则校验、拒绝回调拼接 |
| `src/App.tsx` / `src/pages/Login.tsx` | 新增 `/authorize` 路由；登录后支持 `return_to`，让未登录用户能从授权页跳去登录再回来 |

授权页在未登录时不会丢参数：`/login?return_to=/authorize%3Fstate%3D…%26redirect_uri%3D…`。

## 6. 安全分析与已知限制

**已经做的**

- 令牌只出现在兑换响应里，且兑换是应用服务端到 Veloce 的直接调用。
- `state` 一次性、5 分钟有效、长度 ≥32，未知/过期/已用不可区分。
- 回调地址严格校验，禁止 `javascript:`/`data:`/fragment/userinfo，`http` 仅限回环地址。
- 授权页显式提示应用名、回调域名、"该应用能拿到令牌"，并且默认不选任何东西就让用户
  明确点击；不认识的场景提供「取消授权」。
- 兑换时二次校验令牌是否仍启用、未过期。
- 审计日志留痕，便于事后追踪。

**需要知道的**

1. **`state` 既是关联值也是兑换凭据**，这是「不要 client secret」直接带来的取舍：拿到
   `state` 的人就能兑换令牌。因此 `state` 必须由应用用 CSPRNG 生成，且每次授权都用新
   值；应用不应把它写进日志、URL 收藏或分析系统。
2. **回调劫持**：如果应用使用通用 scheme（而不是只有自己能接收的 scheme），或
   `redirect_uri` 落在了不受控的域名上，攻击者可能截获 `state`。这是无 `client_secret`
   流程的固有风险，建议应用在回调中再次确认是自己发起的流程（例如对照自己的会话），
   或改用「注册应用 + PKCE verifier」的方案。
3. **钓鱼**：任何人都可以构造一个授权链接把 `app_name` 写成任意名字。用户只能靠回调
   域名与警告文案判断。生产环境若要更强约束，应引入应用白名单或让用户确认后再展示。
4. **多节点/重启**：授权记录在内存里，多节点部署时兑换必须落到同一节点（或后续改为
   Redis/数据库存储）。重启会丢掉 5 分钟内未兑换的授权，用户需要重新授权。
5. **`api_base_url`** 按请求头（`X-Forwarded-Proto`/`X-Forwarded-Host`）推导，反向代理
   配置错误时会给出错误地址；应用应允许用户手工覆盖。
6. **没有撤销通知**：应用拿到令牌后，用户在令牌页停用/删除该令牌即等于撤销；应用会
   在下一次调用时收到 401。

### 6.2 跨域（CORS）

拿到令牌之后，客户端通常还要从自己的源（浏览器页面、Capacitor 的 `https://localhost`
WebView）直接调用 `<站点>/v1/...`，这些请求都跨域，因此网关内置了 CORS 中间件
（`internal/middleware/cors.go`）：

- 覆盖 `/v1/*`（含 Claude Messages、OpenAI 兼容、模型列表等）与 `POST /api/oauth/token`；
- 预检请求由 `r.OPTIONS("/v1/*any")`、`r.OPTIONS("/api/oauth/*any")` 直接返回 204；
- 允许的请求头包括 `Authorization`、`x-api-key`、`anthropic-version`、`anthropic-beta` 等；
- 默认 `Access-Control-Allow-Origin: *`：这些接口只用 Bearer 令牌认证、不使用 Cookie，
  通配符不会把浏览器会话暴露给第三方站点；
- 需要收紧时设置环境变量 `CORS_ALLOWED_ORIGINS`（逗号分隔的精确 origin），未命中的来源
  不会再收到跨域响应头。

### 6.3 参考客户端：veloce-aiapp

`veloce-aiapp`（Capacitor 打包的聊天客户端）就是这个流程的完整实现：
设置里填写站点地址 → 打开 `/authorize` → 回调只带 `state` → 用 `state` 换取令牌 →
自动填入 API Key 并把接口地址设为 `<站点>/v1`。

回调地址按运行形态区分：

| 运行形态 | redirect_uri | 说明 |
| --- | --- | --- |
| 原生应用（Android/iOS） | `com.veloce.aiapp://auth/callback` | 自定义协议，Android 用 intent-filter、iOS 用 CFBundleURLSchemes 注册；授权页在系统浏览器打开，回调直接把应用拉回前台 |
| 浏览器 / PWA | `<应用地址>/auth-callback.html` | 静态回调页，兑换成功后跳回首页 |

注意自定义协议的 scheme 必须带点（反向域名），服务端只接受这种形态，
避免与应用商店里的通用 scheme 冲突。

### 6.1 与 PKCE 的对比

| 维度 | OAuth PKCE | 本机制 |
| --- | --- | --- |
| 应用注册 | 需要 `client_id` | 不需要 |
| 中间凭据 | 授权码 `code` | 应用自己生成的 `state` |
| 兑换凭证 | `code_verifier` | 同上（`state` 一把钥匙） |
| 回调携带 | `code` + `state` | 只有 `state` |
| 回调泄露影响 | 没有 verifier 拿不到令牌 | 直接可兑换（见限制 1、2） |

如果想进一步收紧，可以在不破坏现有流程的前提下增加可选参数：授权请求里带
`code_challenge`，兑换时要求 `code_verifier`，即完整的 PKCE。当前实现没有加，是为了
保持"极简、无注册"的定位。

## 7. 接入示例

### 7.1 curl

```bash
# 1. 生成 state 并让用户打开授权页
STATE=$(openssl rand -base64 32 | tr -d '=+/' | cut -c1-43)
echo "https://veloce.example.com/authorize?state=$STATE&redirect_uri=https%3A%2F%2Fapp.example.com%2Fcallback&app_name=MyApp"

# 2. 用户授权后，回调拿到 ?state=…，用同一个值兑换
curl -sS -X POST https://veloce.example.com/api/oauth/token \
  -H 'Content-Type: application/json' \
  -d "{\"state\":\"$STATE\"}"
```

### 7.2 Node.js

```js
import crypto from "node:crypto"

const state = crypto.randomBytes(32).toString("base64url")
const authorizeURL = new URL("https://veloce.example.com/authorize")
authorizeURL.search = new URLSearchParams({
  state,
  redirect_uri: "https://app.example.com/callback",
  app_name: "MyApp",
}).toString()
// 302 用户到 authorizeURL，之后回调里校验 query.state === state

const res = await fetch("https://veloce.example.com/api/oauth/token", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ state }),
})
const { token, api_base_url } = await res.json()
```

### 7.3 Python

```python
import json, secrets, urllib.parse, urllib.request

state = secrets.token_urlsafe(32)  # 43 字符，符合 [A-Za-z0-9._~-]{32,256}
print("open:", "https://veloce.example.com/authorize?" + urllib.parse.urlencode({
    "state": state,
    "redirect_uri": "https://app.example.com/callback",
    "app_name": "MyApp",
}))
# 用户授权后回调收到 ?state=…，用它兑换：
req = urllib.request.Request(
    "https://veloce.example.com/api/oauth/token",
    data=json.dumps({"state": state}).encode(),
    headers={"Content-Type": "application/json"},
)
print(json.load(urllib.request.urlopen(req)))
```

## 8. 撤销

用户在「令牌」页面停用或删除令牌即可让应用立刻失去访问权（下一次请求 401）。授权记录
本身 5 分钟后自动过期，不需要额外清理。

## 9. 常见问题

**Q：为什么不直接把令牌放在回调里？**
A：会被浏览器历史、Referer、代理与访问日志记录，等于把长期有效令牌散播出去。

**Q：为什么不要求应用注册？**
A：目标是让个人脚本、桌面工具、内部服务能用最少配置接入；代价是 `state` 必须当作机密
来处理。

**Q：回调必须 HTTPS 吗？**
A：非回环地址必须 HTTPS。只有 `localhost`/`127.0.0.1`/`[::1]` 允许 `http`，方便本地调试。

**Q：兑换能重试吗？**
A：不能重放。`state` 一次性，兑换失败只能重新走一次授权。
