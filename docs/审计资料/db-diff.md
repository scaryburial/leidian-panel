# 数据库结构化差异分析报告：面板创建的代理协议为什么不可用

- **A = 「曾经可用」备份**：`C:\Users\Administrator\.dsh\attachments\v1\files\44\44e7a7f66142a4375da559a53e739b4bbf4cfd89896a14b5252347a17696f8df\3344.578272.xyz_2026-10-03_204445.db`
  - 服务器 IP `82.29.36.89`，域名 `3344.578272.xyz`，panelGuid `4fc051fc-c895-4d49-b3be-0c4386ddec60`
- **B = 当前出问题快照**：`C:\Users\Administrator\ui\leidian-install\server-current.db`
  - 服务器 IP `154.12.82.50`，域名 `49pwzx78.578272.xyz`，panelGuid `2b6df7c6-dbec-45a5-887e-0d25b818d641`
  - 面板 v1.7 全新安装（install.sh，`UI3344_TAG=v1.7`），安装后由 `create-inbounds.py` 自动生成 9 个入站（安装日志：`ui3344 presets: created=9 skipped=0 failed=0`）

**取证方式**：两个库均以 `sqlite3` URI `mode=ro&immutable=1` 只读打开，未做任何写操作；全程未连接远程服务器。

**两份库的表结构完全一致**（列名、列顺序、索引均相同），因此差异全部是**数据差异**，不是 schema 漂移。唯一列顺序差异在 `client_external_links`（A 为 `...sort_index,created_at,allow_private`，B 为 `...sort_index,allow_private,created_at`），该表两边均 0 行，无影响。

## 行数总览

| 表 | A 行数 | B 行数 | 差异 |
|---|---|---|---|
| settings | **153** | **19** | **B 极度不完整（-134 键）** |
| inbounds | 13 | 9 | B 少 5 个入站（2082/2083/2086/2095/8888），多 1 个（8444） |
| hosts | **52** | **24** | B 的 hosts 只挂到 1 个入站（A 挂 2 个） |
| clients | 3 | 10 | 两边是不同用户体系 |
| client_inbounds | 36 | 18 | A = 3 客户端 × 12 入站；B = 9×1 + 1×9 |
| client_traffics | 3 | 10 | — |
| inbound_client_ips | 2 | 1 | — |
| node_client_ips | 2 | 1 | — |
| users | 1 | 1 | — |

---

# 1. settings 表全量对比

## 1.1 总览

| 项 | A | B |
|---|---|---|
| 总行数 / distinct key | 153 / 153 | 19 / 19 |
| 仅 A 有的键 | **134 个** | — |
| 仅 B 有的键 | — | **0 个** |
| 共有键 | 19 个 | 19 个 |
| 共有且值不同 | **11 个** | 11 个 |
| 共有且值相同 | 8 个 | 8 个 |

> **结论性事实**：B 是全新安装的默认库，**没有从 A 迁移任何扩展设置**。A 的 153 个键里有 134 个在 B 完全缺失。B 只保留了安装器写入的 19 个键。

## 1.2 共有键中「值不同」的 11 个（逐行）

| key | A 值 | B 值 | 影响面 |
|---|---|---|---|
| `domainConfig` | `{"enabled":true,"fqdn":"3344.578272.xyz","ip":"82.29.36.89","recordId":"a10739d8ab4d30d2cd2b4919e44bcd2e","certMode":"origin-ca","certPath":"/root/cert/3344.578272.xyz","proxied":true}` | `{"enabled":true,"fqdn":"49pwzx78.578272.xyz","ip":"154.12.82.50","recordId":"f246234a22477e6cf73674163260a045","certMode":"origin-ca","certPath":"/root/cert/49pwzx78.578272.xyz","proxied":true,"preferredDomains":[...24 项...],"preferredRejectedCount":10,"panel":{"prevPanelPort":33441,"panelMoved":true,"parkedInboundId":1,"parkedInboundNewPort":8444}}` | 域名/证书/CF 优选/面板搬迁 |
| `panelGuid` | `4fc051fc-c895-4d49-b3be-0c4386ddec60` | `2b6df7c6-dbec-45a5-887e-0d25b818d641` | 面板身份 |
| `secret` | `iJgr4m2aF9IeT94hc3V0kkOjYqenEbyq` | `OFfPRs9ga3Tkih96Q0jMlZAlgm51WV8V` | 会话密钥 |
| `subCertFile` | `/root/cert/panel/fullchain.pem` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | 订阅证书 |
| `subKeyFile` | `/root/cert/panel/privkey.pem` | `/root/cert/49pwzx78.578272.xyz/privkey.pem` | 订阅证书 |
| `webCertFile` | `/root/cert/panel/fullchain.pem` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | 面板证书 |
| `webKeyFile` | `/root/cert/panel/privkey.pem` | `/root/cert/49pwzx78.578272.xyz/privkey.pem` | 面板证书 |
| `subDomain` | `3344.578272.xyz` | `49pwzx78.578272.xyz` | 订阅域名 |
| `subPath` | `/mby93v9t1rjr3o0m/` | `/ewspxcpprwwe6r4y/` | 订阅路径 |
| `subJsonPath` | `/b7qixbozcezf6b8l/` | `/zwsfa1wb09vbnxzn/` | JSON 订阅路径 |
| `subClashPath` | `/ingr6y4ogurvo7ea/` | `/w2s65mxpgrelznkh/` | Clash 订阅路径 |

## 1.3 共有键中「值相同」的 8 个

| key | 双方相同值 |
|---|---|
| `domainEnabled` | `true` |
| `subEnable` | `true` |
| `subClashEnable` | `true` |
| `subClashEnableRouting` | `true` |
| `subJsonEnable` | `true` |
| `subTitle` | `雷电面板` |
| `webPort` | `8443` |
| `subClashRules` | `DOMAIN-SUFFIX,doubleclick.net,REJECT\nDOMAIN-SUFFIX,googlesyndication.com,REJECT\nDOMAIN-SUFFIX,googleadservices.com,REJ...`(451 字符) |

## 1.4 任务点名的关键键：逐个核对

| key | A | B | 判定 |
|---|---|---|---|
| `domainConfig` | 见上 | 见上 | **不同**，B 多出 `preferredDomains`/`preferredRejectedCount`/`panel` |
| `domainEnabled` | `true` | `true` | 相同 |
| `subEnable` | `true` | `true` | 相同 |
| `subPort` | `2096` | **`<<ABSENT>>`** | **B 缺失（关键）** |
| `subPath` | `/mby93v9t1rjr3o0m/` | `/ewspxcpprwwe6r4y/` | 不同（路径本身正常） |
| `subCertFile` | `/root/cert/panel/fullchain.pem` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | 不同 |
| `subKeyFile` | `/root/cert/panel/privkey.pem` | `/root/cert/49pwzx78.578272.xyz/privkey.pem` | 不同 |
| `webPort` | `8443` | `8443` | 相同 |
| `webBasePath` | `/ui3344/` | **`<<ABSENT>>`** | **B 缺失** |
| `panelGuid` | `4fc051fc-...` | `2b6df7c6-...` | 不同（全新安装必然不同） |
| `realityScanCandidates` | `www.cloudflare.com:443,www.microsoft.com:443,www.amazon.com:443,aws.amazon.com:443,www.samsung.com:443,www.nvidia.com:443,www.amd.com:443,www.intel.com:443,www.sony.com:443,dl.google.com:443` | **`<<ABSENT>>`** | **B 缺失** |
| `xrayTemplateConfig` | 完整 JSON（见 1.6，1821 字符） | **`<<ABSENT>>`** | **B 缺失** |
| `webDomain` | ``（空） | **`<<ABSENT>>`** | B 缺失 |
| `webListen` | ``（空） | **`<<ABSENT>>`** | B 缺失 |
| `subListen` | ``（空） | **`<<ABSENT>>`** | B 缺失 |
| `subURI` | ``（空） | **`<<ABSENT>>`** | B 缺失 |
| `domainOtpSecret` | `CKSN****[已脱敏]****U2KQ` | **`<<ABSENT>>`** | B 缺失 |

