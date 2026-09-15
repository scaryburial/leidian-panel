import { useEffect, useState } from 'react';

import panel1 from '@/assets/panel-1.jpg';
import panel2 from '@/assets/panel-2.jpg';
import panel3 from '@/assets/panel-3.jpg';
import panel4 from '@/assets/panel-4.jpg';
import panel5 from '@/assets/panel-5.jpg';
import panel6 from '@/assets/panel-6.jpg';

import './PanelBackground.css';

const IMAGES = [panel1, panel2, panel3, panel4, panel5, panel6];
const ROTATE_INTERVAL_MS = 6000;

export default function PanelBackground() {
  const [active, setActive] = useState(0);

  useEffect(() => {
    if (IMAGES.length < 2) return;
    const timer = window.setInterval(() => {
      setActive((i) => (i + 1) % IMAGES.length);
    }, ROTATE_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div className="panel-bg" aria-hidden="true">
      {IMAGES.map((src, i) => (
        <div
          key={src}
          className={`panel-bg-layer${i === active ? ' is-active' : ''}`}
          style={{ backgroundImage: `url(${src})` }}
        />
      ))}
      <div className="panel-bg-scrim" />
    </div>
  );
}
