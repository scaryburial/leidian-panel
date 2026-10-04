import { useMutation, useQueryClient } from '@tanstack/react-query';

import { HttpUtil, Msg } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { markLocalInvalidate } from '@/api/invalidationTracker';
import { cardKeyExportUrl } from '@/lib/clients/card-keys';
import {
  CardKeyGenerateResponseSchema,
  type CardKeyGeneratePayload,
  type CardKeyGenerateResponse,
} from '@/schemas/cardKey';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } } as const;

/** Single-client generate: the contract takes the same body with count 1. */
export function generateCardKeys(
  payload: CardKeyGeneratePayload,
): Promise<Msg<CardKeyGenerateResponse>> {
  return HttpUtil.post('/panel/api/clients/cardkeys/generate', payload, JSON_HEADERS);
}

/* The export endpoint answers text/csv, so HttpUtil (which parses JSON only)
 * cannot carry it: a raw fetch keeps the credentials and the base path the
 * shared transport installs, and the caller decides how to save the text. */
export async function exportCardKeys(inboundId: number): Promise<string> {
  const base = (window.X_UI_BASE_PATH || '').replace(/\/$/, '');
  const resp = await fetch(`${base}${cardKeyExportUrl(inboundId)}`, {
    method: 'GET',
    credentials: 'same-origin',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  if (!resp.ok) throw new Error(`HTTP ${resp.status} ${resp.statusText}`);
  return resp.text();
}

export function useCardKeys() {
  const queryClient = useQueryClient();

  const generateMut = useMutation({
    mutationFn: async (payload: CardKeyGeneratePayload) => {
      const raw = await generateCardKeys(payload);
      return parseMsg(raw, CardKeyGenerateResponseSchema, 'clients/cardkeys/generate');
    },
    onSuccess: (msg) => {
      if (!msg?.success) return;
      markLocalInvalidate();
      void queryClient.invalidateQueries({ queryKey: keys.clients.root() });
      void queryClient.invalidateQueries({ queryKey: keys.inbounds.root() });
    },
  });

  return {
    generate: (payload: CardKeyGeneratePayload) => generateMut.mutateAsync(payload),
    generating: generateMut.isPending,
  };
}
