import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, Descriptions, Form, Input, message, Space, Tag } from 'antd';

import { HttpUtil } from '@/utils';

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } } as const;

interface DomainStatus {
  enabled: boolean;
  fqdn: string;
  rootDomain: string;
  serverIp: string;
  certMode: string;
  certPath: string;
  proxied: boolean;
  cfConfigured: boolean;
  steps?: string[];
}

const EMPTY: DomainStatus = {
  enabled: false,
  fqdn: '',
  rootDomain: '578272.xyz',
  serverIp: '',
  certMode: '',
  certPath: '',
  proxied: false,
  cfConfigured: false,
};

export default function DomainPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<DomainStatus>(EMPTY);
  const [steps, setSteps] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm<{ subdomain: string; totp: string }>();
  const [tokenForm] = Form.useForm<{ token: string; totp: string }>();

  async function refresh() {
    const r = await HttpUtil.get<DomainStatus>('/panel/api/domain/config', undefined, { silent: true });
    if (r?.success && r.obj) setStatus({ ...EMPTY, ...r.obj });
    setLoading(false);
  }

  useEffect(() => {
    void refresh();
  }, []);

  async function preview() {
    const sub = form.getFieldValue('subdomain') || '';
    const r = await HttpUtil.post<DomainStatus>(
      '/panel/api/domain/preview',
      { subdomain: sub },
      JSON_HEADERS,
    );
    if (r?.success && r.obj?.steps) setSteps(r.obj.steps);
  }

  async function enable() {
    const v = await form.validateFields();
    setBusy(true);
    const r = await HttpUtil.post<DomainStatus>(
      '/panel/api/domain/enable',
      { subdomain: v.subdomain || '', totp: v.totp || '' },
      JSON_HEADERS,
    );
    setBusy(false);
    if (r?.success) {
      message.success(t('pages.domain.enabled'));
      await refresh();
    } else {
      message.error(r?.msg || t('pages.domain.failed'));
    }
  }

  async function disable() {
    const v = await form.validateFields(['totp']);
    setBusy(true);
    const r = await HttpUtil.post(
      '/panel/api/domain/disable',
      { totp: v.totp || '' },
      JSON_HEADERS,
    );
    setBusy(false);
    if (r?.success) {
      message.success(t('pages.domain.disabled'));
      await refresh();
    } else {
      message.error(r?.msg || t('pages.domain.failed'));
    }
  }

  async function saveToken() {
    const v = await tokenForm.validateFields();
    const r = await HttpUtil.post(
      '/panel/api/domain/token',
      { token: v.token, totp: v.totp },
      JSON_HEADERS,
    );
    if (r?.success) {
      message.success(t('pages.domain.saved'));
      tokenForm.resetFields();
      await refresh();
    } else {
      message.error(r?.msg || t('pages.domain.failed'));
    }
  }

  return (
    <Space direction="vertical" size="middle" style={{ display: 'flex' }}>
      <Card title={t('pages.domain.title')} loading={loading}>
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('pages.domain.introTitle')}
          description={t('pages.domain.intro')}
        />
        <Descriptions column={1} size="small" bordered style={{ marginBottom: 16 }}>
          <Descriptions.Item label={t('pages.domain.state')}>
            {status.enabled ? (
              <Tag color="green">{t('pages.domain.on')}</Tag>
            ) : (
              <Tag>{t('pages.domain.off')}</Tag>
            )}
          </Descriptions.Item>
          <Descriptions.Item label={t('pages.domain.fqdn')}>{status.fqdn || '-'}</Descriptions.Item>
          <Descriptions.Item label={t('pages.domain.serverIp')}>{status.serverIp || '-'}</Descriptions.Item>
          <Descriptions.Item label={t('pages.domain.cert')}>
            {status.certMode || '-'} {status.certPath}
          </Descriptions.Item>
          <Descriptions.Item label={t('pages.domain.proxied')}>
            {status.proxied ? <Tag color="orange">Cloudflare</Tag> : '-'}
          </Descriptions.Item>
          <Descriptions.Item label={t('pages.domain.cfToken')}>
            {status.cfConfigured ? <Tag color="green">{t('pages.domain.configured')}</Tag> : <Tag color="red">-</Tag>}
          </Descriptions.Item>
        </Descriptions>

        <Form form={form} layout="vertical" style={{ maxWidth: 520 }}>
          <Form.Item name="subdomain" label={t('pages.domain.subdomain')}>
            <Input placeholder={`${'abc123'}.578272.xyz`} allowClear />
          </Form.Item>
          <Form.Item name="totp" label={t('pages.domain.totp')} rules={[{ required: true, len: 6 }]}>
            <Input placeholder="123456" maxLength={6} />
          </Form.Item>
          <Space>
            <Button onClick={preview}>{t('pages.domain.preview')}</Button>
            <Button type="primary" loading={busy} onClick={enable}>
              {t('pages.domain.enable')}
            </Button>
            <Button danger loading={busy} disabled={!status.enabled} onClick={disable}>
              {t('pages.domain.disable')}
            </Button>
          </Space>
        </Form>

        {steps.length > 0 && (
          <Alert
            type="warning"
            showIcon
            style={{ marginTop: 16 }}
            message={t('pages.domain.previewTitle')}
            description={
              <ol style={{ margin: 0, paddingInlineStart: 20 }}>
                {steps.map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ol>
            }
          />
        )}
      </Card>

      <Card title={t('pages.domain.cfTitle')} size="small">
        <Form form={tokenForm} layout="vertical" style={{ maxWidth: 520 }}>
          <Form.Item name="token" label={t('pages.domain.cfTokenInput')} rules={[{ required: true }]}>
            <Input.Password placeholder="Cloudflare API Token" autoComplete="off" />
          </Form.Item>
          <Form.Item name="totp" label={t('pages.domain.totp')} rules={[{ required: true, len: 6 }]}>
            <Input maxLength={6} />
          </Form.Item>
          <Button onClick={saveToken}>{t('pages.domain.save')}</Button>
        </Form>
      </Card>
    </Space>
  );
}
