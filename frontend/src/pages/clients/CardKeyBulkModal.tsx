import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Typography, message } from 'antd';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';
import { FormProvider } from 'react-hook-form';

import { formatInboundLabel } from '@/lib/inbounds/label';
import { CardKeyGenerateFormSchema } from '@/schemas/cardKey';
import type { CardKeyGenerateFormValues, CardKeyGeneratePayload } from '@/schemas/cardKey';
import { FormField, useZodForm } from '@/components/form/rhf';
import type { InboundOption } from '@/hooks/useClients';
import { formatCardKeyForDisplay, normalizeCardKey } from '@/lib/clients/card-keys';
import './CardKeyBulkModal.css';

interface CardKeyBulkModalProps {
  open: boolean;
  inbounds: InboundOption[];
  onOpenChange: (open: boolean) => void;
  /* Returns the generated keys so the modal can show a result list; null means
   * the request failed and its own toast has already been raised. */
  onGenerate: (payload: CardKeyGeneratePayload) => Promise<string[] | null>;
  onExport: (inboundId: number) => Promise<void>;
}

export default function CardKeyBulkModal({
  open,
  inbounds,
  onOpenChange,
  onGenerate,
  onExport,
}: CardKeyBulkModalProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [results, setResults] = useState<string[]>([]);

  const methods = useZodForm(CardKeyGenerateFormSchema, { defaultValues: defaultValues() });

  const inboundId = methods.watch('inboundId');

  // Adjust-while-rendering, the pattern the sibling bulk modals use: resetting
  // the result list from an effect would be a second render pass on every open.
  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      methods.reset(defaultValues());
      setResults([]);
    }
  }

  const inboundOptions = useMemo(
    () =>
      (inbounds || []).map((ib) => ({
        value: ib.id,
        label: `${formatInboundLabel(ib.tag, ib.remark) || `#${ib.id}`} · ${ib.port ?? '-'} · ${ib.protocol ?? ''}`,
      })),
    [inbounds],
  );

  async function submit() {
    const parsed = CardKeyGenerateFormSchema.safeParse(methods.getValues());
    if (!parsed.success) {
      // Surface the first Zod issue key through the same t() path the inline
      // field errors use, so nothing is shown in raw English.
      messageApi.warning(t(parsed.error.issues[0]?.message ?? 'somethingWentWrong'));
      return;
    }
    setSubmitting(true);
    try {
      const keys = await onGenerate(parsed.data);
      if (!keys) return;
      setResults(keys);
      messageApi.success(t('pages.clients.cardKeys.toasts.generated', { count: keys.length }));
    } finally {
      setSubmitting(false);
    }
  }

  async function copyAll() {
    try {
      await navigator.clipboard.writeText(results.map((k) => normalizeCardKey(k)).join('\n'));
      messageApi.success(t('pages.clients.cardKeys.copiedCount', { count: results.length }));
    } catch {
      messageApi.error(t('somethingWentWrong'));
    }
  }

  async function exportCsv() {
    if (!inboundId) return;
    setExporting(true);
    try {
      await onExport(inboundId);
    } finally {
      setExporting(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={t('pages.clients.cardKeys.title')}
        width={640}
        confirmLoading={submitting}
        mask={{ closable: false }}
        onOk={submit}
        onCancel={() => onOpenChange(false)}
        footer={[
          <Button
            key="copyAll"
            icon={<CopyOutlined />}
            disabled={results.length === 0}
            onClick={copyAll}
          >
            {t('pages.clients.cardKeys.copyAll')}
          </Button>,
          <Button
            key="exportCsv"
            icon={<DownloadOutlined />}
            loading={exporting}
            disabled={!inboundId}
            onClick={exportCsv}
          >
            {t('pages.clients.cardKeys.exportCsv')}
          </Button>,
          <Button key="cancel" onClick={() => onOpenChange(false)}>
            {t('close')}
          </Button>,
          <Button key="generate" type="primary" loading={submitting} onClick={submit}>
            {t('pages.clients.cardKeys.generate')}
          </Button>,
        ]}
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.clients.cardKeys.intro')}
        />
        <FormProvider {...methods}>
          <Form layout="vertical">
            <FormField name="inboundId" label={t('pages.clients.cardKeys.inbound')} required>
              <Select
                showSearch
                options={inboundOptions}
                placeholder={t('pages.clients.cardKeys.inboundPlaceholder')}
                filterOption={(input, option) =>
                  String(option?.label ?? '')
                    .toLowerCase()
                    .includes(input.toLowerCase())
                }
              />
            </FormField>
            <FormField name="count" label={t('pages.clients.cardKeys.count')}>
              <InputNumber min={1} max={500} step={1} precision={0} style={{ width: '100%' }} />
            </FormField>
            <FormField
              name="totalGB"
              label={t('pages.clients.totalGB')}
              tooltip={t('pages.clients.cardKeys.quotaHint')}
            >
              <InputNumber
                min={0}
                step={10}
                style={{ width: '100%' }}
                placeholder={t('pages.clients.cardKeys.quotaHint')}
              />
            </FormField>
            <FormField
              name="expiryDays"
              label={t('pages.clients.cardKeys.expiryDays')}
              tooltip={t('pages.clients.cardKeys.expiryDaysHint')}
            >
              <InputNumber
                min={0}
                step={1}
                precision={0}
                style={{ width: '100%' }}
                placeholder={t('pages.clients.cardKeys.expiryDaysHint')}
              />
            </FormField>
            <FormField name="remarkPrefix" label={t('pages.clients.cardKeys.remarkPrefix')}>
              <Input allowClear />
            </FormField>
            <FormField
              name="groupName"
              label={t('pages.clients.cardKeys.groupName')}
              tooltip={t('pages.clients.groupDesc')}
            >
              <Input allowClear placeholder={t('pages.clients.groupPlaceholder')} />
            </FormField>
          </Form>
        </FormProvider>

        {results.length > 0 && (
          <div className="card-key-result">
            <Typography.Text type="secondary">
              {t('pages.clients.cardKeys.resultTitle', { count: results.length })}
            </Typography.Text>
            {results.map((key, index) => (
              <div key={`${key}-${index}`} className="card-key-cell">
                <span className="card-key-value">{formatCardKeyForDisplay(key)}</span>
                <Button
                  size="small"
                  type="text"
                  icon={<CopyOutlined />}
                  aria-label={`${t('pages.clients.cardKeys.copyKey')} ${key}`}
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(normalizeCardKey(key));
                      messageApi.success(t('copied'));
                    } catch {
                      messageApi.error(t('somethingWentWrong'));
                    }
                  }}
                />
              </div>
            ))}
          </div>
        )}
      </Modal>
    </>
  );
}

/* Defaults live in one place so the form, the reset-on-open and the schema's
 * own coercion agree on what "untouched" means: 10 keys, 100 GB, 30 days. The
 * remark prefix is the Chinese default the panel ships with. */
function defaultValues(): CardKeyGenerateFormValues {
  return {
    inboundId: 0,
    count: 10,
    totalGB: 100,
    expiryDays: 30,
    remarkPrefix: '套餐A',
    groupName: '',
  };
}
