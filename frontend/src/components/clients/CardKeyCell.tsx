import { memo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Tooltip } from 'antd';
import { CopyOutlined, KeyOutlined } from '@ant-design/icons';

import { formatCardKeyForDisplay, normalizeCardKey } from '@/lib/clients/card-keys';

export interface CardKeyCellProps {
  cardKey?: string;
  email: string;
  generating: boolean;
  onCopy: (key: string) => void;
  onGenerate: (email: string) => void;
}

// Reads only primitives, so rc-table's traffic-driven re-renders bail out here
// for every client whose card key did not change — the whole page on most
// pushes. The copy value is normalised, never the raw cell text.
const CardKeyCell = memo(function CardKeyCell({
  cardKey,
  email,
  generating,
  onCopy,
  onGenerate,
}: CardKeyCellProps) {
  const { t } = useTranslation();
  const normalized = normalizeCardKey(cardKey);

  if (!normalized) {
    return (
      <Tooltip title={t('pages.clients.cardKeys.emptyHint')}>
        <Button
          size="small"
          icon={<KeyOutlined />}
          loading={generating}
          onClick={() => onGenerate(email)}
        >
          {t('pages.clients.cardKeys.generateOne')}
        </Button>
      </Tooltip>
    );
  }

  return (
    <span className="card-key-cell">
      <Tooltip title={t('pages.clients.cardKeys.copyKey')}>
        <span className="card-key-value">{formatCardKeyForDisplay(normalized)}</span>
      </Tooltip>
      <Tooltip title={t('pages.clients.cardKeys.copyKey')}>
        <Button
          size="small"
          type="text"
          icon={<CopyOutlined />}
          aria-label={`${t('pages.clients.cardKeys.copyKey')} ${normalized}`}
          onClick={() => onCopy(normalized)}
        />
      </Tooltip>
    </span>
  );
});

export default CardKeyCell;