## 1.5 B 中 `domainConfig` 的完整值（未截断）

```json
{
  "enabled": true,
  "fqdn": "49pwzx78.578272.xyz",
  "ip": "154.12.82.50",
  "recordId": "f246234a22477e6cf73674163260a045",
  "certMode": "origin-ca",
  "certPath": "/root/cert/49pwzx78.578272.xyz",
  "proxied": true,
  "preferredDomains": [
    "time.is", "icook.hk", "icook.tw", "ip.sb", "japan.com", "malaysia.com",
    "russia.com", "singapore.com", "skk.moe", "www.visa.com.sg", "www.visa.com.hk",
    "www.visa.com.tw", "www.visa.co.jp", "www.visakorea.com", "www.gco.gov.qa",
    "www.gov.se", "www.gov.ua", "1.cf.cname.vvhan.com", "1.cf.959923.xyz",
    "xn--b6gac.eu.org", "cfip.1323123.xyz", "cf.0sm.com", "cf.877771.xyz",
    "cf.345673.xyz"
  ],
  "preferredRejectedCount": 10,
  "panel": {
    "prevPanelPort": 33441,
    "panelMoved": true,
    "parkedInboundId": 1,
    "parkedInboundNewPort": 8444
  }
}
```

**关键解读**：
1. `"proxied": true` —— B 的域名**开着小黄云（Cloudflare 代理）**。A 同样是 `true`，所以这一点两边一致，**不是差异来源**，但它决定了后面「REALITY vs CF 代理」的冲突判定。
2. `preferredDomains` 这 24 个第三方域名 **== B 的 hosts 表里除自己的域名外的 23 个 address + 1 个**（对照第 3 节）。即 hosts 的 CF 优选地址是这份 `preferredDomains` 派生的，**A 也有同样的 hosts 但 A 的 domainConfig 里没有 preferredDomains 字段**（A 是更早版本写入的，或已从设置里移除）。
3. `panel.panelMoved = true`、`prevPanelPort = 33441`、`parkedInboundNewPort = 8444` —— 面板自己把原先占用的入站「挪走」了：安装时面板跑在 `33441`，面板服务实际端口被改成 `webPort = 8443`（settings 两边都是 8443），而**原本占用端口的面板入站被改挂到 8444**（见第 2 节 `inbound_id=1`）。

## 1.6 A 的 `xrayTemplateConfig` 完整值

```json
{
  "api": { "services": ["HandlerService","LoggerService","StatsService","RoutingService"], "tag": "api" },
  "inbounds": [ { "listen": "127.0.0.1", "port": 62789, "protocol": "tunnel",
                  "settings": { "rewriteAddress": "127.0.0.1" }, "tag": "api" } ],
  "log": { "access": "none", "dnsLog": false, "error": "", "loglevel": "warning", "maskAddress": "" },
  "metrics": { "listen": "127.0.0.1:11111", "tag": "metrics_out" },
  "outbounds": [
    { "protocol": "freedom",
      "settings": { "finalRules": [ { "action": "block", "ip": ["geoip:private"] }, { "action": "allow" } ] },
      "tag": "direct" },
    { "protocol": "blackhole", "settings": {}, "tag": "blocked" }
  ],
  "policy": { "levels": { "0": { "statsUserDownlink": true, "statsUserUplink": true } },
              "system": { "statsInboundDownlink": true, "statsInboundUplink": true,
                          "statsOutboundDownlink": false, "statsOutboundUplink": false } },
  "routing": { "domainStrategy": "AsIs",
    "rules": [
      { "inboundTag": ["api"], "outboundTag": "api", "type": "field" },
      { "ip": ["geoip:private"], "outboundTag": "blocked", "type": "field" },
      { "outboundTag": "blocked", "protocol": ["bittorrent"], "type": "field" }
    ] },
  "stats": {}
}
```

B 中该键**完全不存在**，意味着 B 使用面板内置的默认 Xray 模板。这一项**不是**协议不可用的直接原因，但说明 B 是可被覆写的全新状态。

## 1.7 仅 A 有的 134 个键（完整清单）

`datepicker`, `discordAdminIds`, `discordBotBackup`, `discordBotEnable`, `discordBotToken`, `discordChannelId`, `discordCpu`, `discordEnabledEvents`, `discordLang`, `discordMemory`, `discordRunTime`, `domainOtpSecret`, `expireDiff`, `externalTrafficInformEnable`, `externalTrafficInformURI`, `happLinkEnable`, `ipLimitAllowlist`, `ldapAutoCreate`, `ldapAutoDelete`, `ldapBaseDN`, `ldapBindDN`, `ldapDefaultExpiryDays`, `ldapDefaultLimitIP`, `ldapDefaultTotalGB`, `ldapEnable`, `ldapFlagField`, `ldapHost`, `ldapInboundTags`, `ldapInsecureSkipVerify`, `ldapInvertFlag`, `ldapPassword`, `ldapPort`, `ldapSyncCron`, `ldapTruthyValues`, `ldapUseTLS`, `ldapUserAttr`, `ldapUserFilter`, `ldapVlessField`, `outboundDownThreshold`, `pageSize`, `panelOutbound`, **`realityScanCandidates`**, `relayConfig`, `remarkTemplate`, `restartXrayOnClientDisable`, `sessionMaxAge`, `smtpCpu`, `smtpEnable`, `smtpEnabledEvents`, `smtpEncryptionType`, `smtpFrom`, `smtpFromName`, `smtpHost`, `smtpMemory`, `smtpPassword`, `smtpPort`, `smtpTo`, `smtpUsername`, `subAnnounce`, `subCalendarExpireInclusive`, `subClashAutoDetect`, `subClashURI`, `subClashUserAgentRegex`, `subEnableRouting`, `subEncrypt`, `subExpiredTemplate`, `subHappAlwaysHwid`, `subHappAutoConnect`, `subHappAutoConnectType`, `subHappAutoDetect`, `subHappColorProfile`, `subHappExcludeApns`, `subHappExcludeRoutes`, `subHappFallbackUrl`, `subHappNewUrl`, `subHappNoLimit`, `subHappNotificationExpire`, `subHappPerAppList`, `subHappPerAppMode`, `subHappPingType`, `subHappProviderId`, `subHappSubExpire`, `subHappSubExpireButtonLink`, `subHappSubInfoButtonLink`, `subHappSubInfoButtonText`, `subHappSubInfoColor`, `subHappSubInfoText`, `subHappTunMode`, `subHappTunType`, `subHideSettings`, `subIncyEnableRouting`, `subIncyRoutingRules`, `subInfoNodeEnable`, `subJsonAlwaysArray`, `subJsonAutoDetect`, `subJsonDns`, `subJsonFinalMask`, `subJsonMux`, `subJsonObservatory`, `subJsonRoutingRules`, `subJsonRules`, `subJsonURI`, `subJsonUserAgentRegex`, **`subListen`**, **`subPort`**, `subProfileUrl`, `subRoutingRules`, `subShowIdentityOnAllLinks`, `subSupportUrl`, `subThemeDir`, `subTrafficDepletedTemplate`, **`subURI`**, `subUpdates`, `tgBotAPIServer`, `tgBotBackup`, `tgBotChatId`, `tgBotEnable`, `tgBotProxy`, `tgBotToken`, `tgCpu`, `tgEnabledEvents`, `tgLang`, `tgMemory`, `tgRunTime`, `timeLocation`, `trafficDiff`, `trustedProxyCIDRs`, `twoFactorEnable`, `twoFactorToken`, `warpUpdateInterval`, **`webBasePath`**, **`webDomain`**, **`webListen`**, **`xrayTemplateConfig`**

其中与「证书/订阅/端口/域名」直接相关的已加粗：`realityScanCandidates`、`subListen`、`subPort`、`subURI`、`webBasePath`、`webDomain`、`webListen`、`xrayTemplateConfig`、`domainOtpSecret`。

