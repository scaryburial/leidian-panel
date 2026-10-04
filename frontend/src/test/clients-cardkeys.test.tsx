import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import ClientsPage from '@/pages/clients/ClientsPage';
import { ThemeProvider } from '@/hooks/useTheme';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

/* jsdom mounts this page slowly: the lazy modals, TanStack Query and the AntD
 * portal chain make the first paint take seconds, so every wait here gets a
 * budget rather than the 1s default. */
const SETTLE = { timeout: 8000 } as const;

afterEach(() => {
  vi.restoreAllMocks();
});

const CLIENTS = [
  {
    id: 1,
    email: 'issued@example.com',
    inboundIds: [1],
    enable: true,
    totalGB: 0,
    expiryTime: 0,
    cardKey: 'LD-ABCD-EFGH-IJKL',
    traffic: { up: 0, down: 0 },
  },
  {
    id: 2,
    email: 'plain@example.com',
    inboundIds: [1],
    enable: true,
    totalGB: 0,
    expiryTime: 0,
    cardKey: '',
    traffic: { up: 0, down: 0 },
  },
];

const PAGE = {
  items: CLIENTS,
  total: CLIENTS.length,
  filtered: CLIENTS.length,
  page: 1,
  pageSize: 25,
  groups: [],
  summary: {
    total: CLIENTS.length,
    active: CLIENTS.length,
    onlineCount: 0,
    depletedCount: 0,
    expiringCount: 0,
    deactiveCount: 0,
    online: [],
    depleted: [],
    expiring: [],
    deactive: [],
  },
};

const INBOUNDS = [
  { id: 1, remark: '主线路', tag: 'in-1', protocol: 'vless', port: 443, network: 'tcp' },
];

function mockPanel() {
  const posts: { url: string; body: unknown }[] = [];
  vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) => {
    if (url.includes('/clients/list/paged')) return new Msg(true, '', PAGE);
    if (url.includes('/inbounds/options')) return new Msg(true, '', INBOUNDS);
    return new Msg(true, '', null);
  });
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string, body?: unknown) => {
    posts.push({ url, body });
    if (url.includes('/setting/defaultSettings')) return new Msg(true, '', { pageSize: 25 });
    if (url.includes('/clients/onlines')) return new Msg(true, '', []);
    if (url.includes('/cardkeys/generate')) {
      return new Msg(true, '', { cardKeys: ['LD-1111-2222-3333', 'LD-4444-5555-6666'] });
    }
    return new Msg(true, '', null);
  });
  return posts;
}

function renderPage() {
  const queryClient = makeTestQueryClient();
  return render(
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <MemoryRouter initialEntries={['/panel/clients']}>
          <ClientsPage />
        </MemoryRouter>
      </ThemeProvider>
    </QueryClientProvider>,
  );
}

describe('clients page card keys', () => {
  it('shows the issued key for one client and a generate button for the other', async () => {
    mockPanel();
    renderPage();

    await waitFor(() => expect(screen.getByText('LD-ABCD-EFGH-IJKL')).toBeTruthy(), SETTLE);
    // The un-issued row offers the generate affordance instead of a key.
    expect(screen.getByText('Generate')).toBeTruthy();
    // ...and the filter bar counts it, so the gap is visible without scanning.
    expect(screen.getByText('1 without a card key')).toBeTruthy();
  });

  it('generates a key for a single client through the generate endpoint', async () => {
    const posts = mockPanel();
    renderPage();

    await waitFor(() => expect(screen.getByText('Generate')).toBeTruthy(), SETTLE);
    fireEvent.click(screen.getByText('Generate'));

    await waitFor(
      () => expect(posts.some((p) => p.url.includes('/clients/cardkeys/generate'))).toBe(true),
      SETTLE,
    );
    const call = posts.find((p) => p.url.includes('/clients/cardkeys/generate'));
    expect(call?.body).toMatchObject({ inboundId: 1, count: 1 });
  });

  it('bulk-generates from the toolbar modal and lists the returned keys', async () => {
    const posts = mockPanel();
    renderPage();

    await waitFor(() => expect(screen.getByText('LD-ABCD-EFGH-IJKL')).toBeTruthy(), SETTLE);
    // The toolbar button carries the same label as the modal it opens.
    const opener = document.querySelector(
      'button[aria-label="Generate card keys"]',
    ) as HTMLElement | null;
    expect(opener).toBeTruthy();
    fireEvent.click(opener!);

    await waitFor(() => expect(document.querySelector('.ant-modal')).toBeTruthy(), SETTLE);
    await waitFor(() => expect(document.body.textContent).toContain('Remark prefix'), SETTLE);

    // AntD 6's Form.Item owns the control id (`_r_xx_`), so the field is
    // reached through its role and label rather than `#inboundId`.
    const inbound = screen.getByLabelText('Inbound');
    fireEvent.mouseDown(inbound);
    const option = await screen.findByTitle('主线路 · 443 · vless');
    fireEvent.click(option);
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));

    await waitFor(
      () => expect(posts.some((p) => p.url.includes('/clients/cardkeys/generate'))).toBe(true),
      SETTLE,
    );
    const call = posts.find((p) => p.url.includes('/clients/cardkeys/generate'));
    expect(call?.body).toMatchObject({
      inboundId: 1,
      count: 10,
      totalGB: 100,
      expiryDays: 30,
      remarkPrefix: '套餐A',
    });
    // The result list renders the keys the endpoint returned.
    await waitFor(() => expect(screen.getByText('LD-1111-2222-3333')).toBeTruthy(), SETTLE);
    expect(screen.getByText('LD-4444-5555-6666')).toBeTruthy();
  }, 30000);
});
