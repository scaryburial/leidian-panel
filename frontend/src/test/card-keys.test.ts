import { describe, expect, it } from 'vitest';

import {
  cardKeyExportFileName,
  cardKeyExportUrl,
  formatCardKeyForDisplay,
  isCanonicalCardKey,
  normalizeCardKey,
} from '@/lib/clients/card-keys';

describe('normalizeCardKey', () => {
  it('accepts the canonical shape unchanged', () => {
    expect(normalizeCardKey('LD-ABCD-EFGH-IJKL')).toBe('LD-ABCD-EFGH-IJKL');
  });

  it('upper-cases and re-groups what an operator pastes back', () => {
    expect(normalizeCardKey('ldabcd-efghijkl')).toBe('LD-ABCD-EFGH-IJKL');
    expect(normalizeCardKey('  ld abcd efgh ijkl  ')).toBe('LD-ABCD-EFGH-IJKL');
    expect(normalizeCardKey('LDABCDEFGHIJKL')).toBe('LD-ABCD-EFGH-IJKL');
  });

  it('adds the LD prefix when the backend hands back a bare body', () => {
    expect(normalizeCardKey('0123456789ab')).toBe('LD-0123-4567-89AB');
  });

  it('treats empty and non-key values as "no card key"', () => {
    expect(normalizeCardKey('')).toBe('');
    expect(normalizeCardKey(undefined)).toBe('');
    expect(normalizeCardKey(null)).toBe('');
    expect(normalizeCardKey('---')).toBe('');
  });
});

describe('isCanonicalCardKey', () => {
  it('is true only for the LD-XXXX-XXXX-XXXX form', () => {
    expect(isCanonicalCardKey('LD-ABCD-EFGH-IJKL')).toBe(true);
    expect(isCanonicalCardKey('ld-abcd-efgh-ijkl')).toBe(false);
    expect(isCanonicalCardKey('LDABCDEFGHIJKL')).toBe(false);
    expect(isCanonicalCardKey('LD-ABC-EFGH-IJKL')).toBe(false);
    expect(isCanonicalCardKey('')).toBe(false);
  });
});

describe('formatCardKeyForDisplay', () => {
  it('never lets a stray character widen the cell', () => {
    expect(formatCardKeyForDisplay('LD-ABCD-EFGH-IJKL')).toBe('LD-ABCD-EFGH-IJKL');
    expect(formatCardKeyForDisplay('ld abcd/efgh*ijkl')).toBe('LD-ABCD-EFGH-IJKL');
    expect(formatCardKeyForDisplay('')).toBe('');
  });
});

describe('CSV export helpers', () => {
  it('builds the contract URL', () => {
    expect(cardKeyExportUrl(1)).toBe('/panel/api/clients/cardkeys/export?inboundId=1');
    expect(cardKeyExportUrl(42)).toBe('/panel/api/clients/cardkeys/export?inboundId=42');
  });

  it('date-stamps the download name', () => {
    expect(cardKeyExportFileName(1, new Date(2026, 1, 14))).toBe(
      'cardkeys-inbound-1-2026-02-14.csv',
    );
    expect(cardKeyExportFileName(7, new Date(2026, 11, 3))).toBe(
      'cardkeys-inbound-7-2026-12-03.csv',
    );
  });
});