> **B 仅有的键：0 个**（B 的 19 个键是 A 的子集）。

---

# 2. inbounds 表逐条对比

## 2.1 端口配对表

| port | A | B | A remark | B remark |
|---|---|---|---|---|
| 443 | ✅ | ✅ | `443` | `VLESS-安全-Reality-443` |
| 2052 | ✅ | ✅ | `VMess-综合-WS-2052` | `VMess-综合-WS-2052` |
| 2053 | ✅ | ✅ | `VMess-安全-TLS-2053` | `VMess-安全-TLS-2053` |
| 2082 | ✅ | ❌ | `CF优选-2082-WS-VMess` | — |
| 2083 | ✅ | ❌ | `CF优选-2083-TLS-VLESS` | — |
| 2086 | ✅ | ❌ | `CF优选-2086-WS-VMess` | — |
| 2087 | ✅ | ✅ | `VLESS-综合-WS-2087` | `VLESS-综合-WS-2087` |
| 2095 | ✅ | ❌ | `CF优选-2095-WS-VMess` | — |
| 8080 | ✅ | ✅ | `VMess-速度-8080` | `VMess-速度-8080` |
| 8388 | ✅ | ✅ | `SS2022-速度-8388` | `SS2022-速度-8388` |
| 8389 | ✅ | ✅ | `SS2022-安全-8389` | `SS2022-安全-8389` |
| 8390 | ✅ | ✅ | `SS2022-综合-8390` | `SS2022-综合-8390` |
| 8444 | ❌ | ✅ | — | `VLESS-速度-8443` |
| 8888 | ✅ | ❌ | `SOCKS5-8888` | — |

- A 端口集合：`[443, 2052, 2053, 2082, 2083, 2086, 2087, 2095, 8080, 8388, 8389, 8390, 8888]`（13）
- B 端口集合：`[443, 2052, 2053, 2087, 8080, 8388, 8389, 8390, 8444]`（9）
- **仅 A 有**：`2082, 2083, 2086, 2095, 8888` —— 即 **4 个「CF优选」入站全部消失**，外加 SOCKS5
- **仅 B 有**：`8444`
- A tags：`in-2052-vmess, in-2053-vmess, in-2082-vmess, in-2083-vless, in-2086-vmess, in-2087-vless, in-2095-vmess, in-443-tcp, in-8080-vmess, in-8388-shadowsocks, in-8389-shadowsocks, in-8390-shadowsocks, in-socks8888`
- B tags：`in-2052-vmess, in-2053-vmess, in-2087-vless, in-443-vless, in-8080-vmess, in-8388-shadowsocks, in-8389-shadowsocks, in-8390-shadowsocks, in-8443-vless`

## 2.2 逐条明细（id / tag / remark / port / protocol / enable / network / security / serverName / certFile / ws path / ws host / REALITY / sniffing / method 或 clients 数）

「—」表示该字段不存在或不适用。`enable` 两边**全部入站都为 1**。

### A（13 条）

| id | tag | remark | port | protocol | enable | network | security | serverName | certificateFile / keyFile | ws path | ws host | sniffing | clients 数 / method |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 11 | `in-443-tcp` | `443` | 443 | vmess | 1 | **ws** | **tls** | `3344.578272.xyz` | `/root/cert/panel/fullchain.pem` / `.../privkey.pem` | `/gghh` | `3344.578272.xyz` | `{"enabled": false}` | 3 / — |
| 6 | `in-2052-vmess` | `VMess-综合-WS-2052` | 2052 | vmess | 1 | ws | tls | `3344.578272.xyz` | `/root/cert/3344.578272.xyz/fullchain.pem` / `.../privkey.pem` | `/ui3344vm` | `3344.578272.xyz` | enabled=true, destOverride=[http,tls,quic] | 3 / — |
| 5 | `in-2053-vmess` | `VMess-安全-TLS-2053` | 2053 | vmess | 1 | tcp | tls | `ui3344` | `/root/cert/ui3344-selfsigned/fullchain.pem` / `.../privkey.pem` | — | — | enabled=true | 3 / — |
| 13 | `in-2082-vmess` | `CF优选-2082-WS-VMess` | 2082 | vmess | 1 | ws | **none** | — | — | `/ui3344cf82` | **`3344.578272.xyz`** | enabled=true | 3 / — |
| 12 | `in-2083-vless` | `CF优选-2083-TLS-VLESS` | 2083 | vless | 1 | ws | tls | `3344.578272.xyz` | `/root/cert/3344.578272.xyz/fullchain.pem` / `.../privkey.pem` | `/ui3344cf83` | `3344.578272.xyz` | enabled=true | 3 / — |
| 14 | `in-2086-vmess` | `CF优选-2086-WS-VMess` | 2086 | vmess | 1 | ws | **none** | — | — | `/ui3344cf86` | **`3344.578272.xyz`** | enabled=true | 3 / — |
| 3 | `in-2087-vless` | `VLESS-综合-WS-2087` | 2087 | vless | 1 | ws | tls | `3344.578272.xyz` | `/root/cert/3344.578272.xyz/fullchain.pem` / `.../privkey.pem` | `/ui3344ws` | `3344.578272.xyz` | enabled=true | 3 / — |
| 15 | `in-2095-vmess` | `CF优选-2095-WS-VMess` | 2095 | vmess | 1 | ws | **none** | — | — | `/ui3344cf95` | **`3344.578272.xyz`** | enabled=true | 3 / — |
| 4 | `in-8080-vmess` | `VMess-速度-8080` | 8080 | vmess | 1 | tcp | none | — | — | — | — | enabled=true | 3 / — |
| 7 | `in-8388-shadowsocks` | `SS2022-速度-8388` | 8388 | shadowsocks | 1 | tcp | none | — | — | — | — | enabled=true | 3 / **`2022-blake3-aes-128-gcm`** |
| 8 | `in-8389-shadowsocks` | `SS2022-安全-8389` | 8389 | shadowsocks | 1 | tcp | none | — | — | — | — | enabled=true | 3 / **`2022-blake3-aes-256-gcm`** |
| 9 | `in-8390-shadowsocks` | `SS2022-综合-8390` | 8390 | shadowsocks | 1 | tcp | none | — | — | — | — | enabled=true | 3 / **`2022-blake3-aes-256-gcm`** |
| 10 | `in-socks8888` | `SOCKS5-8888` | 8888 | mixed | 1 | tcp | none | — | — | — | — | enabled=true, destOverride=[http,tls,quic,fakedns] | **无 clients 字段**，`settings={"auth":"password","accounts":[{"user":"888888","pass":"888888"}],"udp":true,"ip":""}` |

### B（9 条）

