// Mirror of web/service/panel.go isNewerVersion: parse a vMAJOR.MINOR.PATCH tag
// and report whether `latest` is ahead of `current`. When either side isn't a
// clean three-part numeric tag, fall back to a normalized string inequality —
// the same heuristic the Go side uses so the node "update available" badge
// agrees with what the server would decide.
// 解析形如 "v1"、"1.6"、"1.7.0" 的版本号。历史发布标签是两段式的
// （v1.6），这里不再强制三段式，缺位在比较时按 0 处理。
function parseVersionParts(version: string): number[] | null {
  const normalized = version.trim().replace(/^v/i, '');
  if (!normalized) return null;
  const parts = normalized.split('.');
  const out: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) return null;
    out.push(Number(part));
  }
  return out;
}

// Format a panel version for display. Dev builds report a "dev+<commit>"
// identity (see config.GetPanelVersion); show those — and any other
// non-numeric label — verbatim. Semantic versions get a single normalized "v"
// prefix, so a raw "v3.4.0" tag and a bare "3.4.0" both render as "v3.4.0"
// instead of doubling up to "vv3.4.0".
export function formatPanelVersion(version: string | undefined | null): string {
  const v = (version || '').trim();
  if (!v) return '';
  const normalized = v.replace(/^v/i, '');
  return /^\d/.test(normalized) ? `v${normalized}` : v;
}

export function isPanelUpdateAvailable(latest: string, current: string): boolean {
  if (!latest || !current) return false;
  const a = parseVersionParts(latest);
  const b = parseVersionParts(current);
  if (!a || !b) {
    return latest.trim().replace(/^v/, '') !== current.trim().replace(/^v/, '');
  }
  const n = Math.max(a.length, b.length);
  for (let i = 0; i < n; i++) {
    const av = a[i] ?? 0;
    const bv = b[i] ?? 0;
    if (av > bv) return true;
    if (av < bv) return false;
  }
  return false;
}
