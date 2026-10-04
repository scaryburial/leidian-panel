# Cloudflare × Xray 协议不可用问题调研报告

> 调研方式：仅网络检索（web_search / web_fetch），未连接任何服务器、未修改任何服务器配置。
> 结论分三档标注可信度：**【官方文档】** = Cloudflare / Xray 官方文档或官方仓库代码与提交；**【项目 issue】** = 项目仓库 issue / PR 中的可复现证据；**【社区经验】** = 博客、论坛、聚合教程等二手说法，已注明可信度与不确定处。
> 报告末尾单列「不确定 / 未证实」清单，凡未查到权威依据的推论均在该清单中明示。

---

## 0. 结论先行（TL;DR）

1. **Cloudflare 免费版（橙云 proxied）只代理 13 个端口**：HTTP 类 `80, 8080, 8880, 2052, 2082, 2086, 2095`；HTTPS 类 `443, 2053, 2083, 2087, 2096, 8443`。端口不在表内 → 橙云下**根本不通**，只能灰云直连或 Enterprise Spectrum。**【官方文档】** [Network ports](https://developers.cloudflare.com/fundamentals/reference/network-ports/)
2. **`security: reality` + 橙云 = 原理上不可能工作**。REALITY 是"改造过的 TLS"，要求客户端与 REALITY 服务端**端到端直接完成 TLS 1.3 握手并做自定义证书校验**；橙云下 TLS 在 CF 边缘就被终结，握手对象变成 CF 边缘而不是你的 xray。**【官方文档 + 项目 issue】** [Xray REALITY 文档](https://xtls.github.io/en/config/transports/reality.html)、[chika0801/Xray-examples#49](https://github.com/chika0801/Xray-examples/issues/49)
3. **CF 边缘证书（浏览器/客户端看到的）与你源站证书（Origin CA / 自签）是两套东西**。Origin CA 只用于「CF → 源站」这段，官方原文："These certificates only encrypt traffic between Cloudflare and your origin server, not traffic from client browsers to your origin." **【官方文档】** [Cloudflare origin CA](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/)
4. **Origin CA 有效期**：API 可选 `7 / 30 / 90 / 365 / 730 / 1095 / 5475` 天，面板默认 15 年（5475 天）；官方明确"不会发送到期通知"。**【官方文档】**
5. **自签证书 + 橙云 + SSL 模式 `Full` 可以工作**；`Full (strict)` 才会校验源站证书（必须公有 CA 或 CF Origin CA，且 CN/SAN 匹配）；`Flexible` 时 CF→源站为明文 HTTP，且**仅对 443 生效，其他端口的 HTTPS 会回落到 Full**。**【官方文档】** [Full](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full/)、[Full (strict)](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)、[Flexible](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/flexible/)
6. **端口类别必须与 xray 入站 `security` 匹配**：HTTP 类端口配 `security: tls` → 客户端 TLS 在边缘就握不上（即便握上，CF 也会用 `http://` 回源，明文打到 TLS 监听端口）；HTTPS 类端口配 `security: none` → CF 会用 `https://` 回源，TLS ClientHello 打到明文 HTTP 监听端口 → 典型表现是 CF 报 **525 / 源站报文解析失败**。**【官方文档 + 社区经验（机制一致）】**
7. **最稳的橙云组合**：自有域名 + 橙云 + **443 端口** + `VLESS/VMess + WebSocket + TLS`（源站装 CF Origin CA 或真证书，SSL 模式 `Full (strict)`）；或迁移到 XHTTP。441/非 443 端口虽然可用，但缓存被禁、部分地区/CF IP 会禁非标端口。**【官方文档 + 社区经验】**
8. **"优选域名"（time.is / icook.hk / ip.sb / www.visa.com.sg 等）能成立，是因为 CF 按 SNI/Host 路由，而不是按 IP 路由**：只要 SNI 和 Host 是你自己的域名（且该域名在 CF 上是橙云），你连到 CF 的**任意** anycast IP 都会被正确送回你的源站。第三方域名只是"提供一组好用的 CF 边缘 IP"。风险：对方关掉 CF / 换解析 / 该 CF IP 被运营商封 / 部分 CF IP 禁非标端口；**绝不能把 SNI 也设成对方域名（那是 domain fronting，会路由到对方源站）**。**【官方文档（anycast + SNI 优先级）+ 社区经验（机制解释）】**
9. **Xray 26.x 的 VMess / WebSocket / Shadowsocks 弃用提示是"警告"，不是"不可用"**。源码中它们调用的是 `PrintNonRemovalDeprecatedFeatureWarning()`（"deprecated, not recommended for using and might be removed"）；真正会**直接报错拒绝启动**的是已被移除的 `h2`/`h3`/`http` 传输（`PrintRemovedFeatureError`）。**【官方仓库代码/提交】** [feature_errors.go](https://github.com/XTLS/Xray-core/blob/main/common/errors/feature_errors.go)、[commit cd8aab9](https://github.com/XTLS/Xray-core/commit/cd8aab9544bc35fbc9990f61cb3613d0abbbe775)、[v26.1.23 release notes](https://newreleases.io/project/github/XTLS/Xray-core/release/v26.1.23)

---

## 1. Cloudflare 免费版（非企业）回源端口支持列表

### 1.1 官方端口清单

来源：**【官方文档】** [Cloudflare Fundamentals → Network ports](https://developers.cloudflare.com/fundamentals/reference/network-ports/)（页面标注 Last updated Apr 20, 2026；原文亦可取 [Markdown 版](https://developers.cloudflare.com/fundamentals/reference/network-ports/index.md)）

官方原文分类（逐字）：

> **HTTP ports supported by Cloudflare**
> - 80
> - 8080
> - 8880
> - 2052
> - 2082
> - 2086
> - 2095
>
> **HTTPS ports supported by Cloudflare**
> - 443
> - 2053
> - 2083
> - 2087
> - 2096
> - 8443

同一页还有两个重要补充：

> **Ports supported by Cloudflare, but with caching disabled**
> - 2052, 2053, 2082, 2083, 2086, 2087, 2095, 2096, 8880, 8443
>
> Note: Enterprise customers that want to enable caching on these ports can do so by creating a cache rule.

> Ports 80 and 443 are the only ports compatible with: HTTP/HTTPS traffic within China data centers for domains that have the **China Network** enabled.

### 1.2 你点名要确认的 13 个端口

| 端口 | 类别 | 依据 |
| --- | --- | --- |
| `80` | **HTTP** | 官方 "HTTP ports supported by Cloudflare" |
| `8080` | **HTTP** | 同上 |
| `8880` | **HTTP** | 同上 |
| `2052` | **HTTP** | 同上 |
| `2082` | **HTTP** | 同上 |
| `2086` | **HTTP** | 同上 |
| `2095` | **HTTP** | 同上 |
| `443` | **HTTPS** | 官方 "HTTPS ports supported by Cloudflare" |
| `2053` | **HTTPS** | 同上 |
| `2083` | **HTTPS** | 同上 |
| `2087` | **HTTPS** | 同上 |
| `2096` | **HTTPS** | 同上 |
| `8443` | **HTTPS** | 同上 |

即：**HTTP 类 = 80, 8080, 8880, 2052, 2082, 2086, 2095；HTTPS 类 = 443, 2053, 2083, 2087, 2096, 8443。** 13 个全部在列表内，没有"表外端口"。

### 1.3 "HTTP 类端口 → CF 用 http:// 回源"这句话，官方是怎么写的

这里必须精确区分"官方逐字写了什么"和"社区/教程的等价概括"，因为它是本案例诊断的关键。

- 官方**逐字明确**的是端口分类，以及 `Full` 模式的回源 scheme 规则。**【官方文档】** [Full](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full/) 原文：

  > When you set your encryption mode to **Full**, Cloudflare allows HTTPS connections between your visitor and Cloudflare and **makes connections to the origin using the scheme requested by the visitor**. If your visitor uses `http`, then Cloudflare connects to the origin using plaintext HTTP and vice versa.

- 官方**没有**用一句话写"在 2052 上 CF 一定用 http:// 回源"。但把这个 scheme 规则和端口分类合起来读，结论是确定的：**HTTP 类端口上访客用明文 HTTP 访问 → CF 用 http:// 回源；HTTPS 类端口上访客用 TLS 访问 → CF 用 https:// 回源。**
- 官方还有一条**间接但很强**的佐证。**【官方文档】** [Flexible](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/flexible/) 原文：

  > Flexible mode is only supported for HTTPS connections on port 443 (default port). **Other ports using HTTPS will fall back to Full mode.**

  这说明 CF 明确按"端口上的协议"来决定回源行为，端口与协议类别是强绑定的。

- 官方 525 错误页进一步佐证 HTTPS 回源失败的表现。**【官方文档】** [Error 525: SSL handshake failed](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-525/) 原文：

  > Error `525` occurs when these two conditions are true: The SSL handshake fails between Cloudflare and the origin web server; **Full or Full (Strict)** SSL is set.
  > Resolution: … No valid SSL certificate is installed. **Port 443 (or another custom secure port) is not open.** No SNI support. …

- 社区/教程对同一机制的概括（与上述官方规则一致）：**【社区经验，可信度较高（多来源一致）】**
  - [丙醛说《CF 中转两种姿势》](https://blog.hbqfkj.cn/2026/09/self-node-cf-access-argo-vs-cdn-2026/)："CF 免费套餐只接受下列端口的回源连接，**你的 WS 服务必须监听其中之一**"，并把端口分为"**HTTPS（TLS）端口**：443、2053、2083、2087、2096、8443"和"**HTTP（明文）端口**：80、8080、8880、2052、2082、2086、2095"。
  - [lvshuqi.top《Cloudflare 非标 HTTPS 端口支持列表》](https://lvshuqi.top/2025/09/29/Cloudflare%E9%9D%9E%E6%A0%87HTTPS%E7%AB%AF%E5%8F%A3%E6%94%AF%E6%8C%81%E5%88%97%E8%A1%A8/)：同样把列表拆成 "HTTPS (TLS) 端口" 与 "HTTP 端口" 两组。

> **重要提醒（官方与社区表述的差异）**：不少老教程写"Full 模式 = CF 总是用 HTTPS 回源"。而 Cloudflare **当前（2026-07-09 更新）** 的 Full 文档写的是"**跟随访客请求的 scheme**"。两者在"HTTPS 类端口"上结论相同（都是 https 回源）；差别只体现在"如果客户端真能在 HTTP 类端口上发 TLS"这种边缘情形。本报告采用当前官方措辞，并把"CF 边缘是否在 HTTP 类端口接受 TLS"列入[第 7 节不确定清单](#7-不确定--未证实清单)。

### 1.4 端口表外的端口怎么办

官方同页给出两条路（**【官方文档】**）：
1. 把该子域改成**灰云（DNS only）**，绕过 CF 直连源站；
2. 配置 **Spectrum** 应用（任意 TCP/UDP 端口，但"all TCP and UDP ports"仅 Enterprise 可用）。

另外，免费版可用 **Origin Rules** 覆盖**目标端口**（即 CF 回源到你源站时用哪个端口），这是免费版唯一可用的 Origin Rules 覆盖项。**【官方文档】** [Origin Rules](https://developers.cloudflare.com/rules/origin-rules/) 可用性表：

| 覆盖项 | Free | Pro | Business | Enterprise |
| --- | --- | --- | --- | --- |
| Override Host header | No | No | No | Yes |
| Override SNI | No | No | No | Yes |
| Override DNS records | No | No | No | Yes |
| **Override destination port** | **Yes** | Yes | Yes | Yes |

（含义：免费版改不了回源 Host/SNI，只能改"回源到哪个端口"。）

### 1.5 非标准端口的 WebSocket

- **【官方文档】** [Cloudflare Network → WebSockets](https://developers.cloudflare.com/network/websockets/)：
  > Cloudflare supports proxied WebSocket connections without additional configuration.
  > **Availability**: WebSockets are supported on **all Cloudflare plans**.
  > Compatibility：与 SSL 兼容（Yes）、与 WAF 兼容（初始 101 请求会过 WAF，建连后不再检查）、与 Argo **不**兼容。
  > 空闲超时：CF 会在"双向都没有数据"一段时间后关闭 WebSocket（Enterprise 可自定义），建议客户端做 ping/pong 心跳。
  > 官方还提到全局发布新代码时会重启服务器，会**终止** WebSocket 连接。

- **端口维度上**：WebSocket 是 HTTP/1.1 的 Upgrade（ALPN **http/1.1**）。官方 WebSocket 文档**没有**对端口做任何限制，因此可用端口即"CF 代理的 HTTP/HTTPS 端口"（见 1.1）。也就是说，**HTTP 类与 HTTPS 类端口都能跑 WS**，区别只在于"边缘到客户端这段是明文还是 TLS"，以及"CF 到源站这段是 http 还是 https"。
  - 佐证（**【社区经验，可信度中高】**）：[BiuPing《Cloudflare 优选 IP 完全指南》](https://www.biuping.com/article/23) 避坑清单第 11 条："CF 部分 IP 出于 DDoS 防护，会禁用 8080 / 2052 等非标准端口。如果你跑 WS+TLS 之类的服务用了非标端口，优选 IP 之后可能完全打不通。优先用 443。" —— 这是"某些 CF IP 上非标端口不可用"的经验说法，不是官方承诺，务必自行实测。
  - 另一条官方硬限制：**China Network 下只有 80 / 443**（见 1.1）。

- **WS 的实际限制（官方 + 项目侧）**：
  - ALPN 固定 `http/1.1`，没有真正的多路复用（这正是 Xray 把 WS 标为 deprecated 的理由，见第 6 节）。
  - Argo 与 WebSocket 不兼容（**【官方文档】**，同上）。
  - 一旦建连，WAF 不再检查（**【官方文档】**）—— 对代理用途是有利的。

---

## 2. 橙云（proxied）与 REALITY 的兼容性

### 2.1 结论

**域名开启橙云后，VLESS+REALITY 不可能工作**；必须是**灰云（DNS only）直连**，或把 REALITY 换成本质上是"TLS over CDN"的方案（WS/XHTTP + 普通 TLS + CF 边缘证书）。

### 2.2 为什么（原理）

- **【官方文档】** [Xray REALITY 文档](https://xtls.github.io/en/config/transports/reality.html) 原文：
  > REALITY is a **modified form of TLS** that uses the appearance and handshake characteristics of a target site as camouflage.
  > REALITY can only be used together with the `RAW`, `XHTTP`, and `gRPC` transport methods.
  > For developers: REALITY only modifies TLS. **On the client side it mainly requires slight handling around a fully random session ID and custom certificate verification**, and in theory it is broadly compatible with most TLS combinations.

  关键点：客户端持有 `password`（= 服务端 x25519 公钥）并做**自定义证书校验**，且 ClientHello 带 REALITY 特有的随机 session ID；服务端用 `privateKey` 完成与自己这一侧的握手。这套认证只能发生在"**客户端 ↔ REALITY 服务端**"之间。

- 橙云下，TLS 在 **Cloudflare 边缘**终结（CF 用自己签发的边缘证书对外，如 Universal SSL / Advanced Cert）。因此：
  1. 客户端握手的对象是 CF 边缘，不是你的 xray；CF 边缘没有 REALITY 的 `privateKey`，无法回应 REALITY 的认证（既无法通过 REALITY 客户端的自定义证书校验，也不认识 REALITY 那套 session id 约定）；
  2. 反过来，CF 边缘用标准证书链回应，REALITY 客户端会拿"真证书"去校验，失败或落入未认证分支；
  3. 即便当成"普通 TLS + VLESS"去撞，VLESS 头也要落在 CF 的 HTTP 语义里才能回源，而 REALITY 走的是原生 TLS（`RAW`），不是 HTTP，CF 的 L7 代理无从转发。

- **【官方文档，反向相关但很有价值】** 同页对 `target`（旧名 `dest`）的 WARNING：
  > For camouflage reasons, Xray **directly forwards** traffic that fails authentication … to `target`. If the IP address of the `target` site is special, for example **a site behind Cloudflare CDN**, your server effectively becomes a port forwarder for Cloudflare and may be abused after scanning. To avoid that, consider placing Nginx or something similar in front to filter out unwanted SNI values.

  注意这是**另一个坑**（"被借证书的 target 站在 CF 后面"），不要和"REALITY 自己套 CF"混淆；但它同样说明 REALITY 与 CDN 的交互是官方明确警示的区域。

- **【项目 issue，实测证据】** [chika0801/Xray-examples#49 "VLESS-XHTTP-REALITY 这个配置example无法通过cloudflare cdn"](https://github.com/chika0801/Xray-examples/issues/49)：用户报告"同配置直接写服务器 IP 就没事，换成 Cloudflare 的 IP 就不行"，日志里能看到 CF 回源、但走的是 **fallback 分支**（`fallback starts > invalid request version` / `failed to fallback request payload`），**流量代理不生效**。这是 REALITY 借道 CDN 失败的典型现场。可信度：项目 issue，单例但日志完整、与原理吻合。

- **【社区经验，可信度中】** 俄语博客 [《Почему лучше не использовать dest за CDN в XTLS-Reality》](https://yet-another-dev-blog.cc/post/reality-avoid-cdn) 解释了 REALITY 默认转发所有 SNI 的行为（"REALITY 默认转发所有 SNI，`dest` 只用来决定目的地"），并给出用 nginx `ssl_preread` 做 SNI 白名单的缓解方案。**注意**：该文讨论的是"dest 站在 CDN 后面"，不是"REALITY 服务端放在 CDN 后面"，引用时不要张冠李戴。

### 2.3 直连（DNS only / 灰云）下 VLESS+REALITY 的正常工作条件

以**【官方文档】**为准（[REALITY](https://xtls.github.io/en/config/transports/reality.html)）：

| 条件 | 说明 |
| --- | --- |
| 传输层 | 只能是 `RAW`（原 TCP）、`XHTTP`、`gRPC` 之一；不能是 `ws`/`httpupgrade` |
| `security` | `reality`（服务端 + 客户端都要） |
| 服务端 `target` | 必填，格式同 VLESS fallback `dest`，例如 `www.example.com:443` |
| 服务端 `serverNames` | 必填，客户端 `serverName` 必须在其列表内；通常与 `target` 的证书 SAN 一致；可含空串 `""` 以接受无 SNI 连接 |
| 服务端 `privateKey` | 必填，`./xray x25519` 生成 |
| 客户端 `password` | 必填，= 服务端私钥对应的公钥（`./xray x25519 -i "server private key"`）；旧名 `publicKey` |
| 客户端 `shortId` | 必须是服务端 `shortIds` 之一；长度 8 字节（≤16 个十六进制字符）、字符数必须为偶数 |
| 客户端 `fingerprint` | 必填；**不支持 `unsafe`**（REALITY 依赖 uTLS 操控底层 TLS 参数） |
| 端口 | 协议上不强制 443，但实践强烈建议 443（`tls ping` 可用于探测 `target` 的证书与 PQ 支持：`xray tls ping example.com`） |
| TLS 版本 | TLS 1.3 路线；若 `target` 支持 `X25519MLKEM768`，REALITY 客户端会自动用上后量子密钥交换 |
| 可选加固 | `mldsa65Seed` / `mldsa65Verify`（后量子签名，要求 `target` 证书 > 3500 字节）、`limitFallbackUpload/Download`（回退限速）、`maxTimeDiff`、`minClientVer/maxClientVer` |

**一句话**：灰云 + REALITY 时，客户端**直连**你的服务器 IP:端口，TLS 握手全程不经过任何第三方——这正是 REALITY 成立的前提。

---

## 3. Cloudflare Origin CA 证书

来源统一为 **【官方文档】**：[Cloudflare origin CA](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/)

### 3.1 有效期

- **面板**：创建时可"Choose a **Certificate Validity** period"（文档只说可选，未在页面列出全部数值）。
- **API**：`POST /certificates` 的 body 参数 `requested_validity`（"The number of days for which the certificate should be valid"），枚举值为：**`7`, `30`, `90`, `365`, `730`, `1095`, `5475`**。文档示例与默认演示值均为 **`5475`（= 15 年）**。**【官方文档】** [Create Certificate API](https://developers.cloudflare.com/api/resources/origin_ca_certificates/methods/create/)
- 面板默认值：15 年（5475 天）。API 文档**未显式标注 default**，此处依据的是示例值 + 面板 UI 的通行默认（见[第 7 节不确定清单](#7-不确定--未证实清单)）。**【官方文档（枚举值）+ 社区（默认值）】**
- **官方已知限制（逐字）**：
  > Known limitation: Cloudflare does not currently send expiration notifications for origin CA certificates. If you rely on long-lived origin CA certificates, track their expiration in your own certificate inventory or monitoring system.

### 3.2 能否用于非 443 / 非标准端口

- 证书本身**与端口无关**：它只是一张由 Cloudflare Origin CA 签发、用于源站 TLS 监听的服务端证书，装在哪个端口由你的源站（nginx/xray/caddy）决定。
- 官方部署步骤写的是"**Enable SSL and port `443` at your origin web server**"，即默认按 443 讲；文档没有禁止装在非标端口。
- 但**回源端口由 CF 侧决定**（客户端访问哪个受支持端口，CF 就回源到哪个端口，除非用 Origin Rules 覆盖目标端口）。所以"源站 HTTPS 监听 2087"要成立，前提就是客户端访问的也是 2087（HTTPS 类端口）。Origin Rules 的目标端口覆盖在**免费版可用**（见 1.4）。
- 可覆盖主机名：**最多 200 个 SAN**，支持一级通配符（`*.example.com`），**不支持 IP 作为 SAN**。**【官方文档】**

### 3.3 它只用于「CF → 源站」；客户端看到的是 CF 边缘证书

官方原文（**【官方文档】**，[Cloudflare origin CA](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/)）：

> If your origin only receives traffic from proxied records, use Cloudflare origin CA certificates to **encrypt traffic between Cloudflare and your origin web server** and reduce bandwidth consumption. Once deployed, these certificates are compatible with **Strict SSL mode**.

> Caution: Site visitors may see untrusted certificate errors if you pause Cloudflare or disable proxying on subdomains that use Cloudflare origin CA certificates. **These certificates only encrypt traffic between Cloudflare and your origin server, not traffic from client browsers to your origin.**

所以：
- 客户端（浏览器 / 代理客户端）在橙云下看到的**始终是 CF 边缘证书**（Universal SSL 等），与源站装什么证书无关；
- Origin CA 属于 CF 运营的私有信任链，**只有 Cloudflare 会校验它**，在源站被其它客户端直连时会出现 `ERR_CERT_AUTHORITY_INVALID`（官方 troubleshooting 页专门讲了这一点）；
- 客户端侧证书必须覆盖你使用的域名（Universal SSL 覆盖 zone 顶点 + 一级通配符；更深的层级需要 Advanced Certificate / Total TLS）。**【官方文档】** [Certificate and hostname priority](https://developers.cloudflare.com/ssl/reference/certificate-and-hostname-priority/)

### 3.4 自签证书 + 橙云 + SSL 模式 "Full" 是否可行

**可行。** 三种模式对比（全部 **【官方文档】**）：

| 模式 | 访客 ↔ CF | CF ↔ 源站 | 是否校验源站证书 | 源站证书要求 |
| --- | --- | --- | --- | --- |
| `Off` | 可 HTTPS，但会告警/不加密 | 明文 HTTP | — | 无 |
| `Flexible` | 加密 | **明文 HTTP** | — | 无（无需任何证书）。**只对 443 的 HTTPS 生效；其他端口的 HTTPS 回落到 Full**；源站若强跳 HTTPS 会造成重定向循环 |
| `Full` | 加密 | **加密（跟随访客 scheme）** | **完全不校验**（官方原文：certificate "will not be validated in any way. It can be expired, self-signed, or not even have a matching CN/SAN entry"） | **自签即可** |
| `Full (strict)` | 加密 | 加密 | 校验：未过期 + 公有 CA 或 **CF Origin CA** 签发 + CN/SAN 匹配 | 自签**不可**用；否则报 **526**；握手失败报 **525** |
| `Strict (SSL-Only Origin Pull)` | 加密 | 强制加密 | 使用 CF 客户端证书（mTLS） | Enterprise |

对应官方页面：[Off](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/off/)、[Flexible](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/flexible/)、[Full](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full/)、[Full (strict)](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)、[Strict (SSL-Only Origin Pull)](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/ssl-only-origin-pull/)。

补充（**【官方文档】**）：`Full (strict)` 只要求"未过期 + 受信 CA + CN/SAN 匹配"；CF Origin CA 属于被认可的签发者，因此**自签 + Full 能通，自签 + Full (strict) 不通；Origin CA + Full (strict) 能通**。另外 AOP/Origin Pull 在 `Off` / `Flexible` 下不生效。

---

## 4. VMess/VLESS + WebSocket + Cloudflare 的标准可用组合

### 4.1 橙云 + 免费版下最稳妥的传输组合

共识（官方文档可支撑的部分 + 教程生态）：

1. **首选：`443` + `WebSocket`（或 XHTTP）+ `TLS`**，源站证书用真证书或 **CF Origin CA**，SSL 模式 `Full (strict)`；客户端 `address` = 你的域名，`serverName`/`host` = 你的域名，WS `path` 用一个非特征字符串。**【社区经验，可信度中高】** [丙醛说](https://blog.hbqfkj.cn/2026/09/self-node-cf-access-argo-vs-cdn-2026/)："第一次上 CF、求稳求快：先走姿势一『443 + VLESS+WS+TLS 回源』"；[Zoio 3x-ui 教程](https://www.zoio.net/2026/08/3x-ui-cloudflare-cdn.html)。
2. **次选：HTTPS 类非标端口（2053/2083/2087/2096/8443）+ TLS**。可行，但注意：这些端口**缓存被禁用**（官方）、部分 CF IP 会禁非标端口（社区）、China Network 不可用（官方）。
3. **明文方案：HTTP 类端口（80/8080/8880/2052/2082/2086/2095）+ `security: none`** + SSL 模式 `Flexible` 或 `Off`。可用，但**客户端↔CF 这段是明文**（代理协议元数据缺乏外层 TLS 保护），不推荐生产。**【官方文档（Flexible 机制）+ 社区经验】**
4. **避免的坑**：不要用 REALITY（见第 2 节）；不要在 HTTP 类端口上开 TLS；不要在 HTTPS 类端口上用明文入站。Cloudflare 官方明确 WebSocket 与 SSL 兼容、与 Argo 不兼容（见 1.5）。

### 4.2 场景 A：SSL 模式 `Full` + 源站端口 `2052`（HTTP 类）+ xray 入站 `security: tls`

**会发生什么（预期结论，机制上必然）：**

- `2052` 属 **HTTP 类**端口 → 该端口上访客（代理客户端）走的是**明文 HTTP**。客户端若强行在这条连接上做 TLS（即 `security: tls`），握手对象是 CF 边缘，而 CF 在该端口按明文的 HTTP 处理，**TLS 握手在边缘就无法建立**（表现为连接被重置/超时，客户端侧 `tls: handshake failure`）。
- 即便假设边缘接受了 TLS：按官方 `Full` 的 scheme 规则"跟随访客 scheme"，CF 会以 **`http://`** 回源到 `2052`，**明文 HTTP 请求打到 xray 的 TLS 监听端口** → xray 解析不到 TLS ClientHello，握手失败；CF 侧典型表现是 **525（Full / Full strict 下 SSL 握手失败）** 或源站直接断开。
- **结论：这个组合从原理上不可能工作。** 必须二选一——要么把入站改成 `security: none`（保留 2052 / 明文），要么把端口换成 HTTPS 类（443/2053/2083/2087/2096/8443）并保留 `security: tls`。

### 4.3 场景 B：源站端口 `2087`（HTTPS 类）+ xray 入站 `security: none`（明文 HTTP）

**会发生什么（预期结论，机制上必然）：**

- `2087` 属 **HTTPS 类**端口 → 客户端到 CF 边缘这段是 TLS；CF 按 scheme 规则会用 **`https://`** 回源到 `2087`。
- xray 入站 `security: none` 意味着它监听的是**明文 HTTP/WS**。CF 发来的 TLS ClientHello 到不了 HTTP 解析层 → xray 无法完成 WS Upgrade / 返回 400 或直接断开 → CF 侧报 **525 SSL handshake failed**（**【官方文档】**：525 的前提正是 Full / Full (strict) 下"CF 与源站之间的 SSL 握手失败"，官方列的原因包含"源站没有装有效证书 / 端口未开放 / 无 SNI 支持"）。
- **结论：这个组合从原理上也不可能工作。** 要么给 2087 配 `security: tls` + 证书（自签配 `Full`，CF Origin CA / 公有 CA 配 `Full (strict)`），要么把端口换成 HTTP 类。

### 4.4 组合速查表

| 客户端访问端口 | 端口类别 | xray 入站 `security` | SSL 模式 | 结果 |
| --- | --- | --- | --- | --- |
| 2052 / 2082 / 2086 / 2095 / 8080 / 8880 / 80 | HTTP | `none`（明文 WS/HTTP） | `Flexible` / `Full` / `Off` | ✅ 可用（客户端↔CF 明文） |
| 2052 / 2082 / … / 80 | HTTP | `tls` | `Full` / `Full (strict)` | ❌ **原理上不可能**（边缘 TLS 不成立；回源为 http 打到 TLS 端口） |
| 443 / 2053 / 2083 / 2087 / 2096 / 8443 | HTTPS | `tls`（自签） | `Full` | ✅ 可用（CF 不校验源站证书） |
| 443 / 2053 / 2083 / 2087 / 2096 / 8443 | HTTPS | `tls`（CF Origin CA / 公有 CA，CN/SAN 匹配） | `Full (strict)` | ✅ 可用（推荐） |
| 443 / 2053 / 2083 / 2087 / 2096 / 8443 | HTTPS | `tls`（自签） | `Full (strict)` | ❌ 报 **526**（源站证书不受信） |
| 443 / 2053 / 2083 / 2087 / 2096 / 8443 | HTTPS | `none`（明文） | 任意 | ❌ **原理上不可能**（CF 用 https 回源） |
| 任意受支持端口 | HTTP/HTTPS | **`reality`** | 任意 | ❌ **原理上不可能**（TLS 在 CF 边缘终结） |
| 不在 13 端口表内（如 10086） | — | 任意 | 任意（橙云） | ❌ CF 不代理该端口；须灰云或 Spectrum |

---

## 5. CF 优选域名 / 优选 IP 的原理

### 5.1 为什么用 time.is / icook.hk / ip.sb / www.visa.com.sg 这类第三方域名

**核心机制：Cloudflare 是按 SNI（以及 Host）路由的，不是按目标 IP 路由的。**

- CF 使用 **Anycast**：全球 300+ PoP 对外宣告**同一组 IP 段**。你连到哪个 CF IP，都会进入 CF 网络。**【官方文档】** [Cloudflare IP addresses](https://developers.cloudflare.com/fundamentals/concepts/cloudflare-ip-addresses/)（官方也公开 IP 段列表 `https://www.cloudflare.com/ips-v4`）。
- CF 边缘收到 TLS 握手后，**用 SNI 找 zone**，再用 zone 的配置回源。**【官方文档】** [Certificate and hostname priority](https://developers.cloudflare.com/ssl/reference/certificate-and-hostname-priority/)：
  > Certificate presentation: **SNI match** → **SNI wildcard match** → IP address（无 SNI 时才用 IP）。

  即：**决定"这包流量属于哪个站"的是 SNI，不是 IP。**
- 因此，只要请求里的 **SNI = 你自己的域名**（且 Host 也是），无论 TCP 连的是 CF 的哪个 IP，CF 都会把它路由到**你的源站**，并出示**你的域名的边缘证书**。
- 那些第三方域名的唯一作用，是"**提供一组数量多、在国内某些线路上更快/更稳的 CF 边缘 IP**"——因为任何 CF 上的站都可能落在不同的 PoP/IP 段上，"优选域名"就是让别人替你维护一份"好 IP 的域名入口"。

**机制解释的最清楚的一份社区材料（可信度中高，与官方 SNI 优先级文档吻合）**：**[BiuPing《Cloudflare 优选 IP 完全指南》](https://www.biuping.com/article/23)**，原文：

> **核心机制：**Cloudflare 是基于 SNI 和 Host 头分发请求的。也就是说，**只要你最终请求里带的 SNI / Host 是你的域名，无论你连到 CF 的哪个 IP，它都能正确把流量转给你的源站。**这就是优选 IP 能成立的根本原因——你换 IP，但服务还是你的服务。

该文同时给出了正常的请求链路（DNS → CF 返回 IP → TCP → **TLS 握手携带 SNI** → CF 按 SNI 找 zone → 回源），以及"改 hosts 换 IP，SNI 不变"的落地方式。

其他相关（**社区经验**）：[jc-lw/youxuanyuming](https://github.com/jc-lw/youxuanyuming)（用采集来的 IP 自建优选域名）、[afoim/fuwari `self-host-fastip.md`](https://github.com/afoim/fuwari/blob/main/src/content/posts/self-host-fastip.md)、[1molchuan/edge-smart-doh](https://github.com/1molchuan/edge-smart-doh)（按 ISP 提供优选 IP 的 DoH）。

### 5.2 这种玩法成立的前提

1. **你自己的域名必须托管在 CF 且是橙云（proxied）**（否则 SNI 找不到 zone，或直接回落到灰云直连 IP）。**【官方文档：SNI 优先级 + DNS proxy status】**
2. **SNI 与 Host 都必须是你自己的域名**。（`address` 可以是优选 IP 或优选域名，但 `serverName`/`host` 必须是你自己的域名。）
3. **落点必须是 CF 的边缘 IP**：任何在 CF 上的域名解析出来的 IP 都满足；"落在同一个 CF anycast 网络"就够了，**并不需要**这个第三方域名与你的账号/源站有任何关系。
4. **端口必须落在 CF 代理端口表内**（见第 1 节），且该 CF 边缘 IP 上没有禁用该端口。

### 5.3 风险与限制

| 风险 | 说明 | 依据/可信度 |
| --- | --- | --- |
| 对方域名关掉 CF / 灰云化 / 换解析 / 过期 | 你配置里的"优选域名"不再指向 CF 边缘 IP，连接直接失效 | **社区经验，高**：BiuPing 避坑第 5 条"把 CF 的某个 IP 当作固定常量硬编码…脚本要有 fallback" |
| 该 CF IP 被运营商/GFW 局部封锁 | 只影响部分省份，过一段时间可能恢复；所以优选要周期性复测 | **社区经验，高**：[丙醛说](https://blog.hbqfkj.cn/2026/09/self-node-cf-access-argo-vs-cdn-2026/)、[BiuPing](https://www.biuping.com/article/23) |
| 某些 CF IP 禁非标端口 | 优选 IP + 非标端口（8080/2052 等）可能"完全打不通"；优先 443 | **社区经验，中**：BiuPing 避坑第 11 条 |
| **SNI 写成对方域名（= domain fronting）** | CF 会按 SNI 路由到**对方**的 zone/源站，你的请求不可能回到你的源站；且这属于 domain fronting 语义，可能被 CF 策略拦截 | **原理确定（官方 SNI 优先级）**；CF 对 domain fronting 的具体策略本报告未找到官方定性 → 见[第 7 节](#7-不确定--未证实清单) |
| 证书校验 | 因为 SNI = 你自己的域名，CF 出示的就是**你域名的边缘证书**，正常链路**不需要** `allowInsecure` / 跳过证书校验。若某些客户端把校验对象错当成 `address`（IP），才会需要；正确做法是显式设置 `serverName`/`host` = 你的域名 | **原理 + 社区经验**：BiuPing 避坑第 6、10 条（"用某些代理工具自定义 IP 时，如果忘了带正确的 SNI，CF 边缘节点会返回 HTTP 403 或者 SSL 错误"；"不要用 IP 访问，要改 hosts 用域名访问"） |
| 依赖第三方域名的稳定性/善意 | 优选域名本身是别人资产，对方随时可变更；且大量代理用户共用同一入口会让该 IP 画像变差 | **社区经验，中** |
| ToS / 滥用风险 | 优选 IP 拿的是 **CF 官方 IP 段**（共用 anycast），本身不等于"蹭别人的服务"，但用别人的域名做入口存在观感/规则上的灰色地带；本报告未找到 Cloudflare 官方对此的明确条款解释 | **不确定**，见第 7 节 |

---

## 6. Xray 26.x 的弃用警告

### 6.1 结论：是**警告**，不是"协议不可用"

**【官方仓库源码/提交】** 权威依据：`common/errors/feature_errors.go` 里有三类输出：

```go
// PrintNonRemovalDeprecatedFeatureWarning prints a warning of the deprecated feature that won't be removed in the near future.
func PrintNonRemovalDeprecatedFeatureWarning(sourceFeature string, targetFeature string) {
	LogWarning(context.Background(), "The feature "+sourceFeature+" is deprecated, not recommended for using and might be removed. Please migrate to "+targetFeature+" as soon as possible.")
}

// PrintDeprecatedFeatureWarning prints a warning for deprecated and going to be removed feature.
func PrintDeprecatedFeatureWarning(feature string, migrateFeature string) { ... }

// PrintRemovedFeatureError prints an error message for removed feature then return an error.
func PrintRemovedFeatureError(feature string, migrateFeature string) error { ... }
```

来源：[XTLS/Xray-core `common/errors/feature_errors.go`](https://github.com/XTLS/Xray-core/blob/main/common/errors/feature_errors.go)。

**具体到各特性**（来自提交 [cd8aab9](https://github.com/XTLS/Xray-core/commit/cd8aab9544bc35fbc9990f61cb3613d0abbbe775) 的完整 diff；该提交即 [PR #5567](https://github.com/XTLS/Xray-core/pull/5567)，随 [`v26.1.23`](https://newreleases.io/project/github/XTLS/Xray-core/release/v26.1.23) 发布，release notes 中列为 *"Config: Add Warning for deprecated features (allowInsecure, Shadowsocks, VMess, Trojan, VLESS without flow)"*）：

| 特性 | 输出类型 | 警告文本 | 建议迁移到 |
| --- | --- | --- | --- |
| **WebSocket 传输** | `PrintNonRemovalDeprecatedFeatureWarning`（**仅警告**） | `The feature WebSocket transport (with ALPN http/1.1, etc.) is deprecated, not recommended for using and might be removed.` | **XHTTP H2 & H3** |
| **VMess** | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature VMess (with no Forward Secrecy, etc.) is deprecated, …` | **VLESS Encryption** |
| **Shadowsocks**（无 FS 等） | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature Shadowsocks (with no Forward Secrecy, etc.) is deprecated, …` | **VLESS Encryption** |
| **Trojan** | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature Trojan (with no Flow, etc.) is deprecated, …` | VLESS with Flow & Seed |
| VLESS 不带 flow | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature VLESS (with no Flow, etc.) is deprecated, …` | VLESS with Flow & Seed |
| gRPC 传输 | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature gRPC transport (with unnecessary costs, etc.) is deprecated, …` | XHTTP stream-up H2 |
| HTTPUpgrade 传输 | `PrintNonRemovalDeprecatedFeatureWarning` | `The feature HTTPUpgrade transport (with ALPN http/1.1, etc.) is deprecated, …` | XHTTP H2 & H3 |
| **`h2` / `h3` / `http` 传输** | **`PrintRemovedFeatureError`（返回 error，配置直接失败）** | `The feature HTTP transport (without header padding, etc.) has been removed and migrated to XHTTP stream-one H2 & H3.` | XHTTP stream-one H2 & H3 |
| **`allowInsecure`** | 在弃用警告列表中（见 release notes；具体输出函数未在本次抓取的 diff 中逐一展开） | — | 用 `pinnedPeerCertSha256` 等替代（见 v26.1.23 release notes 第 5 条） |

**关键结论**：
- **VMess / WebSocket / Shadowsocks / Trojan / gRPC / HTTPUpgrade / 无 flow 的 VLESS 都只是打印 Warning，功能照常工作**，不会导致"协议不可用"。函数名本身就叫 `PrintNonRemoval…`（"won't be removed in the near future"）。
- 只有**已被移除**的特性（`h2`/`h3`/`http` 传输）会让配置无法加载（返回 error）。
- **版本演进**：更早的 `v24.12.15` 用的是 `PrintDeprecatedFeatureWarning`，措辞更硬。**【项目 issue】** [233boy/v2ray#1387](https://github.com/233boy/v2ray/issues/1387) 里用户贴出的 v24.12.15 实际日志：
  > `[Warning] common/errors: This feature WebSocket transport (with ALPN http/1.1, etc.) is deprecated and being migrated to XHTTP H2 & H3. Please update your config(s) according to release note and documentation before removal.`
  > `[Warning] common/errors: This feature "host" in "headers" is deprecated and being migrated to independent "host". …`

  v26.1.23 把它降级为"non-removal"措辞，说明**短期内不会移除**。
- 相关的社区现象（引用时注意分级）：
  - **【项目 issue】** [Openwrt-Passwall/openwrt-passwall2#999](https://github.com/Openwrt-Passwall/openwrt-passwall2/issues/999)：标题即"[Feature Request]: new xray core removed allow insecure and after update to【26.1.31】passwall2 stop"——即**面板/插件侧**在升级到 26.1.31 后因为 `allowInsecure` 相关处理而中断。这是面板适配问题，不能直接推断"xray 本体删除了 allowInsecure"（本报告未在主线源码中确认删除动作）。
  - **【项目 issue】** [MHSanaei/3x-ui#3678](https://github.com/MHSanaei/3x-ui/issues/3678)（"Xray 26.1.18, XHTTP: can't set the flow option - warning in log"）、[#3712](https://github.com/MHSanaei/3x-ui/issues/3712)（"Missing flow field with XHTTP transport & Reality in inbound user"）、[#3841](https://github.com/MHSanaei/3x-ui/issues/3841)（"need VLESS with Flow & Seed"）、[#6004](https://github.com/MHSanaei/3x-ui/issues/6004)、[#5922](https://github.com/MHSanaei/3x-ui/issues/5922)：26.x 里出现的新默认值（如 REALITY `minClientVer`）与 flow 字段要求，会让**老客户端/老面板配置**连不上。这类问题**不是弃用警告本身**造成的，但会与"协议不可用"的现象混淆，值得排查时区分。

### 6.2 XHTTP 与 WebSocket 在 Cloudflare 下的适用性差异

**【官方文档/官方讨论】**
- Xray 官方把 XHTTP 定位为 WebSocket 的继任者：文档页标题即 **"XHTTP: Beyond REALITY"**，指向官方讨论 [XTLS/Xray-core#4113](https://github.com/XTLS/Xray-core/discussions/4113)。**【官方文档】** [XHTTP](https://xtls.github.io/en/config/transports/xhttp.html)
- 官方给出的迁移方向：
  - WebSocket / HTTPUpgrade → **XHTTP H2 & H3**（弃用理由是 "ALPN http/1.1, etc."，即 H1/单连接/带内开销）
  - gRPC → **XHTTP stream-up H2**
  - HTTP（h2/h3 旧传输）→ **XHTTP stream-one H2 & H3**（已移除）
- 官方把 REALITY 的传输支持限定为 `RAW` / `XHTTP` / `gRPC`——**注意这说的是 REALITY 这一"传输安全层"能与哪些传输方法组合，与"能否过 CDN"无关**（过 CDN 时 TLS 仍在边缘终结，见第 2 节）。

**在 Cloudflare 语境下的实际差异（部分为推断，见第 7 节）**
- **WS**：固定 ALPN `http/1.1`，一条长连接；CF 官方明确支持且"所有套餐可用"（**【官方文档】**）；缺点是无多路复用、H1 语义、被 Xray 标记 deprecated。
- **XHTTP**：支持 H2/H3 与 packet-up / stream-up / stream-one 等工作模式（**【官方文档】** 指向 #4113）。**在 CF 橙云下，客户端↔CF 可以是 H2/H3，但 CF↔源站使用哪个 ALPN 由 CF 决定**（CF 对源站支持 HTTP/1.1 与 HTTP/2）——因此"XHTTP H3"在橙云下通常无法端到端成立，实际落地多为 H2。
- **社区反馈（可信度中，作为线索而非结论）**：
  - [Yulinanami/my-xhttp-cdn-config](https://github.com/Yulinanami/my-xhttp-cdn-config)：XHTTP + CDN 上下行分离的搭建步骤（社区实践，说明 XHTTP 过 CF 是可行的）。
  - [NTC 论坛"Тестируем XHTTP"](https://ntc.rkn.quest/t/%D1%82%D0%B5%D1%81%D1%82%D0%B8%D1%80%D1%83%D0%B5%D0%BC-xhttp/13855/146)、[chonglangbiji《XHTTP / Splithttp 新一代传输层完整指南（2026版）》](https://www.chonglangbiji.com/clients/xhttp/)：XHTTP 过 CF 时 stream-one/stream-up 的可行性依 CDN 而定，个别模式需要额外设置。
  - **[项目 issue]** [chika0801/Xray-examples#49](https://github.com/chika0801/Xray-examples/issues/49) 说明：**XHTTP 也救不了 REALITY 过 CDN**——同一个 issue 里失败的正是 "VLESS-XHTTP-REALITY + cloudflare cdn"。

> **对本案例的含义**：把 `ws` 换成 `xhttp` 是为了**跟上 Xray 的迁移方向**，但它**不能**修复"REALITY 套橙云"或"端口类别与 security 不匹配"这两类原理性错误。

---

## 7. 不确定 / 未证实清单

调研过程中**没有**找到权威依据、因此在本报告中一律标注为不确定的点：

1. **CF 边缘是否在"HTTP 类端口"上直接拒绝 TLS 握手。** 官方只给了端口分类与 Full 模式"跟随访客 scheme"的规则，**没有**逐字写"2052 端口不接受 TLS"。本报告采用"HTTP 类端口 = 访客侧明文 HTTP"这一被官方 Flexible 说明间接支持、且社区高度一致的解读，但把它当作机制性推论而非官方逐字结论。
2. **`Full` 模式回源 scheme 的历史说法。** 老教程普遍写"Full = 总是 https 回源"，而当前官方页面写"跟随访客请求的 protocol"。二者在 HTTPS 类端口上结论一致；差异只在边缘情形。此处以当前官方页面为准。
3. **Origin CA 面板默认有效期的"默认值"字段。** API 文档只列了枚举 `7/30/90/365/730/1095/5475` 与示例值 `5475`，未显式写 `default`。"面板默认 15 年"来自社区材料（如 [panelica 的 15 年说明](https://www.panelica.com/blog/cloudflare-origin-certificate-15-years-why-and-how-2026)），可信度中。
4. **CF 免费版"CF ↔ 源站"的 ALPN 支持（H2 是否一定可用）。** 本报告据此推断 XHTTP 在橙云下实际以 H2 落地，但未逐字查到官方对"回源 ALPN 支持矩阵"的说明。
5. **优选域名做法的 Cloudflare ToS 定性。** 未找到官方条款明确解释"用第三方 CF 域名作为接入地址"的合规边界。技术上不涉及伪装 SNI（SNI 仍是你自己的域名），但规则解释层面不确定。
6. **`allowInsecure` 在 Xray 主线中是否已被移除。** 确认它在弃用警告列表中；**未**在本次抓取的主线源码 diff 中确认"删除"动作。社区 issue（[passwall2#999](https://github.com/Openwrt-Passwall/openwrt-passwall2/issues/999)）报告的是面板侧升级后中断，属适配问题。
7. **Cloudflare 社区论坛的原始帖子内容。** `community.cloudflare.com` 的多个相关帖子（如 "Proxy doesnt work on port 2087"、"[ERR_SSL_PROTOCOL_ERROR with port number](https://community.cloudflare.com/t/ssl-error-err-ssl-protocol-error-with-port-number/529183)"）本次 fetch 返回 **HTTP 403（Cloudflare 挑战页）**，只拿到搜索摘要，未取到全文，故未作为本报告的核心论据。

---

## 8. 对本案例的直接推论

**案例背景（据任务描述）**：一个跑在 Cloudflare 橙云代理后面的 xray 面板/节点，出现"协议不可用"；需要判断哪些端口 / `security` 组合从原理上就不可能工作。

### 8.1 从原理上就**不可能**工作的组合（先排除这些）

| # | 组合 | 为什么不可能 | 依据类型 |
| --- | --- | --- | --- |
| 1 | **橙云 + `security: reality`**（任意端口） | REALITY 要求客户端与 REALITY 服务端端到端完成 TLS 1.3 + 自定义证书校验；橙云下 TLS 在 CF 边缘终结，握手对象变成 CF 边缘，REALITY 认证链路不成立；且 REALITY 只支持 `RAW/XHTTP/gRPC`，都不是 CF 的 L7 回源语义 | 官方文档 + 项目 issue（[#49](https://github.com/chika0801/Xray-examples/issues/49)） |
| 2 | **HTTP 类端口（80/8080/8880/2052/2082/2086/2095）+ `security: tls`** | 该端口上客户端是明文 HTTP，TLS 在边缘就不成立；即便成立，CF 也会用 `http://` 回源，明文请求打到 TLS 监听端口 → 握手失败（CF 侧 525） | 官方端口分类 + Full/Flexible/525 文档 + 社区一致经验 |
| 3 | **HTTPS 类端口（443/2053/2083/2087/2096/8443）+ `security: none`** | CF 用 `https://` 回源，TLS ClientHello 打到明文 HTTP 监听端口 → 无法解析 → CF 侧 **525**（"CF 与源站之间 SSL 握手失败"）| 官方端口分类 + Full + 525 文档 |
| 4 | **橙云 + 入站监听"13 端口表"以外的端口**（例如自建面板常见的 10086、12345、随机高位端口） | CF 根本不代理该端口，橙云下不可达；须灰云或 Enterprise Spectrum | 官方 Network ports 文档 |
| 5 | **`Full (strict)` + 源站自签证书** | Full (strict) 要求源站证书未过期、由公有 CA 或 CF Origin CA 签发且 CN/SAN 匹配；自签不满足 → **526** | 官方 Full (strict) 文档 |

### 8.2 从原理上**可以**工作、但需要条件齐备的组合

| # | 组合 | 需要满足的条件 |
| --- | --- | --- |
| A | 443 + `ws` + `tls`（源站真证书或 **CF Origin CA**）+ SSL 模式 `Full (strict)` | 客户端 `serverName`/`host` = 你自己的域名；WS `path` 非特征；CF 边缘有覆盖该域名/子域的证书（Universal SSL 覆盖顶点 + 一级通配，更深层级需 Advanced Cert / Total TLS） |
| B | 443 + `ws` + `tls`（**自签**）+ SSL 模式 `Full` | 仅 CF 不校验源站证书；安全性弱于 A |
| C | 2053 / 2083 / 2087 / 2096 / 8443 + `ws`/`xhttp` + `tls` + 源站证书 | 同上；**注意这些端口官方禁用缓存**，部分 CF IP 上可能禁用非标端口（社区说法），China Network 不可用 |
| D | 80 / 8080 / 8880 / 2052 / 2082 / 2086 / 2095 + `ws` + `security: none` + SSL 模式 `Flexible`/`Off`/`Full` | 可用，但**客户端↔CF 明文**；建议仅作兜底 |
| E | 灰云（DNS only）+ 任意端口 + `security: reality`（或任意协议） | 绕过 CF，客户端直连源站；这是 REALITY 唯一正确的用法。代价：失去 CF 隐藏源站 IP / 抗封锁能力 |

### 8.3 建议的排查顺序（把这个案例代进去）

1. **先看端口**：橙云记录对应的源站监听端口是否在 `80/8080/8880/2052/2082/2086/2095/443/2053/2083/2087/2096/8443` 之内？不在 → 一切免谈（先改端口、加 Origin Rules 覆盖目标端口、或改灰云）。
2. **再看端口类别与入站 `security` 是否匹配**（第 4.4 节速查表）：HTTP 类端口必须配 `security: none`；HTTPS 类端口必须配 `security: tls`。**这是最常见、也最容易在面板里被一键脚本配错的组合错配。**
3. **再看协议**：入站是否是 `reality`？是 → 橙云下必然失败，必须改灰云直连，或把协议换成 `vless/vmess + ws/xhttp + tls`。
4. **再看 SSL 模式与源站证书是否自洽**：自签 → 只能用 `Full`（或 `Flexible`，但 Flexible 只对 443 的 HTTPS 生效）；要上 `Full (strict)` → 必须装 **CF Origin CA**（推荐，免费、15 年）或公有 CA 证书，且 CN/SAN 覆盖所用域名。
5. **再看客户端**：`address`/端口/`serverName`（SNI）/`host` 是否与"你自己的域名"一致；`path` 是否与服务端一致；WS 是否做了心跳（CF 会因空闲关闭长连接）；`serverName` 千万不要写成优选域名本身。
6. **区分"弃用警告"与"真故障"**：日志里出现 `WebSocket transport … is deprecated` / `VMess … is deprecated` **只是警告**，不要据此判定协议不可用；出现 `The feature HTTP transport … has been removed`（`h2`/`h3`/`http`）才是**配置级致命错误**；另外 26.x 的新默认（如 REALITY `minClientVer`、XHTTP flow 字段）会导致老客户端连不上，需与面板版本一起排查（[3x-ui#5922](https://github.com/MHSanaei/3x-ui/issues/5922)、[#6004](https://github.com/MHSanaei/3x-ui/issues/6004)）。

### 8.4 与本案例最可能相关的三条"一句话判据"

- **判据 1**：如果这个面板的节点是 `reality` 且域名是橙云 → **不可能可用**，与端口、证书、面板版本都无关。
- **判据 2**：如果节点是 `ws + tls` 但监听在 `2052`（或任何 HTTP 类端口）→ **不可能可用**；反过来 `ws` 监听在 `2087` 却把入站设成明文 → **不可能可用**。二者都会在 CF 侧留下 **525** 或源站侧留下"收到非 HTTP 数据/握手失败"的痕迹。
- **判据 3**：如果节点是 `ws + tls`、监听 `443`、源站自签、SSL 模式 `Full` → **应该可用**；此时"不可用"就要往客户端 SNI/Host、WS path、CF 边缘证书覆盖范围、以及"优选域名/优选 IP 是否把非标端口打不通"这些方向查，而不是怀疑端口清单。

---

## 附：本报告引用来源总表

### 官方文档（Cloudflare）
- [Network ports（端口清单，含 HTTP/HTTPS 分类与缓存禁用列表）](https://developers.cloudflare.com/fundamentals/reference/network-ports/)
- [WebSockets（所有套餐支持、Argo 不兼容、空闲超时）](https://developers.cloudflare.com/network/websockets/)
- [Cloudflare origin CA（仅 CF↔源站、免费版可用、无到期通知、200 SAN、无 IP SAN）](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/)
- [Origin CA Create Certificate API（`requested_validity` 枚举 7/30/90/365/730/1095/5475）](https://developers.cloudflare.com/api/resources/origin_ca_certificates/methods/create/)
- [Encryption modes（总览）](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/)
- [Full（跟随访客 scheme、不校验源站证书）](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full/)
- [Full (strict)（校验源站证书，公有 CA 或 CF Origin CA）](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)
- [Flexible（CF↔源站明文；仅 443 的 HTTPS 生效，其他端口回落 Full）](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/flexible/)
- [Error 525: SSL handshake failed](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-525/)
- [Certificate and hostname priority（SNI match → wildcard → IP）](https://developers.cloudflare.com/ssl/reference/certificate-and-hostname-priority/)
- [Validity periods and renewal（Universal SSL 90 天、Advanced 可选期）](https://developers.cloudflare.com/ssl/reference/certificate-validity-periods/)
- [Origin Rules（免费版仅支持 Override destination port）](https://developers.cloudflare.com/rules/origin-rules/)
- [Cloudflare IP addresses](https://developers.cloudflare.com/fundamentals/concepts/cloudflare-ip-addresses/)

### 官方文档 / 官方仓库（Xray）
- [Xray REALITY 传输安全文档](https://xtls.github.io/en/config/transports/reality.html)（含"REALITY 是改造过的 TLS""客户端自定义证书校验""只支持 RAW/XHTTP/gRPC""target 在 CF 后面会被当成 CF 的端口转发器"）
- [Xray XHTTP 文档（"XHTTP: Beyond REALITY"）](https://xtls.github.io/en/config/transports/xhttp.html) → 官方讨论 [XTLS/Xray-core#4113](https://github.com/XTLS/Xray-core/discussions/4113)
- [Xray RAW 传输文档](https://xtls.github.io/en/config/transports/raw.html)
- [`common/errors/feature_errors.go`（三类弃用输出函数）](https://github.com/XTLS/Xray-core/blob/main/common/errors/feature_errors.go)
- [commit cd8aab9（WS/VMess/SS/Trojan/gRPC/HTTPUpgrade 的精确警告文本与迁移目标）](https://github.com/XTLS/Xray-core/commit/cd8aab9544bc35fbc9990f61cb3613d0abbbe775)
- [PR #5567（引入 `PrintNonRemovalDeprecatedFeatureWarning`）](https://github.com/XTLS/Xray-core/pull/5567)
- [Xray-core v26.1.23 release notes（弃用警告、`pinnedPeerCertSha256`、REALITY 真证书告警）](https://newreleases.io/project/github/XTLS/Xray-core/release/v26.1.23)

### 项目 issue / PR
- [chika0801/Xray-examples#49：VLESS-XHTTP-REALITY 无法通过 Cloudflare CDN（含 xray 日志）](https://github.com/chika0801/Xray-examples/issues/49)
- [233boy/v2ray#1387：v24.12.15 实际打印的 WS 弃用警告原文](https://github.com/233boy/v2ray/issues/1387)
- [MHSanaei/3x-ui#3678：Xray 26.1.18 XHTTP 无法设置 flow 的日志警告](https://github.com/MHSanaei/3x-ui/issues/3678)
- [MHSanaei/3x-ui#3712：XHTTP + REALITY 入站用户缺少 flow 字段](https://github.com/MHSanaei/3x-ui/issues/3712)
- [MHSanaei/3x-ui#3841：need VLESS with Flow & Seed](https://github.com/MHSanaei/3x-ui/issues/3841)
- [MHSanaei/3x-ui#5922：REALITY 在 26.7.11 拒绝老客户端（新增默认 `minClientVer`）](https://github.com/MHSanaei/3x-ui/issues/5922)
- [MHSanaei/3x-ui#6004：REALITY/VLESS 新连接在 v26.7.11 失败、降级到 v26.6.27 恢复](https://github.com/MHSanaei/3x-ui/issues/6004)
- [Openwrt-Passwall/openwrt-passwall2#999：升级 26.1.31 后 passwall2 中断（allowInsecure 相关）](https://github.com/Openwrt-Passwall/openwrt-passwall2/issues/999)

### 社区 / 博客（可信度分级）
- **中高**：[BiuPing《Cloudflare 优选 IP 完全指南：原理、测速方法、自动化脚本与避坑清单》](https://www.biuping.com/article/23)（SNI/Host 路由机制、避坑 12 条）
- **中高**：[丙醛说《CF 中转两种姿势：端口回源过 CDN vs Argo Tunnel》](https://blog.hbqfkj.cn/2026/09/self-node-cf-access-argo-vs-cdn-2026/)（端口分两类、Argo 边缘终结 TLS、源站必须监听受支持端口）
- **中**：[lvshuqi.top《Cloudflare 非标 HTTPS 端口支持列表》](https://lvshuqi.top/2025/09/29/Cloudflare%E9%9D%9E%E6%A0%87HTTPS%E7%AB%AF%E5%8F%A3%E6%94%AF%E6%8C%81%E5%88%97%E8%A1%A8/)（端口分组、缓存/China Network 限制的转述）
- **中**：[yet-another-dev-blog《Почему лучше не использовать dest за CDN в XTLS-Reality》](https://yet-another-dev-blog.cc/post/reality-avoid-cdn)（REALITY 与 CDN 交互、SNI 白名单缓解）
- **中**：[chonglangbiji《XHTTP / Splithttp 新一代传输层完整指南（2026版）》](https://www.chonglangbiji.com/clients/xhttp/)
- **中**：[Yulinanami/my-xhttp-cdn-config（XHTTP+CDN 上下行分离实操）](https://github.com/Yulinanami/my-xhttp-cdn-config)
- **中**：[jc-lw/youxuanyuming（自建优选域名）](https://github.com/jc-lw/youxuanyuming)、[1molchuan/edge-smart-doh](https://github.com/1molchuan/edge-smart-doh)
- **仅供参考**：[panelica《Cloudflare Origin Certificate: Why 15 Years and How to Set It Up (2026)》](https://www.panelica.com/blog/cloudflare-origin-certificate-15-years-why-and-how-2026)（"15 年默认"的旁证）
- **未能取证**：`community.cloudflare.com` 的相关帖子（本次抓取被 HTTP 403 Cloudflare 挑战页拦住，仅见搜索摘要）：
  - `https://community.cloudflare.com/t/proxy-doesnt-work-on-port-2087/584987`
  - `https://community.cloudflare.com/t/ssl-error-err-ssl-protocol-error-with-port-number/529183`