| id | tag | remark | port | protocol | enable | network | security | serverName | certificateFile / keyFile | ws path | ws host | REALITY | sniffing | clients 数 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 2 | `in-443-vless` | `VLESS-安全-Reality-443` | 443 | vless | 1 | **tcp** | **reality** | — | — | — | — | dest=`www.apple.com:443`, serverNames=`["www.apple.com"]`, shortIds=`["4331fefa"]`, privateKey 43 字符, `settings.publicKey`=`eAjFGKnyDJEVIS-wG5O7us8U5WSxmh5rJgIPuOQF1js`, `settings.serverName`=**空串**, spiderX=`/`, maxTimediff=0 | enabled=true | 2 |
| 6 | `in-2052-vmess` | `VMess-综合-WS-2052` | 2052 | vmess | 1 | ws | **none** | — | — | `/ui3344vm` | **空串 `""`** | — | enabled=true | 2 |
| 5 | `in-2053-vmess` | `VMess-安全-TLS-2053` | 2053 | vmess | 1 | tcp | tls | `ui3344` | `/root/cert/ui3344-selfsigned/fullchain.pem` / `.../privkey.pem` | — | — | — | enabled=true | 2 |
| 3 | `in-2087-vless` | `VLESS-综合-WS-2087` | 2087 | vless | 1 | ws | tls | `49pwzx78.578272.xyz` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` / `.../privkey.pem` | `/ui3344ws` | `49pwzx78.578272.xyz` | — | enabled=true | 2 |
| 4 | `in-8080-vmess` | `VMess-速度-8080` | 8080 | vmess | 1 | tcp | none | — | — | — | — | — | enabled=true | 2 |
| 7 | `in-8388-shadowsocks` | `SS2022-速度-8388` | 8388 | shadowsocks | 1 | tcp | none | — | — | — | — | — | enabled=true | 2 / `2022-blake3-aes-128-gcm` |
| 8 | `in-8389-shadowsocks` | `SS2022-安全-8389` | 8389 | shadowsocks | 1 | tcp | none | — | — | — | — | — | enabled=true | 2 / `2022-blake3-aes-256-gcm` |
| 9 | `in-8390-shadowsocks` | `SS2022-综合-8390` | 8390 | shadowsocks | 1 | tcp | none | — | — | — | — | — | enabled=true | 2 / `2022-blake3-aes-256-gcm` |
| 1 | **`in-8443-vless`** | **`VLESS-速度-8443`** | **8444** | vless | 1 | tcp | none | — | — | — | — | — | enabled=true | 2 |

## 2.3 逐条差异（同端口对照）

| port | 差异性质 | A | B |
|---|---|---|---|
| **443** | **协议/传输/安全完全不同** | vmess + **ws** + **tls**，serverName `3344.578272.xyz`，cert `/root/cert/panel/fullchain.pem`，ws path `/gghh`，ws host `3344.578272.xyz`，sniffing **关闭** | vless + **tcp** + **reality**，**无证书**，dest `www.apple.com:443`，serverNames `["www.apple.com"]`，shortIds `["4331fefa"]`，`settings.serverName` 空，首客户端 flow `xtls-rprx-vision` |
| **2052** | security 与 ws host 变化 | ws + **tls**，serverName `3344.578272.xyz`，cert `/root/cert/3344.578272.xyz/fullchain.pem`，ws host `3344.578272.xyz` | ws + **none**（明文），**无 cert**，ws host **`""` 空串** |
| **2053** | 一致 | tcp + tls，serverName `ui3344`，selfsigned 证书 | 同上，**完全相同** |
| **2087** | 仅域名/证书替换 | ws + tls，serverName `3344.578272.xyz`，cert `/root/cert/3344.578272.xyz/...` | ws + tls，serverName `49pwzx78.578272.xyz`，cert `/root/cert/49pwzx78.578272.xyz/...`，**其余相同** |
| **8080** | 一致 | tcp + none | 相同 |
| **8388/8389/8390** | 一致（method 相同） | SS2022，method 128/256/256 | 相同 |
| **2082/2083/2086/2095** | **B 缺失** | 4 个 CF优选入站（2 个 ws+none、1 个 ws+tls VLESS、1 个 ws+none） | — |
| **8888** | **B 缺失** | mixed (SOCKS5) | — |
| **8444** | **B 新增** | — | id=1，tag **`in-8443-vless`**（tag 说 8443，实际 `port=8444`），remark `VLESS-速度-8443`，vless + tcp + **none**（明文），无证书 |

## 2.4 inbounds 层面的关键异常点

1. **B 的 443 是 REALITY，但域名开着 CF 代理**：`domainConfig.proxied=true`。REALITY 依赖客户端与源站直接完成 TLS 握手，而 `49pwzx78.578272.xyz` 的 443 会被 Cloudflare 接管，REALITY 握手不可能成功。A 的 443 是 **ws+tls**（CF 可代理形态），因此 A 的 443 可用。
2. **B 的 2052 从 tls 降级为 none**：A 的 2052 有 TLS 和合法证书，B 的 2052 变成明文 ws 且 **ws host 为空串**。如果客户端仍按 TLS/SNI 连接（订阅模板可能沿用 A 的形态），必然握手失败。
3. **B 的 2052/2053/8080/8388/8389/8390 的 `ws host` / serverName 未统一为本机域名**：2052 的 `wsSettings.host` 是空串，而 A 是 `3344.578272.xyz`。
4. **B 新增的 `inbound_id=1` 是面板搬迁产生的占位入站**：`domainConfig.panel.parkedInboundId=1, parkedInboundNewPort=8444`，`prevPanelPort=3341` 实为 `33441`。它的 tag `in-8443-vless` 与实际端口 `8444` 不一致，是**面板为了腾出 8443 给 `webPort` 而改挂**的历史残留。它在 B 的 hosts 里**没有任何 host 记录**（第 3 节），订阅不会为它生成节点。
5. **B 完全没有 2096 相关入站**：A 也没有 2096 入站，但 A 的 `subPort=2096` 让面板**自己起一个 2096 的订阅 HTTP/TLS 服务**；B 缺 `subPort`，订阅服务起不来。
6. **B 的 REALITY `settings.serverName` 为空串**，且 B 缺 `realityScanCandidates`，面板无法给出可扫的 dest 候选；若订阅生成器把这个空串写进分享链接的 `sni=`，客户端 REALITY 校验会失败。

---

# 3. hosts 表对比

## 3.1 行数与挂载关系

| 项 | A | B |
|---|---|---|
| 总行数 | **52** | **24** |
| 挂载的 inbound_id | **3（26 行）、12（26 行）** | **仅 3（24 行）** |
| group_id | `rtpxoqgte705vehh`（全部） | `gi2vv4a4nmxak7b2`（全部） |
| security 策略 | 全部 `same` | 全部 `same` |
| sni 策略 | 全部 `3344.578272.xyz`（52/52） | 全部 `49pwzx78.578272.xyz`（24/24） |
| host_header 策略 | 全部 `3344.578272.xyz` | 全部 `49pwzx78.578272.xyz` |
| port | 全部 `0` | 全部 `0` |
| path | 全部空 | 全部空 |
| alpn | 全部 **`[]`（空 JSON 数组）** | 全部 **`<NULL>`** |
| fingerprint | 空 | 空 |
| override_sni_from_address / keep_sni_blank / allow_insecure | 全 `0` | 全 `0` |
| is_disabled / is_hidden | 全 `0` | 全 `0` |
| tags / mux_params / node_guids / exclude_from_sub_types | `<NULL>` | `<NULL>` |

> **结论**：B 的 hosts **只有 24 行**，且**全部挂在 `inbound_id=3`（port 2087，VLESS-WS-TLS）**。A 的 hosts **52 行**，**同时挂在 `inbound_id=3`（2087）和 `inbound_id=12`（2083，CF优选-2083-TLS-VLESS）**。

## 3.2 address 列表对比（第三方 CF 优选域名）

**是的，A 也把 address 设成了第三方 CF 优选域名，而且比 B 更全。** 这正是任务里问的点——**这一点上 A 和 B 行为一致，不是差异来源**。

A 的 26 个 address（每个各出现 2 次，分别挂 inbound 3 和 12）：

`3344.578272.xyz`（自有域名）、`time.is`、`icook.hk`、`icook.tw`、`ip.sb`、`japan.com`、`malaysia.com`、`russia.com`、`singapore.com`、`skk.moe`、`www.visa.com.sg`、`www.visa.com.hk`、`www.visa.com.tw`、`www.visa.co.jp`、`www.visakorea.com`、`www.gco.gov.qa`、`www.gov.se`、`www.gov.ua`、`1.cf.cname.vvhan.com`、`1.cf.959923.xyz`、`xn--b6gac.eu.org`、`cfip.1323123.xyz`、`cf.0sm.com`、`cf.877771.xyz`、`cf.345673.xyz`、**`cfip.xxxxxxxx.tk`**

B 的 24 个 address（每个各出现 1 次，全部挂 inbound 3）：

`time.is`、`icook.hk`、`icook.tw`、`ip.sb`、`japan.com`、`malaysia.com`、`russia.com`、`singapore.com`、`skk.moe`、`www.visa.com.sg`、`www.visa.com.hk`、`www.visa.com.tw`、`www.visa.co.jp`、`www.visakorea.com`、`www.gco.gov.qa`、`www.gov.se`、`www.gov.ua`、`1.cf.cname.vvhan.com`、`1.cf.959923.xyz`、`xn--b6gac.eu.org`、`cfip.1323123.xyz`、`cf.0sm.com`、`cf.877771.xyz`、`cf.345673.xyz`

**集合差异**：
- B 比 A **少 2 个**：自有域名 `49pwzx78.578272.xyz`（对应 A 的 `3344.578272.xyz`）和 `cfip.xxxxxxxx.tk`
- 这 23 个第三方域名与 B 的 `domainConfig.preferredDomains`（24 项）**高度重合**：`preferredDomains` 比 hosts 多出的正是自有域名 `49pwzx78.578272.xyz`，其余 23 项一一对应
- A 的 `cfip.xxxxxxxx.tk` 是一个**明显无效的占位域名**（`xxxxxxxx`），这本不该出现在生产配置里，说明 A 的 hosts 也是批量刷入的

## 3.3 hosts 差异结论

| 差异 | A | B | 影响 |
|---|---|---|---|
| 总行数 | 52 | 24 | B 只有一半 |
| 挂载入站数 | 2 个（3=2087、12=2083） | **1 个（3=2087）** | **B 的 CF优选 TLS 入站 2083 不存在，所以无法为它生成 host** |
| 每个 address 的行数 | 2 | 1 | — |
| 含自有域名行 | 有（2 行 `3344.578272.xyz`） | **无** | B 的 hosts 里没有任何「直连自有域名」的节点，全部走第三方 CF 优选域名 |
| `cfip.xxxxxxxx.tk` | 有 | 无 | 无实质影响 |
| `alpn` | `[]` | `<NULL>` | 语义等价（都表示不覆盖 alpn） |

> **重要推断（有 DB 证据支撑）**：B 的 hosts 只挂 `inbound_id=3`，而 `inbound_id=3` 是 **2087 / ws / tls / serverName=49pwzx78.578272.xyz**。也就是说 B 面板生成的优选节点实际上是「**Cloudflare 优选地址 + SNI 49pwzx78.578272.xyz + 端口/路径取 2087 入站**」。A 的 hosts 能挂在 `inbound_id=12`（2083），是因为 A 里存在这套「CF优选」专属入站；B 的 `create-inbounds.py` 只生成了 9 个入站，**把 4 个 CF优选入站（2082/2083/2086/2095）全丢了**，导致 B 的优选列表被迫全部挤到 2087 上，节点形态与 A 的 CF优选节点形态不再等价。

---

# 4. clients / client_inbounds 表对比

## 4.1 clients 行数

| 项 | A | B |
|---|---|---|
| clients 行数 | **3** | **10** |
| client_inbounds 行数 | **36** | **18** |
| client_traffics 行数 | 3 | 10 |
| 每个客户端关联的入站数 | **每个都关联 12 个** | 9 个各自只关联 1 个；仅 `oexjuh3j76` 关联全部 9 个 |

> **两边的用户体系完全不同**：A 是「3 个真实用户 × 全部入站」，B 是「1 个管理员账号 `oexjuh3j76` 覆盖全部入站 + 9 个 per-协议测试账号各自只关联 1 个入站」。B 是全新安装生成的**测试账号结构**，不是从 A 迁移的用户。

## 4.2 A 的 3 个客户端（完整字段）

| id | email | uuid | password | auth | flow | security | sub_id | total_gb | enable |
|---|---|---|---|---|---|---|---|---|---|
| 10 | `zqrm417c3c` | `e5fab79b-3eb4-4c67-be95-ee37843e165f` | `OX+zdcXde+jDL68L2Ws2A1Di3aGLWWjgpsKR+6LyIe0=` | `x6gr6jtak74jlmyt` | （空） | `auto` | `od7jwm5zjhcvugz9` | 0 | 1 |
| 11 | `7nz056zj8g` | `5c7f7b3e-8a7a-4dbd-9c2c-678ff6201746` | `N7BW6d0kUzID898R2E1Fyl/k5Niebi5WurCOuooA6qU=` | `7q2hun8q57fw9zo9` | （空） | `auto` | `diqbqx6n33j6lj0w` | 322122547200 (300 GiB) | 1 |
| 12 | `lhx25ryjci` | `8b0c5063-9139-46b0-a98c-159bc0cd3a22` | `UVZlAw3jo5KAuLljC83jLpxiuhR0Epi2f/0rVTkbF5w=` | `v2k8oscknjgge0ij` | （空） | `auto` | `5ijc56zefre1gz9e` | 107374182400 (100 GiB) | 1 |

三个客户端的 `limit_ip=0`、`limit_hwid=0`、`expiry_time=0`、`tg_id=0`、`group_name` 空、`comment` 空、`secret` 空、`reverse` 空。

**client_inbounds（36 行）**：client_id `10`、`11`、`12` **各自关联全部 12 个入站**：`inbound_id ∈ {3,4,5,6,7,8,9,11,12,13,14,15}`，所有 `flow_override` 为空。

> 注意：A 的 13 个入站里，**`inbound_id=10`（SOCKS5-8888）没有任何 client_inbounds 记录**——SOCKS5 用固定 `accounts`（`888888/888888`）而非 clients 表。这是正常的。

**client_traffics（3 行）**——A 有真实流量，证明确实被使用过：

| id | inbound_id | email | enable | up | down | total |
|---|---|---|---|---|---|---|
| 10 | 15 | `zqrm417c3c` | 1 | 31,526,949 | 325,736,187 | 0 |
| 24 | 14 | `7nz056zj8g` | 1 | 1,726,399,324 | 30,904,823,815 | 322122547200 |
| 36 | 15 | `lhx25ryjci` | 1 | 22,412 | 176,540 | 107374182400 |

## 4.3 B 的 10 个客户端（完整字段）

| id | email | uuid | password | auth | flow | security | sub_id |
|---|---|---|---|---|---|---|---|
| 1 | `vless-speed` | `2462b423-937b-44d8-89aa-63fad19d868b` | （空） | （空） | （空） | （空） | `i3dn88zjawkgt6tv` |
| 2 | `vless-reality` | `2a092f30-68ea-479e-819d-4d7280cf068a` | （空） | （空） | **`xtls-rprx-vision`** | （空） | `m6svnfbfd6z8h8pa` |
| 3 | `vless-ws` | `4f19ec10-1127-46cd-a022-6c59dec6c975` | （空） | （空） | （空） | （空） | `xfzpviwyd3fmz1sm` |
| 4 | `vmess-speed` | `af231013-7869-47fa-baa8-f44a92b1e5d7` | （空） | （空） | （空） | `auto` | `arru51oo39tl0yhj` |
| 5 | `vmess-tls` | `ec47948c-f048-41bb-bf42-9d0cc47384c5` | （空） | （空） | （空） | `auto` | `pzzz2ab9oc8uhq17` |
| 6 | `vmess-ws` | `7763dc15-0203-4d7d-aedb-26406f7de2e2` | （空） | （空） | （空） | `auto` | `936e61kxco8exrq6` |
| 7 | `ss-speed` | **（空）** | `NhSXaag1EFcW2uAMWSEbqw==` | （空） | （空） | （空） | `aa47sqww90t2jqhu` |
| 8 | `ss-secure` | **（空）** | `loMk5Krsm8DW+NMGWbhpgsbYB3jpzArcG7hf9N6gvVs=` | （空） | （空） | （空） | `kvlctdqy48m8hlix` |
| 9 | `ss-balanced` | **（空）** | `ylsJ5BzGgf55IPSyKoqK2lxoPrlazcrpmMjh9R9jNJY=` | （空） | （空） | （空） | `ynom75mibtf9bvdq` |
| 10 | `oexjuh3j76` | `8bae10b0-4d34-4e1d-9291-663505e4449b` | `5kYOCp2p0SUuqNalNnLIDoWa7SSxLf3+K7xt2HjBM+c=` | `qcka9qfmhv2w3cia` | （空） | `auto` | `zfu4a4fs6rhb8jzq` |

全部 `limit_ip=0`、`limit_hwid=0`、`total_gb=0`、`expiry_time=0`、`tg_id=0`、`group_name` 空、`comment` 空、`secret` 空、`enable=1`。

**client_inbounds（18 行）**：

| client_id | inbound_id | flow_override |
|---|---|---|
| 1 (`vless-speed`) | 1 | （空） |
| 2 (`vless-reality`) | 2 | **`xtls-rprx-vision`** |
| 3 (`vless-ws`) | 3 | （空） |
| 4 (`vmess-speed`) | 4 | （空） |
| 5 (`vmess-tls`) | 5 | （空） |
| 6 (`vmess-ws`) | 6 | （空） |
| 7 (`ss-speed`) | 7 | （空） |
| 8 (`ss-secure`) | 8 | （空） |
| 9 (`ss-balanced`) | 9 | （空） |
| 10 (`oexjuh3j76`) | 1,2,3,4,5,6,7,8,9 | （空） |

**client_traffics（10 行）**——几乎全为 0，只有一条有微量流量：

| id | inbound_id | email | up | down | total |
|---|---|---|---|---|---|
| 1..9 | 1..9 | 各自对应 | 0 | 0 | 0 |
| 10 | 9 | `oexjuh3j76` | **7,763** | **0** | 0 |

## 4.4 clients / client_inbounds 差异结论

| 差异 | A | B |
|---|---|---|
| 客户端数 | 3（真实用户） | 10（1 管理员 + 9 per-协议测试号） |
| UUID 是否一致 | — | **与 A 完全无交集**（不同 uuid/password/sub_id/auth） |
| 是否所有客户端都关联所有入站 | **是**（3 × 12 = 36） | **否**（9 个只管自己那 1 个入站；只有管理员 `oexjuh3j76` 覆盖全部 9 个） |
| Shadowsocks 客户端 uuid | 有值（但入站 settings 里 SS 客户端用 auth/password，不用 id） | **clients 表 uuid 为空**（3 个 SS 客户端） |
| 实际流量证据 | 有真实 GB 级流量（证明 A 可用过） | 仅 7,763 字节上行，近乎零 |

> **注意**：B 的 `oexjuh3j76` 在 `client_traffics` 里只出现在 `inbound_id=9`（SS2022-综合-8390），up=7763 —— 说明**只有 SS 入站曾经收到过一点点数据，其它入站（含 443 REALITY、2052、2053、2087、8080）流量为 0**。这是一个可用的旁证：**用户报告的「协议不可用」覆盖面与「零流量」的入站集合高度一致**。

**这与用户体系差异无关的判定**：由于 B 的 9 个 per-协议账号各自只关联自己的入站，且订阅（`subEnable=true`）会按 `client_inbounds` 下发，**用 `vless-speed` 这类测试账号拿订阅，只会拿到它自己那 1 个节点**；跨协议测试必须用 `oexjuh3j76`。这会让「创建出来的代理用不了」的排查更容易误导，但**不是协议本身不可用的根因**。

---

# 5. 证书文件路径盘点

## 5.1 入站 `stream_settings` 里引用的证书（certificateFile / keyFile）

### A（3 组路径，5 个入站使用）

| 路径 | 类型 | 被哪些入站使用 |
|---|---|---|
| `/root/cert/3344.578272.xyz/fullchain.pem` | cert | `inbound_id=3` (2087), `inbound_id=6` (2052), `inbound_id=12` (2083) |
| `/root/cert/3344.578272.xyz/privkey.pem` | key | 同上 |
| `/root/cert/panel/fullchain.pem` | cert | `inbound_id=11` (443) |
| `/root/cert/panel/privkey.pem` | key | 同上 |
| `/root/cert/ui3344-selfsigned/fullchain.pem` | cert | `inbound_id=5` (2053) |
| `/root/cert/ui3344-selfsigned/privkey.pem` | key | 同上 |

### B（仅 2 组路径，2 个入站使用）

| 路径 | 类型 | 被哪些入站使用 |
|---|---|---|
| `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | cert | `inbound_id=3` (2087) |
| `/root/cert/49pwzx78.578272.xyz/privkey.pem` | key | 同上 |
| `/root/cert/ui3344-selfsigned/fullchain.pem` | cert | `inbound_id=5` (2053) |
| `/root/cert/ui3344-selfsigned/privkey.pem` | key | 同上 |

