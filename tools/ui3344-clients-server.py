#!/usr/bin/env python3
"""在面板尚未升级到 v2.3 之前，先把客户端安装包对外提供出去。

面板二进制里的 /clients/ 路由要重新编译才会出现；在那之前，用这个极简静态
服务把 <数据目录>/clients 挂到 Cloudflare 可代理的 HTTP 端口（默认 8880）上，
订阅页下载按钮将来指向的也是同一批文件，升级后可直接停用本服务。

用法：
  ui3344-clients-server [--port 8880] [--dir /etc/ui3344/clients]
"""
import argparse
import html
import os
import re
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

RANGE_RE = re.compile(r"bytes=(\d*)-(\d*)")

TEMPLATE = """<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>客户端下载</title>
<style>
:root{color-scheme:dark}
body{margin:0;padding:24px;font:15px/1.6 -apple-system,system-ui,"Segoe UI",Roboto,sans-serif;
     background:#14151a;color:#e7e8ec}
h1{font-size:20px;margin:0 0 4px}
p.hint{color:#9aa0ad;font-size:13px;margin:0 0 20px}
.card{background:#1c1e26;border:1px solid #2a2d38;border-radius:12px;padding:16px;margin-bottom:14px}
h2{font-size:14px;margin:0 0 12px;color:#9aa0ad;font-weight:600;letter-spacing:.5px}
a.row{display:flex;align-items:center;justify-content:space-between;gap:12px;
      padding:12px 14px;margin:8px 0;background:#242733;border-radius:10px;
      color:#e7e8ec;text-decoration:none}
a.row:hover{background:#2c3040}
.meta{color:#9aa0ad;font-size:12px}
.size{color:#6ea8fe;font-size:12px;white-space:nowrap}
</style></head><body>
<h1>客户端下载</h1>
<p class="hint">选一个装到手机上，再把订阅链接导入即可。下载卡住可重试，支持断点续传。</p>
%s
</body></html>
"""

ANDROID = [
    ("v2rayng-universal.apk", "V2RayNG（通用版）", "推荐，兼容所有机型"),
    ("v2rayng-arm64.apk", "V2RayNG（arm64）", "体积更小，近几年的手机都用这个"),
    ("singbox-sfa.apk", "Sing-box (SFA)", "通用性强，支持多协议"),
    ("happ.apk", "Happ", "界面清爽"),
    ("incy.apk", "Incy", "轻量"),
]

IOS = [
    ("https://apps.apple.com/app/shadowrocket/id932747118", "Shadowrocket", "付费"),
    ("https://apps.apple.com/app/v2box-v2ray-client/id6446814690", "V2Box", "免费"),
    ("https://apps.apple.com/app/v2raytun/id6476628951", "V2RayTun", "免费"),
    ("https://apps.apple.com/app/npv-tunnel/id1629465476", "NPV Tunnel", "免费"),
    ("https://apps.apple.com/app/happ-proxy-utility/id6504287215", "Happ", "免费"),
]


def human(size):
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024:
            return "%.0f %s" % (size, unit) if unit == "B" else "%.1f %s" % (size, unit)
        size /= 1024.0
    return "%.1f TB" % size


def build_index(directory):
    rows = []
    for name, label, note in ANDROID:
        path = os.path.join(directory, name)
        if not os.path.exists(path):
            continue
        size = human(os.path.getsize(path))
        rows.append(
            '<a class="row" href="%s"><span>%s<div class="meta">%s</div></span>'
            '<span class="size">%s</span></a>'
            % (html.escape(name), html.escape(label), html.escape(note), size)
        )
    android_block = '<div class="card"><h2>ANDROID</h2>%s</div>' % "".join(rows)

    ios_rows = "".join(
        '<a class="row" href="%s"><span>%s<div class="meta">%s</div></span>'
        '<span class="size">App Store</span></a>'
        % (url, html.escape(label), html.escape(note))
        for url, label, note in IOS
    )
    ios_block = '<div class="card"><h2>iOS</h2>%s</div>' % ios_rows
    return TEMPLATE % (android_block + ios_block)


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, directory=None, index_html=None, **kwargs):
        self.index_html = index_html
        super().__init__(*args, directory=directory, **kwargs)

    def do_GET(self):
        if self.path in ("/", "/index.html"):
            body = self.index_html.encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self._serve_ranged():
            return
        super().do_GET()

    def _serve_ranged(self):
        """支持 Range：APK 有几十 MB，网络差时断点续传比重新下整包重要得多。"""
        header = self.headers.get("Range")
        if not header:
            return False
        match = RANGE_RE.match(header.strip())
        if not match:
            return False
        name = os.path.basename(self.path.split("?")[0])
        full = os.path.join(self.directory, name)
        if not os.path.isfile(full):
            return False
        size = os.path.getsize(full)
        start_s, end_s = match.group(1), match.group(2)
        if start_s:
            start = int(start_s)
            end = int(end_s) if end_s else size - 1
        else:
            start = max(0, size - int(end_s or 0))
            end = size - 1
        if start >= size:
            self.send_response(416)
            self.send_header("Content-Range", "bytes */%d" % size)
            self.end_headers()
            return True
        end = min(end, size - 1)
        length = end - start + 1
        self.send_response(206)
        self.send_header("Content-Type", self.guess_type(name))
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Range", "bytes %d-%d/%d" % (start, end, size))
        self.send_header("Content-Length", str(length))
        self.end_headers()
        with open(full, "rb") as fh:
            fh.seek(start)
            remaining = length
            while remaining > 0:
                chunk = fh.read(min(65536, remaining))
                if not chunk:
                    break
                self.wfile.write(chunk)
                remaining -= len(chunk)
        return True

    def log_message(self, fmt, *args):
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=8880)
    parser.add_argument("--dir", default="/etc/ui3344/clients")
    parser.add_argument("--bind", default="0.0.0.0")
    args = parser.parse_args()

    if not os.path.isdir(args.dir):
        print("目录不存在：%s" % args.dir, file=sys.stderr)
        return 1

    index_html = build_index(args.dir)
    handler = lambda *a, **kw: Handler(*a, directory=args.dir,  # noqa: E731
                                       index_html=index_html, **kw)
    server = ThreadingHTTPServer((args.bind, args.port), handler)
    print("客户端下载服务已启动：http://%s:%d/ （目录 %s）" % (args.bind, args.port, args.dir))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
