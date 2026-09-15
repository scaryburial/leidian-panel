import { Outlet } from 'react-router';

import { useWebSocketBridge } from '@/api/websocketBridge';
import { usePageTitle } from '@/hooks/usePageTitle';
import CommandPalette from '@/components/command-palette/CommandPalette';
import PanelBackground from '@/layouts/PanelBackground';

export default function PanelLayout() {
  useWebSocketBridge();
  usePageTitle();
  return (
    <>
      <PanelBackground />
      <Outlet />
      <CommandPalette />
    </>
  );
}