## 5.2 settings 里的证书路径

| key | A | B | B 是否存在 |
|---|---|---|---|
| `webCertFile` | `/root/cert/panel/fullchain.pem` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | ✅ |
| `webKeyFile` | `/root/cert/panel/privkey.pem` | `/root/cert/49pwzx78.578272.xyz/privkey.pem` | ✅ |
| `subCertFile` | `/root/cert/panel/fullchain.pem` | `/root/cert/49pwzx78.578272.xyz/fullchain.pem` | ✅ |
| `subKeyFile` | `/root/cert/panel/privkey.pem` | `/root/cert/49pwzx78.578272.xyz/privkey.pem` | ✅ |
| `domainConfig.certPath` | `/root/cert/3344.578272.xyz` | `/root/cert/49pwzx78.578272.xyz` | ✅ |

## 5.3 集合差异（关键）

A 用到的全部证书目录：**`/root/cert/3344.578272.xyz/`、`/root/cert/panel/`、`/root/cert/ui3344-selfsigned/`**
B 用到的全部证书目录：**`/root/cert/49pwzx78.578272.xyz/`、`/root/cert/ui3344-selfsigned/`**

| 目录 | A | B |
|---|---|---|
| `/root/cert/3344.578272.xyz/` | ✅（入站 3/6/12 + domainConfig.certPath） | ❌（域名不同，换成 `49pwzx78...`） |
| `/root/cert/panel/` | ✅（**入站 443** + web/sub 四项设置） | **❌ 完全不存在** |
| `/root/cert/ui3344-selfsigned/` | ✅（入站 2053） | ✅（入站 2053） |
| `/root/cert/49pwzx78.578272.xyz/` | ❌ | ✅（入站 3 + web/sub 四项设置） |

