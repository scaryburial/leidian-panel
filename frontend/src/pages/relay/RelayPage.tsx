import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Space,
  Switch,
  message,
} from 'antd';

import { HttpUtil } from '@/utils';

type RelayScope = 'all' | 'emails' | 'inbounds';

interface RelayConfig {
  enable: boolean;
  type: 'socks' | 'http';
  host: string;
  port: number;
  user: string;
  pass: string;
  scope: RelayScope;
  emails: string[];
  inbounds: string[];
}

interface Inbound {
  id: number;
  remark: string;
  tag: string;
  protocol: string;
  port: number;
  settings?: string;
}

const DEFAULT: RelayConfig = {
  enable: false,
  type: 'socks',
  host: '',
  port: 1080,
  user: '',
  pass: '',
  scope: 'all',
  emails: [],
  inbounds: [],
};

export default function RelayPage() {
  const { t } = useTranslation();
  const [form] = Form.useForm<RelayConfig>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [scope, setScope] = useState<RelayScope>('all');
  const [inbounds, setInbounds] = useState<Inbound[]>([]);
  const [emails, setEmails] = useState<string[]>([]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [cfg, ib] = await Promise.all([
        HttpUtil.get<RelayConfig>('/panel/api/relay/config', undefined, { silent: true }),
        HttpUtil.get<Inbound[]>('/panel/api/inbounds/list', undefined, { silent: true }),
      ]);
      if (cancelled) return;
      const value = cfg?.success && cfg.obj ? { ...DEFAULT, ...cfg.obj } : DEFAULT;
      form.setFieldsValue(value);
      setScope(value.scope || 'all');
      const list = (Array.isArray(ib?.obj) ? ib.obj : []) as Inbound[];
      setInbounds(list);
      const set = new Set<string>();
      for (const x of list) {
        try {
          const s = x.settings ? JSON.parse(x.settings) : null;
          const clients = (s?.clients ?? []) as { email?: string }[];
          for (const c of clients) if (c?.email) set.add(c.email);
        } catch {
          // ignore malformed settings
        }
      }
      setEmails([...set].sort());
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [form]);

  const inboundOptions = useMemo(
    () =>
      inbounds.map((i) => ({
        label: `${i.remark} (${i.protocol}:${i.port})`,
        value: i.tag,
      })),
    [inbounds],
  );
  const emailOptions = useMemo(() => emails.map((e) => ({ label: e, value: e })), [emails]);

  async function save() {
    const values = await form.validateFields();
    values.scope = scope;
    setSaving(true);
    const r = await HttpUtil.post('/panel/api/relay/config', values);
    setSaving(false);
    if (r.success) message.success(t('pages.relay.saved'));
  }

  async function test() {
    const values = await form.validateFields(['type', 'host', 'port', 'user', 'pass']);
    setTesting(true);
    const r = await HttpUtil.post<{ egressIp: string }>('/panel/api/relay/test', values, {
      silent: true,
    });
    setTesting(false);
    if (r.success && r.obj?.egressIp) {
      message.success(t('pages.relay.testOk', { ip: r.obj.egressIp }));
    } else {
      message.error(r.msg || t('pages.relay.testFail'));
    }
  }

  return (
    <Card
      title={t('menu.relay')}
      loading={loading}
      extra={
        <Space>
          <Button onClick={test} loading={testing}>
            {t('pages.relay.test')}
          </Button>
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
        message={t('pages.relay.introTitle')}
        description={t('pages.relay.intro')}
      />
      <Form form={form} layout="vertical" initialValues={DEFAULT}>
        <Form.Item name="enable" label={t('pages.relay.enable')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="type" label={t('pages.relay.type')}>
          <Radio.Group optionType="button" buttonStyle="solid">
            <Radio.Button value="socks">SOCKS5</Radio.Button>
            <Radio.Button value="http">HTTP</Radio.Button>
          </Radio.Group>
        </Form.Item>
        <Space align="start" size="large" wrap>
          <Form.Item
            name="host"
            label={t('pages.relay.host')}
            rules={[{ required: false }]}
            style={{ minWidth: 260 }}
          >
            <Input placeholder="1.2.3.4" autoComplete="off" />
          </Form.Item>
          <Form.Item name="port" label={t('pages.relay.port')}>
            <InputNumber min={1} max={65535} style={{ width: 140 }} />
          </Form.Item>
          <Form.Item name="user" label={t('pages.relay.user')}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="pass" label={t('pages.relay.pass')}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Space>
        <Form.Item label={t('pages.relay.scope')}>
          <Radio.Group value={scope} onChange={(e) => setScope(e.target.value)}>
            <Radio value="all">{t('pages.relay.scopeAll')}</Radio>
            <Radio value="emails">{t('pages.relay.scopeEmails')}</Radio>
            <Radio value="inbounds">{t('pages.relay.scopeInbounds')}</Radio>
          </Radio.Group>
        </Form.Item>
        {scope === 'emails' && (
          <Form.Item name="emails" label={t('pages.relay.emails')}>
            <Select
              mode="tags"
              options={emailOptions}
              placeholder={t('pages.relay.emailsPlaceholder')}
              style={{ maxWidth: 640 }}
            />
          </Form.Item>
        )}
        {scope === 'inbounds' && (
          <Form.Item name="inbounds" label={t('pages.relay.inbounds')}>
            <Select
              mode="multiple"
              options={inboundOptions}
              placeholder={t('pages.relay.inboundsPlaceholder')}
              style={{ maxWidth: 640 }}
            />
          </Form.Item>
        )}
      </Form>
    </Card>
  );
}
