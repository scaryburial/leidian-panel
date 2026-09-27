import { TlsStreamSettingsSchema } from '@/schemas/protocols/security/tls';

function defaultCertificate(): Record<string, unknown> {
  return {
    useFile: true,
    certificateFile: '',
    keyFile: '',
    certificate: [],
    key: [],
    ocspStapling: 0,
    oneTimeLoading: false,
    usage: 'encipherment',
    buildChain: false,
  };
}

export function createTlsSettingsWithDefaultCert(): Record<string, unknown> {
  const tls = TlsStreamSettingsSchema.parse({}) as Record<string, unknown>;
  tls.certificates = [defaultCertificate()];
  const settings =
    tls.settings && typeof tls.settings === 'object' && !Array.isArray(tls.settings)
      ? { ...(tls.settings as Record<string, unknown>) }
      : {};
  settings.fingerprint = 'chrome';
  tls.settings = settings;
  return tls;
}

/*
 * WebSocket and HTTPUpgrade cannot run over HTTP/2. Offering h2 in ALPN lets the
 * client (or Cloudflare, in front of it) negotiate h2, and the upgrade then fails
 * outright instead of falling back: `websocket: protocol "h2" was given but is
 * not supported`, `malformed HTTP response "\x00\x00\x12\x04..."`. Measured on
 * one node: alpn=http/1.1 -> HTTP 204; alpn=h2,http/1.1 -> 000. The Go side
 * writes http/1.1 only for the same reason (domain.go); do not widen this.
 */
export function tlsAlpnForNetwork(network: string | undefined, alpn: unknown): string[] {
  const list = Array.isArray(alpn) ? alpn.filter((v): v is string => typeof v === 'string') : [];
  if (network !== 'ws' && network !== 'httpupgrade') return list;
  return list.filter((v) => v !== 'h2');
}

export function createHysteriaTlsSettingsWithDefaultCert(): Record<string, unknown> {
  const tls = createTlsSettingsWithDefaultCert();
  tls.alpn = ['h3'];

  const settings =
    tls.settings && typeof tls.settings === 'object' && !Array.isArray(tls.settings)
      ? { ...(tls.settings as Record<string, unknown>) }
      : {};
  settings.fingerprint = '';
  tls.settings = settings;

  return tls;
}