**结论**：B 里 **`/root/cert/panel/` 这套证书被整体替换成了 `/root/cert/49pwzx78.578272.xyz/`**。

> **重要且必须区分的点**：`/root/cert/panel/` 是**面板自己的证书目录**，而 A 把它**同时用于入站 443**（`inbound_id=11`，vmess+ws+tls）。B 中该目录不存在，且 B 的 `inbound_id=1/2` 都不引用任何证书。
>
> 就「文件是否真实存在」而言，DB 无法证明 `/root/cert/49pwzx78.578272.xyz/fullchain.pem` 在磁盘上是否存在；**但 DB 能证明**：`domainConfig.certMode = "origin-ca"`，说明证书应由 Cloudflare Origin CA 为该域名签发并落在 `certPath`。若面板申请流程未完成，则 2052/2087/2053/8443 的 TLS 入站与 **面板自身的 8443** 都会因找不到证书而握手失败。

## 5.4 证书相关的入站覆盖对比

| 入站 | A 的证书 | B 的证书 | 风险 |
|---|---|---|---|
| 443 | `/root/cert/panel/*` | **无（REALITY 不需要证书）** | B 的 443 换成了 REALITY，与 CF 代理冲突 |
| 2052 | `/root/cert/3344.578272.xyz/*` | **无（security 降为 none）** | B 明文，若客户端按 TLS 连则失败 |
| 2053 | `/root/cert/ui3344-selfsigned/*`（serverName `ui3344`，**自签**） | 同 A | 自签证书 —— A/B 一致，本身就不是给公网客户端用的（浏览器会报错），属预期的「安全入口」用途 |
| 2087 | `/root/cert/3344.578272.xyz/*` | `/root/cert/49pwzx78.578272.xyz/*` | 域名已换，需证书真实存在 |
| 2082/2083/2086/2095 | 2083 用 `/root/cert/3344.578272.xyz/*`，其余 none | **入站不存在** | — |
| 面板 8443 | `/root/cert/panel/*` | `/root/cert/49pwzx78.578272.xyz/*` | 若证书缺失，面板 HTTPS 起不来 |

