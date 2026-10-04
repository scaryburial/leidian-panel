/*
 * Card-key (卡密) helpers. The backend picks the canonical LD-XXXX-XXXX-XXXX
 * shape, but operators routinely paste keys back with spaces, lowercase or a
 * missing prefix, so every read path normalises before comparing or exporting.
 */

export const CARD_KEY_GROUP_SEPARATOR = '-';
export const CARD_KEY_PREFIX = 'LD';
export const CARD_KEY_GROUP_SIZE = 4;
export const CARD_KEY_GROUP_COUNT = 3;
export const CARD_KEY_GB = 1024 * 1024 * 1024;

const CARD_KEY_BODY_PATTERN = new RegExp(
  `^${CARD_KEY_PREFIX}([0-9A-Za-z]{${CARD_KEY_GROUP_SIZE * CARD_KEY_GROUP_COUNT}})$`,
);

/**
 * Canonical LD-XXXX-XXXX-XXXX form: prefix and body upper-cased, separators
 * dropped, body re-grouped. Returns '' for anything that cannot hold a key, so
 * callers can treat the result as the "has no card key" test.
 */
export function normalizeCardKey(raw: string | null | undefined): string {
  if (!raw) return '';
  const compact = String(raw)
    .toUpperCase()
    .replace(/[^0-9A-Z]/g, '');
  const body = compact.startsWith(CARD_KEY_PREFIX)
    ? compact.slice(CARD_KEY_PREFIX.length)
    : compact;
  if (body.length === 0) return '';
  const groups: string[] = [];
  for (let i = 0; i < body.length; i += CARD_KEY_GROUP_SIZE) {
    groups.push(body.slice(i, i + CARD_KEY_GROUP_SIZE));
  }
  return [CARD_KEY_PREFIX, ...groups].join(CARD_KEY_GROUP_SEPARATOR);
}

/** True when the value already carries the canonical LD-XXXX-XXXX-XXXX shape. */
export function isCanonicalCardKey(raw: string | null | undefined): boolean {
  if (!raw) return false;
  const compact = String(raw).replace(/[^0-9A-Za-z]/g, '');
  const match = CARD_KEY_BODY_PATTERN.exec(compact.toUpperCase());
  return !!match && normalizeCardKey(raw) === raw;
}

/**
 * Display wrapper: keeps the cell one line wide when a key arrives with
 * unexpected characters, without changing what the copy button writes.
 */
export function formatCardKeyForDisplay(key: string): string {
  return normalizeCardKey(key).replace(/[^0-9A-Z-]/g, CARD_KEY_GROUP_SEPARATOR);
}

/** Card-key rows for one inbound, for the CSV download endpoint. */
export function cardKeyExportUrl(inboundId: number): string {
  return `/panel/api/clients/cardkeys/export?inboundId=${encodeURIComponent(String(inboundId))}`;
}

/** Date-stamped download name, e.g. cardkeys-inbound-1-2026-02-14.csv. */
export function cardKeyExportFileName(inboundId: number, now: Date = new Date()): string {
  const stamp = [
    now.getFullYear(),
    String(now.getMonth() + 1).padStart(2, '0'),
    String(now.getDate()).padStart(2, '0'),
  ].join('-');
  return `cardkeys-inbound-${inboundId}-${stamp}.csv`;
}
