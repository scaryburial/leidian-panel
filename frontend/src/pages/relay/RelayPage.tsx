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
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';

import { HttpUtil } from '@/utils';

type RelayScope = 'all' | 'emails' | 'inbounds';

interface RelayRule {
  id?: string;
  enable: boolean;
  type: 'socks' | 'http';
  host: string;
  port: number;
  user: string;
  pass: string;
  scope: RelayScope;
  emails: string[];
  inbounds: string[];
  remark?: string;
}

interface Inbound {
  id: number;
  remark: string;
  tag: string;
  protocol: string;
  port: number;
  settings?: string;
}

const NEW_RULE: RelayRule = {
  enable: true,
  type: 'socks',
  host: '',
  port: 1080,
  user: '',
  pass: '',
  scope: 'all',
  emails: [],
  inbounds: [],
  remark: '',
};

export default function RelayPage() {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ rules: RelayRule[] }>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testingIdx, setTestingIdx] = useState<number | null>(null);
  const [inbounds, setInbounds] = useState<Inbound[]>([]);
  const [emails, setEmails] = useState<string[]>([]);
  const watched = Form.useWatch('rules', form);
  const rules = (watched ?? []) as RelayRule[];

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [cfg, ib] = await Promise.all([
        HttpUtil.get<{ rules: RelayRule[] }>('/panel/api/relay/config', undefined, {
          silent: true,
        }),
        HttpUtil.get<Inbound[]>('/panel/api/inbounds/list', undefined, { silent: true }),
      ]);
      if (cancelled) return;
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
      const loaded = cfg?.success && cfg.obj?.rules?.length ? cfg.obj.rules : [NEW_RULE];
      form.setFieldsValue({ rules: loaded });
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
    setSaving(true);
    const r = await HttpUtil.post('/panel/api/relay/config', { rules: values.rules ?? [] });
    setSaving(false);
    if (r.success) message.success(t('pages.relay.saved'));
  }

  async function testRule(index: number) {
    const row = form.getFieldValue(['rules', index]) as RelayRule | undefined;
    if (!row?.host || !row?.port) {
      message.error(t('pages.relay.testFail'));
      return;
    }
    setTestingIdx(index);
    const r = await HttpUtil.post<{ egressIp: string }>(
      '/panel/api/relay/test',
      { type: row.type, host: row.host, port: row.port, user: row.user, pass: row.pass },
      { silent: true },
    );
    setTestingIdx(null);
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
        <Button type="primary" onClick={save} loading={saving}>
          {t('pages.relay.save')}
        </Button>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('pages.relay.introTitle')}
        description={t('pages.relay.intro')}
      />
      <Form form={form} layout="vertical" initialValues={{ rules: [NEW_RULE] }}>
        <Form.List name="rules">
          {(fields, { add, remove }) => (
            <>
              {fields.map((field, index) => {
                const scope = rules[index]?.scope ?? 'all';
                return (
                  <Card
                    key={field.key}
                    size="small"
                    style={{ marginBottom: 12 }}
                    title={`${t('pages.relay.ruleTitle')} #${index + 1}${rules[index]?.remark ? ` · ${rules[index]!.remark}` : ''}`}
                    extra={
                      <Space>
                        <Button
                          size="small"
                          onClick={() => testRule(index)}
                          loading={testingIdx === index}
                        >
                          {t('pages.relay.test')}
                        </Button>
                        <Button
                          size="small"
                          danger
                          icon={<DeleteOutlined />}
                          onClick={() => remove(field.name)}
                          disabled={fields.length <= 1}
                        >
                          {t('pages.relay.removeRule')}
                        </Button>
                      </Space>
                    }
                  >
                    <Form.Item name={[field.name, 'id']} hidden>
                      <Input />
                    </Form.Item>
                    <Form.Item name={[field.name, 'remark']} label={t('pages.relay.remark')}>
                      <Input placeholder={t('pages.relay.remarkPlaceholder')} autoComplete="off" style={{ maxWidth: 480 }} />
                    </Form.Item>
                    <Space wrap align="start" size="large">
                      <Form.Item name={[field.name, 'enable']} label={t('pages.relay.enable')} valuePropName="checked">
                        <Switch />
                      </Form.Item>
                      <Form.Item name={[field.name, 'type']} label={t('pages.relay.type')}>
                        <Radio.Group optionType="button" buttonStyle="solid">
                          <Radio.Button value="socks">SOCKS5</Radio.Button>
                          <Radio.Button value="http">HTTP</Radio.Button>
                        </Radio.Group>
                      </Form.Item>
                      <Form.Item name={[field.name, 'host']} label={t('pages.relay.host')}>
                        <Input placeholder="1.2.3.4" autoComplete="off" style={{ width: 200 }} />
                      </Form.Item>
                      <Form.Item name={[field.name, 'port']} label={t('pages.relay.port')}>
                        <InputNumber min={1} max={65535} style={{ width: 120 }} />
                      </Form.Item>
                      <Form.Item name={[field.name, 'user']} label={t('pages.relay.user')}>
                        <Input autoComplete="off" style={{ width: 160 }} />
                      </Form.Item>
                      <Form.Item name={[field.name, 'pass']} label={t('pages.relay.pass')}>
                        <Input.Password autoComplete="new-password" style={{ width: 160 }} />
                      </Form.Item>
                    </Space>
                    <Form.Item name={[field.name, 'scope']} label={t('pages.relay.scope')}>
                      <Radio.Group>
                        <Radio value="all">{t('pages.relay.scopeAll')}</Radio>
                        <Radio value="emails">{t('pages.relay.scopeEmails')}</Radio>
                        <Radio value="inbounds">{t('pages.relay.scopeInbounds')}</Radio>
                      </Radio.Group>
                    </Form.Item>
                    {scope === 'emails' && (
                      <Form.Item name={[field.name, 'emails']} label={t('pages.relay.emails')}>
                        <Select
                          mode="tags"
                          options={emailOptions}
                          placeholder={t('pages.relay.emailsPlaceholder')}
                          style={{ maxWidth: 640 }}
                        />
                      </Form.Item>
                    )}
                    {scope === 'inbounds' && (
                      <Form.Item name={[field.name, 'inbounds']} label={t('pages.relay.inbounds')}>
                        <Select
                          mode="multiple"
                          options={inboundOptions}
                          placeholder={t('pages.relay.inboundsPlaceholder')}
                          style={{ maxWidth: 640 }}
                        />
                      </Form.Item>
                    )}
                  </Card>
                );
              })}
              <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ ...NEW_RULE })}>
                {t('pages.relay.addRule')}
              </Button>
            </>
          )}
        </Form.List>
      </Form>
    </Card>
  );
}