---

# 6. 根因判断（按可能性排序）

> 每条都给出 DB 中的具体表/字段/值作为证据。**排序依据**：该差异是否能单独直接导致「协议握手/连接失败」，以及它影响多少入站。

## 🥇 候选原因 1（最高）：B 的 443 用 REALITY，但域名开着 Cloudflare 代理 —— 二者根本互斥

**证据链**：
- `settings.domainConfig` → B：`"proxied": true`，`"fqdn": "49pwzx78.578272.xyz"`，`"ip": "154.12.82.50"`
- `inbounds` → B `id=2, port=443`：`stream_settings.security = "reality"`，`realitySettings.dest = "www.apple.com:443"`，`realitySettings.serverNames = ["www.apple.com"]`，`realitySettings.shortIds = ["4331fefa"]`，`network = "tcp"`，`tlsSettings` 不存在
- 对照 `inbounds` → A `id=11, port=443`：`security = "tls"`，`network = "ws"`，`tlsSettings.certificates[0].certificateFile = "/root/cert/panel/fullchain.pem"`，`serverName = "3344.578272.xyz"`

**为什么这是首要原因**：
- REALITY 的可用前提是**客户端与源站直接完成 TLS 握手并由源站伪装目标站点**；一旦 `proxied=true`，240.0.0.0/4 之外的公网访问 `49pwzx78.578272.xyz:443` 会先到 Cloudflare 边缘，Cloudflare 会用自己的证书终结 TLS，**REALITY 的握手结构被破坏，必然失败**。
- A 的 443 是 **WebSocket + TLS**，正是 Cloudflare 可以正常反向代理的形态——所以 A 的 443 从来就不是靠 REALITY 工作的。B 换了协议，却没有关掉 CF 代理，**这是最直接、最可单独致因的配置错误**。
- 附带证据：B 的 `realitySettings.settings.serverName` 是**空串**（`""`），而 B 又缺 `settings.realityScanCandidates`，说明 REALITY 参数是默认生成、未按域名校准的。

**结论**：B 中 `port=443` 的 `VLESS-安全-Reality-443` 入站**配置本身就是错的**（REALITY 与 Cloudflare 代理冲突），应改为 ws+tls 或关闭 CF 代理。

## 🥈 候选原因 2：B 丢失了全部 4 个「CF优选」入站，优选链路整体塌陷

**证据链**：
- A 有 4 个 CF 优选专属入站：`id=13 port=2082`（ws/none，path `/ui3344cf82`）、`id=12 port=2083`（ws/tls，path `/ui3344cf83`，cert `/root/cert/3344.578272.xyz/*`，serverName `3344.578272.xyz`）、`id=14 port=2086`（ws/none，path `/ui3344cf86`）、`id=15 port=2095`（ws/none，path `/ui3344cf95`）
- B 的端口集合 `[443,2052,2053,2087,8080,8388,8389,8390,8444]` —— **2082/2083/2086/2095 全部不存在**
- 旁证：安装日志 `ui3344 presets: created=9 skipped=0 failed=0`，B 的 inbounds 恰好 9 行 —— 说明 B 只跑了默认 9 个预设，**没有创建 CF优选入站**
- 后果在 hosts 表直接可见：A 的 hosts 挂在 `inbound_id ∈ {3, 12}`；**B 的 hosts 全部挂在 `inbound_id=3`**（24 行全部 `inbound_id=3`）
- 且 B 的 `hosts` 里**没有任何一行 address 指向自有域名 `49pwzx78.578272.xyz`**（A 有 2 行 `3344.578272.xyz`）

**为什么高**：优选域名（`time.is`、`icook.hk` 等，来自 `domainConfig.preferredDomains`）必须配合「能在 CF 上正常代理的入站 + 正确的 SNI」才能工作。B 把 24 个优选地址全部压到 `inbound_id=3`（2087）上，节点形态与 A 的 CF优选节点不再等价；A 里专门为优选准备的 2083（tls 版）在 B 中缺失，使优选列表失去可用的 TLS 承载入站。

**结论**：B 中「CF优选」这一整类入站**缺失**，是 hosts/订阅层不可用的结构性原因。

## 🥉 候选原因 3：B 的 2052 入站从 `tls` 降级为 `none`，且 ws host 为空 —— 明文承载 + SNI 缺失

**证据链**：
- A `id=6 port=2052`：`security="tls"`，`tlsSettings.serverName="3344.578272.xyz"`，`certificates[0].certificateFile="/root/cert/3344.578272.xyz/fullchain.pem"`，`wsSettings.host="3344.578272.xyz"`，`wsSettings.path="/ui3344vm"`
- B `id=6 port=2052`：**`security="none"`**（无 `tlsSettings`），`wsSettings.host=""`（**空串**），`wsSettings.path="/ui3344vm"`

**为什么高**：
- 同一个 `tag`/`remark`（都是 `in-2052-vmess` / `VMess-综合-WS-2052`）、同一个 `wsSettings.path`（`/ui3344vm`）说明这是**同一个入站的同名重建**，但安全层被降级。
- 若客户端订阅链接仍按 TLS（`tls=1`、`sni=49pwzx78.578272.xyz`）连接 2052，而服务端是明文 ws，**TLS 握手直接失败**。
- `wsSettings.host` 为空串会导致部分客户端在 Host 头/SNI 上取到错误值。

**结论**：B 的 `port=2052` 入站**配置本身与 A 不等价**（丢了 TLS 与 host），是明确的配置错误。

## 候选原因 4：订阅服务缺失（`subPort` 等键被判空）

**证据链**：
- `settings` → A 有 `subPort = 2096`；**B `<<ABSENT>>`**
- A 有 `subListen = ""`、`subURI = ""`；B 二者均 `<<ABSENT>>`
- 两边 `subEnable = true`、`subClashEnable = true`、`subJsonEnable = true`、`subClashEnableRouting = true` —— **开关全开，但没有 `subPort`**
- A 有 `webBasePath = /ui3344/`；**B `<<ABSENT>>`**（安装日志显示安全入口为 `/ui3344/`，说明该值本应存在）
- A 有 `xrayTemplateConfig`（1821 字符）；**B `<<ABSENT>>`**
- B 只有 19 个 settings 键，**134 个键整体缺失**（含 `subPort`、`realityScanCandidates`、`webBasePath`、`domainOtpSecret`、`trustedProxyCIDRs`、`remarkTemplate`、`subUpdates`、`subEncrypt`、各 `subHapp*`/`subJson*`/`subClash*` 细项）

**为什么高**：`subEnable=true` 但 `subPort` 缺失 ⇒ 面板无法在预期端口 2096 上提供订阅。这会让「面板里创建完协议、客户端拿不到配置」表现为「协议不可用」。同时 `webBasePath` 缺失会影响面板 URL 与订阅路径的拼接。

**结论**：B 的 settings **不是「被改坏」，而是「整体没写入」**——全新安装只落了 19 个默认键，所有扩展/订阅/优选/遥测键全部缺失。这是 B 与 A 最大、最系统性的差异。

## 候选原因 5：证书目录 `/root/cert/panel/` 在 B 中整体消失，443 失去证书承载

**证据链**：
- A 的证书目录集合：`/root/cert/3344.578272.xyz/`、**`/root/cert/panel/`**、`/root/cert/ui3344-selfsigned/`
- B 的证书目录集合：`/root/cert/49pwzx78.578272.xyz/`、`/root/cert/ui3344-selfsigned/` —— **`/root/cert/panel/` 不存在**
- A `inbound_id=11 (port=443)` 使用 `/root/cert/panel/fullchain.pem` + `/root/cert/panel/privkey.pem`
- A 的 `webCertFile`/`webKeyFile`/`subCertFile`/`subKeyFile` **四项全部**指向 `/root/cert/panel/*`；B 四项全部改为 `/root/cert/49pwzx78.578272.xyz/*`
- `domainConfig.certMode = "origin-ca"`（两边相同），`certPath` 随域名变化

