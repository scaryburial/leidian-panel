import { z } from 'zod';

import { CARD_KEY_GB } from '@/lib/clients/card-keys';

/** Card-key quota in GB -> the byte count the panel stores on a client. */
export function cardKeyGBToBytes(gb: number): number {
  return Math.trunc((Number(gb) || 0) * CARD_KEY_GB);
}

// The number fields arrive from AntD InputNumber as string | number | null, so
// every one of them is coerced before the range check (0 = no limit for both
// the traffic and the expiry).
export const CardKeyGenerateFormSchema = z.object({
  inboundId: z.number().int().positive('pages.clients.cardKeys.inboundRequired'),
  count: z.coerce
    .number()
    .int()
    .min(1, 'pages.clients.cardKeys.countRange')
    .max(500, 'pages.clients.cardKeys.countRange'),
  totalGB: z.coerce
    .number()
    .min(0, 'pages.clients.cardKeys.quotaRange')
    .max(1048576, 'pages.clients.cardKeys.quotaRange'),
  expiryDays: z.coerce
    .number()
    .int()
    .min(0, 'pages.clients.cardKeys.daysRange')
    .max(36500, 'pages.clients.cardKeys.daysRange'),
  remarkPrefix: z.string().max(64, 'pages.clients.cardKeys.remarkPrefixRange'),
  groupName: z.string().max(64, 'pages.clients.cardKeys.groupRange'),
});

export const CardKeyGenerateResponseSchema = z.object({
  cardKeys: z
    .array(z.string())
    .nullable()
    .transform((v) => v ?? []),
});

export type CardKeyGenerateFormValues = z.infer<typeof CardKeyGenerateFormSchema>;
export type CardKeyGenerateResponse = z.infer<typeof CardKeyGenerateResponseSchema>;
export type CardKeyGeneratePayload = z.infer<typeof CardKeyGenerateFormSchema>;
