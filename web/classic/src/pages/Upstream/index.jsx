/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import React, { useEffect, useState, useCallback, useRef } from 'react';
import {
  Button,
  Table,
  Modal,
  Form,
  Tag,
  Space,
  Popconfirm,
  Card,
  Spin,
  Typography,
  Input,
  Select,
  Banner,
  Toast,
  Tabs,
} from '@douyinfe/semi-ui';
import {
  IconPlus,
  IconRefresh,
  IconDelete,
  IconEdit,
  IconCopy,
  IconSearch,
  IconEyeOpened,
  IconTick,
  IconClose,
} from '@douyinfe/semi-icons';
import { useTranslation } from 'react-i18next';
import { API, showError, showSuccess } from '../../helpers';
import { copy } from '../../helpers/utils';
import UpstreamKeyTestModal from './UpstreamKeyTestModal';

const { Text, Title } = Typography;

const timeAgo = (timestamp) => {
  if (!timestamp) return '-';
  const seconds = Math.floor(Date.now() / 1000 - timestamp);
  if (seconds < 60) return '刚刚';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}小时前`;
  const days = Math.floor(hours / 24);
  return `${days}天前`;
};

const pingSite = async (url) => {
  const start = performance.now();
  try {
    await fetch(url, { method: 'HEAD', mode: 'no-cors', cache: 'no-store', signal: AbortSignal.timeout(5000) });
    return Math.round(performance.now() - start);
  } catch {
    try {
      await fetch(url, { mode: 'no-cors', cache: 'no-store', signal: AbortSignal.timeout(5000) });
      return Math.round(performance.now() - start);
    } catch {
      return -1;
    }
  }
};

const Upstream = () => {
  const { t } = useTranslation();
  const [sites, setSites] = useState([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [keyword, setKeyword] = useState('');

  const [formVisible, setFormVisible] = useState(false);
  const [editingSite, setEditingSite] = useState(null);
  const [formLoading, setFormLoading] = useState(false);
  const [testLoading, setTestLoading] = useState(false);

  const [detailVisible, setDetailVisible] = useState(false);
  const [detailSite, setDetailSite] = useState(null);
  const [groups, setGroups] = useState({});
  const [tokens, setTokens] = useState({});
  const [detailLoading, setDetailLoading] = useState(false);
  const [syncLoading, setSyncLoading] = useState(false);
  const [creatingGroup, setCreatingGroup] = useState(null);

  const [syncingIds, setSyncingIds] = useState(new Set());
  const [syncAllLoading, setSyncAllLoading] = useState(false);

  const [latencies, setLatencies] = useState({});

  const [searchKeyword, setSearchKeyword] = useState('');
  const [searchResults, setSearchResults] = useState([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchTotal, setSearchTotal] = useState(0);

  // Unified key test modal
  const [keyTestVisible, setKeyTestVisible] = useState(false);
  const [keyTestTarget, setKeyTestTarget] = useState(null);

  const openKeyTestForGroup = useCallback((site, groupName) => {
    setKeyTestTarget({
      site_id: site.id,
      group_name: groupName,
      base_url: site.base_url,
    });
    setKeyTestVisible(true);
  }, []);

  const formApiRef = useRef();
  const originalRemarks = useRef({});

  const pingAllSites = useCallback((siteList) => {
    siteList.forEach(async (site) => {
      const ms = await pingSite(site.base_url);
      setLatencies((prev) => ({ ...prev, [site.id]: ms }));
    });
  }, []);

  const loadSites = useCallback(async () => {
    setLoading(true);
    try {
      let url = `/api/upstream/?p=${page}&page_size=${pageSize}`;
      if (keyword) url += `&keyword=${encodeURIComponent(keyword)}`;
      const res = await API.get(url);
      if (res.data.success) {
        const items = res.data.data?.items || [];
        setSites(items);
        setTotal(res.data.data?.total || 0);
        const remarks = {};
        items.forEach((s) => { remarks[s.id] = s.remark || ''; });
        originalRemarks.current = remarks;
        pingAllSites(items);
      } else {
        showError(res.data.message);
      }
    } catch (e) {
      showError(t('加载失败'));
    }
    setLoading(false);
  }, [page, pageSize, keyword, t, pingAllSites]);

  useEffect(() => {
    loadSites();
  }, [loadSites]);

  const handleAdd = () => {
    setEditingSite(null);
    setFormVisible(true);
  };

  const handleEdit = (site) => {
    setEditingSite(site);
    setFormVisible(true);
  };

  const handleDelete = async (id) => {
    try {
      const res = await API.delete(`/api/upstream/${id}`);
      if (res.data.success) {
        showSuccess(t('删除成功'));
        loadSites();
      } else {
        showError(res.data.message);
      }
    } catch (e) {
      showError(t('删除失败'));
    }
  };

  const handleSync = async (site) => {
    setSyncingIds((prev) => new Set(prev).add(site.id));
    try {
      const res = await API.post(`/api/upstream/${site.id}/sync`);
      if (res.data.success) {
        showSuccess(t('同步成功'));
        loadSites();
      } else {
        showError(res.data.message || t('同步失败'));
      }
    } catch (e) {
      showError(t('同步失败'));
    }
    setSyncingIds((prev) => {
      const next = new Set(prev);
      next.delete(site.id);
      return next;
    });
  };

  const handleSyncAll = async () => {
    setSyncAllLoading(true);
    try {
      const res = await API.post('/api/upstream/sync-all');
      if (res.data.success) {
        showSuccess(t('全部同步完成') + ` (${res.data.data?.total || 0})`);
        loadSites();
      } else {
        showError(res.data.message || t('同步失败'));
      }
    } catch (e) {
      showError(t('同步失败'));
    }
    setSyncAllLoading(false);
  };

  const handleTestConnection = async () => {
    const values = formApiRef.current?.getValues() || {};
    if (!values.base_url || !values.username || !values.password) {
      showError(t('请填写URL、用户名和密码'));
      return;
    }
    setTestLoading(true);
    try {
      const res = await API.post('/api/upstream/test', {
        base_url: values.base_url,
        username: values.username,
        password: values.password,
        site_type: values.site_type || 'newapi',
      });
      if (res.data.success) {
        showSuccess(t('连接测试成功'));
      } else {
        showError(res.data.message || t('连接测试失败'));
      }
    } catch (e) {
      const msg = e?.response?.data?.message || e?.message || t('连接测试失败');
      showError(msg);
    }
    setTestLoading(false);
  };

  const handleFormSubmit = async (values) => {
    setFormLoading(true);
    try {
      if (editingSite) {
        const data = { id: editingSite.id, ...values };
        if (!data.password) delete data.password;
        const res = await API.put('/api/upstream/', data);
        if (res.data.success) {
          showSuccess(t('更新成功'));
          setFormVisible(false);
          loadSites();
        } else {
          showError(res.data.message);
        }
      } else {
        const res = await API.post('/api/upstream/', values);
        if (res.data.success) {
          showSuccess(t('添加成功'));
          setFormVisible(false);
          loadSites();
        } else {
          showError(res.data.message);
        }
      }
    } catch (e) {
      showError(t('操作失败'));
    }
    setFormLoading(false);
  };

  const handleRemarkBlur = async (siteId, value) => {
    if (originalRemarks.current[siteId] === value) return;
    originalRemarks.current[siteId] = value;
    setSites((prev) =>
      prev.map((s) => (s.id === siteId ? { ...s, remark: value } : s)),
    );
    try {
      await API.patch(`/api/upstream/${siteId}/remark`, { remark: value });
    } catch (e) {
      // silent
    }
  };

  const handleShowDetail = async (site) => {
    setDetailSite(site);
    setDetailVisible(true);
    setDetailLoading(true);
    try {
      const res = await API.get(`/api/upstream/${site.id}/groups`);
      if (res.data.success) {
        setGroups(res.data.data?.groups || {});
        setTokens(res.data.data?.tokens || {});
      }
    } catch (e) {
      showError(t('加载分组失败'));
    }
    setDetailLoading(false);
  };

  const handleDetailSync = async () => {
    if (!detailSite) return;
    setSyncLoading(true);
    try {
      const res = await API.post(`/api/upstream/${detailSite.id}/sync`);
      if (res.data.success) {
        setGroups(res.data.data?.groups || {});
        setTokens(res.data.data?.tokens || {});
        showSuccess(t('同步成功'));
        loadSites();
      } else {
        showError(res.data.message || t('同步失败'));
      }
    } catch (e) {
      showError(t('同步失败'));
    }
    setSyncLoading(false);
  };

  const handleCreateToken = async (groupName) => {
    if (!detailSite) return;
    setCreatingGroup(groupName);
    try {
      const res = await API.post(
        `/api/upstream/${detailSite.id}/groups/${encodeURIComponent(groupName)}/token`,
      );
      if (res.data.success) {
        setTokens((prev) => ({
          ...prev,
          [groupName]: res.data.data,
        }));
        showSuccess(t('密钥创建成功'));
      } else {
        showError(res.data.message || t('创建失败'));
      }
    } catch (e) {
      showError(t('创建失败'));
    }
    setCreatingGroup(null);
  };

  const handleSearchKeys = async () => {
    setSearchLoading(true);
    try {
      const res = await API.get(`/api/upstream/search-keys?keyword=${encodeURIComponent(searchKeyword)}`);
      if (res.data.success) {
        setSearchResults(res.data.data?.results || []);
        setSearchTotal(res.data.data?.total || 0);
      } else {
        showError(res.data.message);
      }
    } catch (e) {
      showError(t('搜索失败'));
    }
    setSearchLoading(false);
  };

  const handleCopyKey = (key) => {
    copy(key);
    showSuccess(t('已复制到剪贴板'));
  };

  const latencyColor = (ms) => {
    if (ms < 0) return 'var(--semi-color-danger)';
    if (ms < 500) return 'var(--semi-color-success)';
    if (ms < 1500) return 'var(--semi-color-warning)';
    return 'var(--semi-color-danger)';
  };

  const columns = [
    {
      title: t('站点'),
      dataIndex: 'name',
      key: 'name',
      render: (text, record) => (
        <div>
          <Text strong>{text}</Text>
          <div
            style={{
              fontSize: 11,
              color: 'var(--semi-color-text-2)',
              cursor: 'pointer',
              marginTop: 2,
            }}
            onClick={(e) => {
              e.stopPropagation();
              copy(record.base_url);
              showSuccess(t('已复制'));
            }}
            title={t('点击复制')}
          >
            {record.base_url}
          </div>
        </div>
      ),
    },
    {
      title: t('余额'),
      dataIndex: 'balance',
      key: 'balance',
      width: 100,
      render: (text) => (
        <Text strong style={{ fontFamily: 'monospace' }}>
          ${text || '0.00'}
        </Text>
      ),
    },
    {
      title: t('延迟'),
      key: 'latency',
      width: 80,
      render: (_, record) => {
        const ms = latencies[record.id];
        if (ms === undefined) return <Text type='tertiary'>...</Text>;
        if (ms < 0) return <Text style={{ color: latencyColor(ms), fontFamily: 'monospace', fontSize: 12 }}>超时</Text>;
        return (
          <Text style={{ color: latencyColor(ms), fontFamily: 'monospace', fontSize: 12 }}>
            {ms}ms
          </Text>
        );
      },
    },
    {
      title: t('状态'),
      dataIndex: 'status',
      key: 'status',
      width: 70,
      render: (status) =>
        status === 1 ? (
          <Tag color='green' size='small' prefixIcon={<IconTick />}>
            {t('正常')}
          </Tag>
        ) : (
          <Tag color='red' size='small' prefixIcon={<IconClose />}>
            {t('禁用')}
          </Tag>
        ),
    },
    {
      title: t('备注'),
      dataIndex: 'remark',
      key: 'remark',
      width: 160,
      render: (text, record) => (
        <textarea
          rows={2}
          defaultValue={text || ''}
          placeholder={t('备注')}
          onBlur={(e) => handleRemarkBlur(record.id, e.target.value)}
          style={{
            width: '100%',
            resize: 'none',
            border: '1px solid var(--semi-color-border)',
            borderRadius: 4,
            padding: '4px 8px',
            fontSize: 13,
            lineHeight: '1.4',
            background: 'transparent',
            color: 'inherit',
            outline: 'none',
            fontFamily: 'inherit',
          }}
          onFocus={(e) => { e.target.style.borderColor = 'var(--semi-color-primary)'; }}
        />
      ),
    },
    {
      title: t('同步'),
      dataIndex: 'last_sync_time',
      key: 'last_sync_time',
      width: 100,
      render: (time, record) => (
        <div>
          <div style={{ fontSize: 12 }}>{timeAgo(time)}</div>
          {record.last_sync_error && (
            <Text
              type='danger'
              style={{ fontSize: 11 }}
              ellipsis={{ showTooltip: true, pos: 'middle' }}
            >
              {record.last_sync_error}
            </Text>
          )}
        </div>
      ),
    },
    {
      title: t('操作'),
      key: 'actions',
      width: 180,
      render: (_, record) => (
        <Space>
          <Button
            icon={<IconEyeOpened />}
            size='small'
            theme='borderless'
            onClick={() => handleShowDetail(record)}
          />
          <Button
            icon={<IconRefresh spin={syncingIds.has(record.id)} />}
            size='small'
            theme='borderless'
            onClick={() => handleSync(record)}
            disabled={syncingIds.has(record.id)}
          />
          <Button
            icon={<IconEdit />}
            size='small'
            theme='borderless'
            onClick={() => handleEdit(record)}
          />
          <Popconfirm
            title={t('确定删除此站点吗？')}
            onConfirm={() => handleDelete(record.id)}
          >
            <Button
              icon={<IconDelete />}
              size='small'
              theme='borderless'
              type='danger'
            />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const searchColumns = [
    {
      title: t('站点'),
      key: 'site',
      width: 160,
      render: (_, record) => (
        <div>
          <Text strong style={{ fontSize: 13 }}>{record.site_name}</Text>
          <div
            style={{ fontSize: 11, color: 'var(--semi-color-text-2)', cursor: 'pointer', marginTop: 1 }}
            onClick={() => { copy(record.base_url); showSuccess(t('已复制')); }}
          >
            {record.base_url}
          </div>
        </div>
      ),
    },
    {
      title: t('分组'),
      dataIndex: 'group_name',
      key: 'group_name',
      width: 200,
      render: (text) => <Text strong style={{ fontSize: 13 }}>{text}</Text>,
    },
    {
      title: t('倍率'),
      dataIndex: 'ratio',
      key: 'ratio',
      width: 70,
      sorter: (a, b) => (Number(a.ratio) || 0) - (Number(b.ratio) || 0),
      render: (v) => (
        <Tag color='blue' size='small'>{v ?? '-'}</Tag>
      ),
    },
    {
      title: t('描述'),
      dataIndex: 'desc',
      key: 'desc',
      width: 200,
      render: (text) => (
        <div
          title={text || ''}
          style={{
            fontSize: 12,
            color: 'var(--semi-color-text-2)',
            maxWidth: 200,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
          }}
        >
          {text || '-'}
        </div>
      ),
    },
    {
      title: t('密钥'),
      dataIndex: 'key',
      key: 'key',
      width: 380,
      render: (key) =>
        key ? (
          <div
            style={{
              fontFamily: 'monospace',
              fontSize: 11,
              cursor: 'pointer',
              wordBreak: 'break-all',
              padding: '2px 6px',
              background: 'var(--semi-color-fill-0)',
              borderRadius: 3,
            }}
            onClick={() => handleCopyKey(key)}
            title={t('点击复制')}
          >
            {key}
            <IconCopy size='extra-small' style={{ marginLeft: 4, verticalAlign: 'middle' }} />
          </div>
        ) : (
          <Text type='tertiary' style={{ fontSize: 12 }}>-</Text>
        ),
    },
  ];

  const groupEntries = Object.entries(groups).filter(
    ([name]) => name !== 'auto',
  );

  return (
    <div className='mt-[60px] px-2'>
      <Tabs type='line'>
        <Tabs.TabPane tab={t('站点管理')} itemKey='sites'>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 16,
          marginTop: 12,
        }}
      >
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <Input
            prefix={<IconSearch />}
            placeholder={t('搜索站点名称或URL')}
            value={keyword}
            onChange={setKeyword}
            onEnterPress={loadSites}
            style={{ width: 280 }}
            showClear
          />
        </div>
        <Space>
          <Button
            onClick={() => {
              setKeyTestTarget({ adhoc: true });
              setKeyTestVisible(true);
            }}
          >
            {t('快速测试 Key')}
          </Button>
          <Button
            icon={<IconRefresh spin={syncAllLoading} />}
            onClick={handleSyncAll}
            loading={syncAllLoading}
          >
            {t('全部同步')}
          </Button>
          <Button
            icon={<IconPlus />}
            theme='solid'
            type='primary'
            onClick={handleAdd}
          >
            {t('添加站点')}
          </Button>
        </Space>
      </div>

      <Table
        columns={columns}
        dataSource={sites}
        rowKey='id'
        loading={loading}
        pagination={{
          currentPage: page,
          pageSize,
          total,
          onPageChange: setPage,
          onPageSizeChange: (size) => {
            setPageSize(size);
            setPage(1);
          },
          showSizeChanger: true,
          pageSizeOpts: [10, 20, 50],
        }}
      />

        </Tabs.TabPane>

        <Tabs.TabPane tab={t('密钥搜索')} itemKey='search'>
          <div style={{ marginTop: 12, marginBottom: 16, display: 'flex', gap: 8 }}>
            <Input
              prefix={<IconSearch />}
              placeholder={t('输入关键词搜索分组和密钥，如 max、kiro、codex')}
              value={searchKeyword}
              onChange={setSearchKeyword}
              onEnterPress={handleSearchKeys}
              style={{ width: 400 }}
              showClear
            />
            <Button
              theme='solid'
              type='primary'
              onClick={handleSearchKeys}
              loading={searchLoading}
            >
              {t('搜索')}
            </Button>
          </div>
          {searchTotal > 0 && (
            <Text type='tertiary' style={{ marginBottom: 12, display: 'block', fontSize: 13 }}>
              {t('找到')} {searchTotal} {t('个匹配结果')}
            </Text>
          )}
          <Table
            columns={searchColumns}
            dataSource={searchResults}
            rowKey={(r, i) => `${r.site_id}-${r.group_name}-${i}`}
            loading={searchLoading}
            pagination={false}
            empty={
              <Text type='tertiary'>{t('输入关键词后点击搜索')}</Text>
            }
          />
        </Tabs.TabPane>
      </Tabs>

      {/* Add/Edit Modal */}
      <Modal
        title={editingSite ? t('编辑站点') : t('添加站点')}
        visible={formVisible}
        onCancel={() => setFormVisible(false)}
        footer={null}
        closeOnEsc
        width={520}
      >
        <Form
          getFormApi={(api) => (formApiRef.current = api)}
          onSubmit={handleFormSubmit}
          initValues={
            editingSite
              ? {
                  name: editingSite.name,
                  base_url: editingSite.base_url,
                  username: editingSite.username,
                  password: '',
                  site_type: editingSite.site_type,
                  remark: editingSite.remark || '',
                }
              : { site_type: 'newapi' }
          }
          labelPosition='left'
          labelWidth={80}
        >
          <Form.Input
            field='name'
            label={t('名称')}
            placeholder={t('站点显示名称')}
            rules={[{ required: true, message: t('请填写名称') }]}
          />
          <Form.Input
            field='base_url'
            label='URL'
            placeholder='https://api.example.com'
            rules={[{ required: true, message: t('请填写URL') }]}
          />
          <Form.Input
            field='username'
            label={t('用户名')}
            rules={[{ required: true, message: t('请填写用户名') }]}
          />
          <Form.Input
            field='password'
            label={t('密码')}
            mode='password'
            placeholder={editingSite ? t('留空保持不变') : ''}
            rules={
              editingSite
                ? []
                : [{ required: true, message: t('请填写密码') }]
            }
          />
          <Form.Select field='site_type' label={t('类型')} style={{ width: '100%' }}>
            <Select.Option value='newapi'>New API</Select.Option>
            <Select.Option value='sub2api'>Sub2API</Select.Option>
          </Form.Select>
          <Form.Input field='remark' label={t('备注')} placeholder={t('可选备注')} />

          <div
            style={{
              display: 'flex',
              justifyContent: 'flex-end',
              gap: 8,
              marginTop: 16,
            }}
          >
            <Button onClick={handleTestConnection} loading={testLoading}>
              {t('测试连接')}
            </Button>
            <Button
              theme='solid'
              type='primary'
              htmlType='submit'
              loading={formLoading}
            >
              {editingSite ? t('更新') : t('添加')}
            </Button>
          </div>
        </Form>
      </Modal>

      {/* Detail Modal */}
      <Modal
        title={
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              paddingRight: 24,
            }}
          >
            <span>{detailSite?.name}</span>
            <Button
              icon={<IconRefresh />}
              size='small'
              onClick={handleDetailSync}
              loading={syncLoading}
            >
              {t('同步')}
            </Button>
          </div>
        }
        visible={detailVisible}
        onCancel={() => setDetailVisible(false)}
        footer={null}
        width={700}
        closeOnEsc
        style={{ maxHeight: '80vh' }}
        bodyStyle={{ overflow: 'auto', maxHeight: 'calc(80vh - 100px)' }}
      >
        {detailSite && (
          <div>
            <div
              style={{
                display: 'grid',
                gridTemplateColumns: '1fr 1fr',
                gap: 12,
                marginBottom: 16,
              }}
            >
              <div>
                <Text type='tertiary'>URL: </Text>
                <Text copyable style={{ fontSize: 12, fontFamily: 'monospace' }}>
                  {detailSite.base_url}
                </Text>
              </div>
              <div>
                <Text type='tertiary'>{t('余额')}: </Text>
                <Text strong style={{ fontFamily: 'monospace' }}>
                  ${detailSite.balance || '0.00'}
                </Text>
              </div>
              <div>
                <Text type='tertiary'>{t('类型')}: </Text>
                <Tag>{detailSite.site_type}</Tag>
              </div>
              <div>
                <Text type='tertiary'>{t('用户名')}: </Text>
                <Text>{detailSite.username}</Text>
              </div>
            </div>

            <Title heading={6} style={{ marginBottom: 12 }}>
              {t('分组与密钥')} ({groupEntries.length})
            </Title>

            {detailLoading ? (
              <div style={{ textAlign: 'center', padding: 40 }}>
                <Spin size='large' />
              </div>
            ) : groupEntries.length === 0 ? (
              <Banner
                type='info'
                description={t('暂无分组数据，点击同步获取')}
              />
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {groupEntries.map(([groupName, groupInfo]) => {
                  const token = tokens[groupName];
                  const hasKey = token && token.key;
                  const isCreating = creatingGroup === groupName;

                  return (
                    <Card
                      key={groupName}
                      bodyStyle={{ padding: '12px 16px' }}
                    >
                      <div
                        style={{
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                        }}
                      >
                        <div
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: 8,
                          }}
                        >
                          <Text strong>{groupName}</Text>
                          <Tag color='blue' size='small'>
                            {t('倍率')}:{' '}
                            {typeof groupInfo.ratio === 'number'
                              ? groupInfo.ratio
                              : groupInfo.ratio}
                          </Tag>
                          {token && token.status === 1 && (
                            <Tag color='green' size='small'>
                              {t('已有密钥')}
                            </Tag>
                          )}
                        </div>
                        <Space>
                          {hasKey && (
                            <Button
                              size='small'
                              type='primary'
                              theme='light'
                              onClick={() => openKeyTestForGroup(detailSite, groupName)}
                            >
                              {t('测试')}
                            </Button>
                          )}
                          {hasKey ? (
                            <Button
                              icon={<IconCopy />}
                              size='small'
                              onClick={() => handleCopyKey(token.key)}
                            >
                              {t('复制密钥')}
                            </Button>
                          ) : (
                            <Button
                              icon={<IconPlus />}
                              size='small'
                              loading={isCreating}
                              onClick={() => handleCreateToken(groupName)}
                            >
                              {t('创建密钥')}
                            </Button>
                          )}
                        </Space>
                      </div>
                      {hasKey && (
                        <div
                          style={{
                            marginTop: 8,
                            padding: '6px 10px',
                            background: 'var(--semi-color-fill-0)',
                            borderRadius: 4,
                            fontFamily: 'monospace',
                            fontSize: 12,
                            wordBreak: 'break-all',
                            cursor: 'pointer',
                          }}
                          onClick={() => handleCopyKey(token.key)}
                        >
                          {token.key}
                        </div>
                      )}
                      {groupInfo.desc && (
                        <Text
                          type='tertiary'
                          style={{ fontSize: 12, marginTop: 4, display: 'block' }}
                        >
                          {groupInfo.desc}
                        </Text>
                      )}
                    </Card>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </Modal>

      <UpstreamKeyTestModal
        visible={keyTestVisible}
        onCancel={() => setKeyTestVisible(false)}
        target={keyTestTarget}
      />
    </div>
  );
};

export default Upstream;