**为什么中**：如果 `/root/cert/49pwzx78.578272.xyz/{fullchain,privkey}.pem` 在磁盘上不存在，则所有 TLS 入站（2052 曾是、2053 用 selfsigned 不受影响、2087、以及**面板自身 8443**）都会因加载证书失败而起不来。DB **无法证明文件是否存在**，但可以确定**路径来源已经整体换过一轮**，且 A 里承担 443 与面板的整套 `panel/` 证书在 B 中无对应物。这属于「需要上机 `ls` 验证」的高危项。

## 候选原因 6：面板搬迁占位入站 `in-8443-vless` 端口/tag 不一致，8443 归属不清晰

**证据链**：
- `settings.domainConfig.panel` → `{"prevPanelPort": 33441, "panelMoved": true, "parkedInboundId": 1, "parkedInboundNewPort": 8444}`
- `settings.webPort = 8443`（A、B 相同）
- B `inbounds id=1`：`tag = "in-8443-vless"`，`remark = "VLESS-速度-8443"`，但 **`port = 8444`**，`protocol=vless`，`security=none`
- B 的端口集合里**没有任何入站占用 8443**，也没有占用 33441
- 安装日志显示面板初始端口为 33441、安全入口 `/ui3344/`；而 `webPort` 已是 8443

**为什么列在此**：这是「面板与 Xray 抢端口」留下的痕迹。面板把原来的入站从 8443 挪到 8444 以让出 8443 给自己，逻辑上自洽；但 `tag`/`remark` 仍写着 `in-8443`/`8443`，**tag 与实际端口不一致**。这本身通常不致命，但它是理解「8443/8444 归属」的关键，且说明 B 经历过端口搬迁流程（`panelMoved=true`）——搬迁过程正是最容易把证书/订阅配置打散的时刻。

## 候选原因 7（次要）：用户/入站关联与订阅下发范围不一致

**证据链**：
- A `client_inbounds`：client_id `10/11/12` 各关联 12 个入站（3 × 12 = 36 行）
- B `client_inbounds`：client_id `1..9` 各只关联 1 个入站，仅 client_id `10 (oexjuh3j76)` 关联全部 9 个（9 + 9 = 18 行）
- B `client_traffics` 除 `id=10, inbound_id=9` 有 `up=7763` 外，**其余 9 行 up/down 全为 0**
- A `client_traffics` 有真实 GB 级流量（如 `id=24 up=1,726,399,324 down=30,904,823,815`）

**为什么低**：这会影响「测试用哪个账号」以及订阅下发的节点数量，但不会让入站本身握手失败。它更可能是**症状**（全零流量说明连接从未成功建立）而非原因，因此列在最后。

---

## 6.1 B 中「配置本身就是错的」入站清单

| 入站 | 问题 | DB 证据 |
|---|---|---|
| `id=2` port **443** `VLESS-安全-Reality-443` | **REALITY 与 Cloudflare 代理互斥** | `domainConfig.proxied=true` + `stream_settings.security="reality"`、`realitySettings.dest="www.apple.com:443"`、无证书；A 同端口为 `ws/tls` |
| `id=6` port **2052** `VMess-综合-WS-2052` | **security 由 tls 降级为 none；ws host 为空串** | `stream_settings={"network":"ws","security":"none","wsSettings":{"headers":{},"host":"","path":"/ui3344vm"}}`；A 同入站为 `security:"tls"` + `host:"3344.578272.xyz"` + 证书 |
| `id=3` port **2087** `VLESS-综合-WS-2087` | 证书路径依赖 `/root/cert/49pwzx78.578272.xyz/*`，**存在性未证实**；(它是 B 全部 24 条 hosts 的唯一承载) | `certificates[0].certificateFile="/root/cert/49pwzx78.578272.xyz/fullchain.pem"` |
| `id=1` port **8444** `VLESS-速度-8443` | **tag/remark 写 8443、实际 port=8444**；`security=none` 明文；无 hosts 引用 | `tag="in-8443-vless"`, `remark="VLESS-速度-8443"`, `port=8444`；`hosts` 中无 `inbound_id=1` |
| **缺失** | **2082 / 2083 / 2086 / 2095 四个 CF优选入站整体不存在** | B 端口集合无这 4 个；A 有 `id=13/12/14/15` |
| **缺失** | **SOCKS5-8888 入站不存在** | B 端口集合无 8888；A 有 `id=10` |
| **缺失** | **`subPort`/`webBasePath`/`realityScanCandidates`/`xrayTemplateConfig` 等 134 个 settings 键不存在** | B 仅 19 键，A 153 键 |

## 6.2 「A 是不是也把 address 设成第三方 CF 优选域名？」—— 明确回答

**是的，而且 A 比 B 更全。** A 的 `hosts` 里 52 行中有 **50 行**的 `address` 是第三方 CF 优选域名（`time.is`、`icook.hk`、`icook.tw`、`ip.sb`、`japan.com`、`malaysia.com`、`russia.com`、`singapore.com`、`skk.moe`、`www.visa.*`、`www.gov.*`、`www.gco.gov.qa`、`1.cf.*`、`xn--b6gac.eu.org`、`cfip.*`、`cf.0sm.com`、`cf.877771.xyz`、`cf.345673.xyz`、`cfip.xxxxxxxx.tk`），另 2 行是自有域名 `3344.578272.xyz`。

B 的 24 行**全部**是第三方 CF 优选域名，**没有自有域名行**。

因此**「hosts 用 CF 优选域名」本身不是 B 独有的错误做法**，A 也这么做且曾经可用。真正的差异在于：
- A 把这些 address 挂在 **2 个**入站上（`inbound_id=3` 2087、`inbound_id=12` 2083），其中 2083 是**专为优选准备的 `ws+tls` VLESS 入站**；
- B 只能挂在**唯一**的 `inbound_id=3`（2087），因为专为优选准备的入站在 B 中**根本不存在**。

---

# 7. 建议的验证与修复顺序（按根因优先级，均为 DB 证据驱动）

1. **先上机确认证书是否存在**（DB 无法证明，但这是最高危的未知项）：
   `ls -l /root/cert/49pwzx78.578272.xyz/ /root/cert/ui3344-selfsigned/ /root/cert/panel/`
   —— 若 `/root/cert/49pwzx78.578272.xyz/fullchain.pem` 不存在，则 2052/2087 与面板 8443 全部起不来。
2. **处理 443 的 REALITY/CF 冲突**：要么关闭 `domainConfig.proxied`（改为灰云直连）并保留 REALITY，要么把 443 改回 A 的 `vmess + ws + tls + /root/cert/...` 形态。
3. **重建 4 个 CF优选入站**（2082/2083/2086/2095）与 SOCKS5-8888，使 `hosts` 能像 A 一样分挂到两个入站。
4. **补回 settings 缺失键**：至少 `subPort=2096`、`webBasePath=/ui3344/`、`realityScanCandidates`，否则订阅链路不通。
5. **修正 2052**：决定是恢复 `security=tls` + 证书 + `wsSettings.host`，还是明确按明文使用并同步客户端配置。
6. **核对 `inbound_id=1` 的 tag/remark 与实际 `port=8444`**，消除 `in-8443`/8443 的命名错位。

---

## 附：本次分析用到的只读脚本（可复现）

均位于 `C:\Users\Administrator\ui\_dbdiff\`，全部以 `mode=ro&immutable=1` 打开数据库，无任何写操作：

`common.py`、`s01_schema.py`、`s02_settings.py`、`s03_keys.py`、`s04_inbounds.py`、`s05_domain.py`、`s06_pair.py`、`s07_stream.py`、`s08_hosts.py`、`s09_clients.py`、`s10_certs.py`

原始输出：`settings_out.txt`、`keys_out.txt`、`domain_out.txt`、`pair_out.txt`、`stream_out.txt`、`hosts_out.txt`、`clients_out.txt`、`certs_out.txt`
