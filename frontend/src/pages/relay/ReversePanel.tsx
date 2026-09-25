import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Switch,
  message,
} from 'antd';

import { HttpUtil } from '@/utils';

interface ReverseConfig {
  enable: boolean;
  inboundId: number;
  clientEmail: string;
  tag: string;
  serverAddr: string;
  scope: 'all' | 'emails';
  emails: string[];
}

interface Inbound {
  id: number;
  remark: string;
  tag: string;
  protocol: string;
  port: number;
  settings?: string;
}

const DEFAULT: ReverseConfig = {
  enable: false,
  inboundId: 0,
  clientEmail: '',
  tag: 'ui3344rev',
  serverAddr: '',
  scope: 'all',
  emails: [],
};

export default function ReversePanel() {
  const { t } = useTranslation();
  const [form] = Form.useForm<ReverseConfig>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [scope, setScope] = useState<'all' | 'emails'>('all');
  const [inbounds, setInbounds] = useState<Inbound[]>([]);
  const [bridgeOpen, setBridgeOpen] = useState(false);
  const [bridgeText, setBridgeText] = useState('');
  const inboundId = Form.useWatch('inboundId', form);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [cfg, ib] = await Promise.all([
        HttpUtil.get<ReverseConfig>('/panel/api/reverse/config', undefined, { silent: true }),
        HttpUtil.get<Inbound[]>('/panel/api/inbounds/list', undefined, { silent: true }),
      ]);
      if (cancelled) return;
      const list = (Array.isArray(ib?.obj) ? ib.obj : []) as Inbound[];
      setInbounds(list);
      const value = { ...DEFAULT, ...(cfg?.success && cfg.obj ? cfg.obj : {}) };
      form.setFieldsValue(value);
      setScope(value.scope || 'all');
      if (!value.serverAddr && typeof window !== 'undefined') {
        value.serverAddr = window.location.hostname;
        form.setFieldValue('serverAddr', value.serverAddr);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [form]);

  const vlessInbounds = useMemo(() => inbounds.filter((i) => i.protocol === 'vless'), [inbounds]);
  const inboundOptions = useMemo(
    () => vlessInbounds.map((i) => ({ label: `${i.remark} (${i.port})`, value: i.id })),
    [vlessInbounds],
  );
  const clientEmails = useMemo(() => {
    const ib = vlessInbounds.find((i) => i.id === inboundId);
    if (!ib?.settings) return [] as string[];
    try {
      const s = JSON.parse(ib.settings) as { clients?: { email?: string }[] };
      return (s.clients ?? []).map((c) => c.email).filter((e): e is string => !!e);
    } catch {
      return [] as string[];
    }
  }, [vlessInbounds, inboundId]);

  async function save() {
    const values = await form.validateFields();
    values.scope = scope;
    setSaving(true);
    const r = await HttpUtil.post('/panel/api/reverse/config', values);
    setSaving(false);
    if (r.success) message.success(t('pages.relay.saved'));
  }

  async function showBridge() {
    const r = await HttpUtil.get<{ config: string }>('/panel/api/reverse/bridge', undefined, {
      silent: true,
    });
    if (r.success && r.obj?.config) {
      setBridgeText(r.obj.config);
      setBridgeOpen(true);
    } else {
      message.error(r.msg || t('pages.relay.revBridgeFail'));
    }
  }

  return (
    <Card
      loading={loading}
      extra={
        <Space>
          <Button onClick={showBridge}>{t('pages.relay.revBridge')}</Button>
          <Button type="primary" onClick={save} loading={saving}>
            {t('pages.relay.save')}
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('pages.relay.revIntroTitle')}
        description={t('pages.relay.revIntro')}
      />
      <Form form={form} layout="vertical" initialValues={DEFAULT}>
        <Form.Item name="enable" label={t('pages.relay.revEnable')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Space wrap align="start" size="large">
          <Form.Item name="inboundId" label={t('pages.relay.revInbound')} style={{ minWidth: 260 }}>
            <Select options={inboundOptions} />
          </Form.Item>
          <Form.Item name="clientEmail" label={t('pages.relay.revClient')} style={{ minWidth: 220 }}>
            <Select options={clientEmails.map((e) => ({ label: e, value: e }))} />
          </Form.Item>
          <Form.Item name="tag" label={t('pages.relay.revTag')}>
            <Input style={{ width: 180 }} />
          </Form.Item>
          <Form.Item name="serverAddr" label={t('pages.relay.revServer')}>
            <Input style={{ width: 200 }} />
          </Form.Item>
        </Space>
        <div style={{ color: '#888', marginBottom: 12 }}>{t('pages.relay.revHint')}</div>
        <Form.Item label={t('pages.relay.revScope')}>
          <Radio.Group value={scope} onChange={(e) => setScope(e.target.value)}>
            <Radio value="all">{t('pages.relay.revScopeAll')}</Radio>
            <Radio value="emails">{t('pages.relay.revScopeEmails')}</Radio>
          </Radio.Group>
        </Form.Item>
        {scope === 'emails' && (
          <Form.Item name="emails" label={t('pages.relay.revEmails')}>
            <Select
              mode="tags"
              style={{ maxWidth: 640 }}
              placeholder={t('pages.relay.emailsPlaceholder')}
            />
          </Form.Item>
        )}
      </Form>

      <Modal
        open={bridgeOpen}
        title={t('pages.relay.revBridge')}
        width={760}
        onCancel={() => setBridgeOpen(false)}
        footer={[
          <Button
            key="copy"
            onClick={() => {
              void navigator.clipboard.writeText(bridgeText);
              message.success(t('copied'));
            }}
          >
            {t('copy')}
          </Button>,
          <Button key="close" type="primary" onClick={() => setBridgeOpen(false)}>
            {t('close')}
          </Button>,
        ]}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('pages.relay.revBridgeHint')}
        />
        <Input.TextArea value={bridgeText} readOnly autoSize={{ minRows: 12, maxRows: 24 }} />
      </Modal>
    </Card>
  );
}
