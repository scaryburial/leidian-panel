/// <reference types="vite/client" />
import { describe, expect, it } from 'vitest';

import {
  createTlsSettingsWithDefaultCert,
  tlsAlpnForNetwork,
} from '@/lib/xray/inbound-tls-defaults';
import { fillStreamDefaults } from '@/lib/xray/stream-defaults';
import { TlsStreamSettingsSchema } from '@/schemas/protocols/security/tls';

// WebSocket and HTTPUpgrade cannot run over HTTP/2. Offering h2 in ALPN lets the
// client (or Cloudflare) negotiate h2, and the upgrade then fails outright:
// `websocket: protocol "h2" was given but is not supported`, `malformed HTTP
// response "\x00\x00\x12\x04..."`. Measured: alpn=http/1.1 -> 204, alpn=h2,http/1.1
// -> 000. These tests pin the transport-aware default so it cannot regress.
describe('TLS ALPN default per transport', () => {
  it('strips h2 for ws and httpupgrade, keeping http/1.1', () => {
    for (const network of ['ws', 'httpupgrade']) {
      expect(tlsAlpnForNetwork(network, ['h2', 'http/1.1'])).toEqual(['http/1.1']);
    }
  });

  it('leaves h2 in place for transports that can use it', () => {
    for (const network of ['tcp', 'grpc', 'xhttp', 'kcp', '', undefined]) {
      expect(tlsAlpnForNetwork(network, ['h2', 'http/1.1'])).toEqual(['h2', 'http/1.1']);
    }
  });

  it('never invents an ALPN for a transport that cannot set one', () => {
    expect(tlsAlpnForNetwork('ws', [])).toEqual([]);
    expect(tlsAlpnForNetwork('ws', undefined)).toEqual([]);
    expect(tlsAlpnForNetwork('ws', 'h2,http/1.1')).toEqual([]);
  });

  it('keeps h3 and drops only h2 on a ws transport', () => {
    expect(tlsAlpnForNetwork('ws', ['h2', 'h3', 'http/1.1'])).toEqual(['h3', 'http/1.1']);
  });

  it('does not mutate the list it is given', () => {
    const original = ['h2', 'http/1.1'];
    tlsAlpnForNetwork('ws', original);
    expect(original).toEqual(['h2', 'http/1.1']);
  });

  it('seeds a ws TLS inbound without h2', () => {
    const tls = createTlsSettingsWithDefaultCert();
    // The raw schema default still carries both, which is what the sanitizer
    // narrows; asserting it here documents exactly what is being narrowed.
    expect(TlsStreamSettingsSchema.parse({}).alpn).toEqual(['h2', 'http/1.1']);
    expect(tlsAlpnForNetwork('ws', tls.alpn)).toEqual(['http/1.1']);
  });

  it('produces the documented http/1.1-only ALPN the Go side writes', () => {
    // internal/web/service/domain/domain.go writes alpn = ["http/1.1"] for a
    // proxied ws inbound; the frontend must not hand the runtime anything wider.
    expect(tlsAlpnForNetwork('ws', TlsStreamSettingsSchema.parse({}).alpn)).not.toContain('h2');
  });
});

// Fill-on-read is the last chokepoint: an inbound stored (or imported) as
// ws + tls gets the schema's transport-blind default unless it is narrowed here.
describe('fillStreamDefaults narrows ALPN to the network', () => {
  it('drops h2 from a ws TLS stream coming out of the DB', () => {
    const filled = fillStreamDefaults({
      network: 'ws',
      security: 'tls',
      wsSettings: { path: '/ui3344ws' },
      tlsSettings: { serverName: 'example.test', alpn: ['h2', 'http/1.1'] },
    });
    expect((filled.tlsSettings as Record<string, unknown>).alpn).toEqual(['http/1.1']);
  });

  it('drops h2 from a ws TLS stream that had no stored tlsSettings at all', () => {
    const filled = fillStreamDefaults({ network: 'ws', security: 'tls', wsSettings: {} });
    expect((filled.tlsSettings as Record<string, unknown>).alpn).toEqual(['http/1.1']);
  });

  it('drops h2 from an httpupgrade TLS stream', () => {
    const filled = fillStreamDefaults({
      network: 'httpupgrade',
      security: 'tls',
      httpupgradeSettings: { path: '/' },
      tlsSettings: { alpn: ['h2', 'http/1.1'] },
    });
    expect((filled.tlsSettings as Record<string, unknown>).alpn).toEqual(['http/1.1']);
  });

  it('keeps h2 for a tcp TLS stream', () => {
    const filled = fillStreamDefaults({
      network: 'tcp',
      security: 'tls',
      tcpSettings: {},
      tlsSettings: { alpn: ['h2', 'http/1.1'] },
    });
    expect((filled.tlsSettings as Record<string, unknown>).alpn).toEqual(['h2', 'http/1.1']);
  });

  it('leaves a ws stream with security none untouched', () => {
    const filled = fillStreamDefaults({ network: 'ws', security: 'none', wsSettings: {} });
    expect(filled.tlsSettings).toBeUndefined();
  });
});
