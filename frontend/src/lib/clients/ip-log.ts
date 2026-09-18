// Shape of one entry in a client's IP log, as returned by
// POST /panel/api/clients/ips/:email. `node` is the name of the node the IP is
// connecting through, or '' when it is on this local panel (or unattributed).
export type ClientIpInfo = {
  ip: string;
  time: string;
  node: string;
  country?: string;
  province?: string;
  city?: string;
  isp?: string;
};

// formatRegion renders a client IP's resolved region, e.g. "江苏省 南京市 电信".
// The country is dropped for domestic (China) addresses to avoid noise.
export function formatRegion(entry: ClientIpInfo): string {
  const parts = [entry.province, entry.city, entry.isp].filter(Boolean);
  if (entry.country && entry.country !== '中国') parts.unshift(entry.country);
  return parts.join(' ');
}

// normalizeClientIps accepts the API payload and returns typed entries. It also
// tolerates the legacy shape (a plain array of "ip (time)" strings) so the UI
// keeps working against older panels.
export function normalizeClientIps(obj: unknown): ClientIpInfo[] {
  if (!Array.isArray(obj)) return [];
  const out: ClientIpInfo[] = [];
  for (const x of obj) {
    if (typeof x === 'string') {
      if (x.length > 0) out.push({ ip: x, time: '', node: '' });
      continue;
    }
    if (x && typeof x === 'object') {
      const o = x as Record<string, unknown>;
      const ip = typeof o.ip === 'string' ? o.ip : '';
      if (!ip) continue;
      out.push({
        ip,
        time: typeof o.time === 'string' ? o.time : '',
        node: typeof o.node === 'string' ? o.node : '',
        country: typeof o.country === 'string' ? o.country : '',
        province: typeof o.province === 'string' ? o.province : '',
        city: typeof o.city === 'string' ? o.city : '',
        isp: typeof o.isp === 'string' ? o.isp : '',
      });
    }
  }
  return out;
}

// isPrivateIp reports whether an IPv4 address is in a private/reserved range,
// so the UI can label it instead of leaving the region blank.
export function isPrivateIp(ip: string): boolean {
  return /^(10\.|127\.|169\.254\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(ip);
}
