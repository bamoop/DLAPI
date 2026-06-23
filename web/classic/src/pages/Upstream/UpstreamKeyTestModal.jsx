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

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Modal,
  Button,
  Input,
  InputNumber,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  Spin,
  Banner,
  Row,
  Col,
  Card,
  Collapse,
  Divider,
  TextArea,
  List,
} from '@douyinfe/semi-ui';
import { API, showError, showSuccess } from '../../helpers';
import { getUserIdFromLocalStorage } from '../../helpers/utils';

const { Text } = Typography;

const CUSTOM_PROMPT_ID = 'custom';
const DEFAULT_MODEL = 'claude-sonnet-4-6';
const DEFAULT_GPT_MODEL = 'gpt-5.5';

const shortHash = (h) => {
  if (!h || typeof h !== 'string') return '—';
  if (h.startsWith('raw:')) return h.slice(4, 12);
  return h.slice(0, 8);
};

const percentile = (sortedArr, p) => {
  if (!sortedArr.length) return 0;
  const idx = Math.min(sortedArr.length - 1, Math.floor((sortedArr.length * p) / 100));
  return sortedArr[idx];
};

const formatJSON = (raw) => {
  if (!raw) return '';
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
};

/**
 * UpstreamKeyTestModal
 *
 * Three independent test modes inside a single dialog:
 *   1. Basic   — single request, connectivity + latency + fingerprint.
 *                Result shown as a side-by-side Request / Response pair,
 *                each individually collapsible.
 *   2. Cache   — same prompt sent twice with a cache_control breakpoint,
 *                so the operator can verify cache hit behaviour.
 *   3. RPM     — concurrent burst, SSE-streamed, reports effective RPM
 *                and per-request results.
 *
 * Mode 2 and 3 are NOT bundled — each has its own "run" button. The
 * shared prompt + model fields live at the top of the dialog.
 */
const UpstreamKeyTestModal = ({ visible, onCancel, target }) => {
  const { t } = useTranslation();

  // ── Shared config ────────────────────────────────────────────────────
  const [modelName, setModelName] = useState(DEFAULT_MODEL);
  const [gptModelName, setGptModelName] = useState(DEFAULT_GPT_MODEL);
  const [promptText, setPromptText] = useState('hello');

  // Ad-hoc target: when the dialog is opened without a (site_id, group_name)
  // pair, the operator can paste an upstream base URL + key here. We keep
  // this state separate from the `target` prop because (a) the prop is
  // read-only and (b) editing only matters for ad-hoc mode.
  const isAdhoc = !target || target.adhoc || !target.site_id;
  const [adhocBaseUrl, setAdhocBaseUrl] = useState('');
  const [adhocKey, setAdhocKey] = useState('');

  // When entering ad-hoc mode, seed from target.base_url if the caller
  // supplied one (some launch points pass a known base URL but no group).
  useEffect(() => {
    if (visible && isAdhoc) {
      setAdhocBaseUrl(target?.base_url || '');
      setAdhocKey(target?.key || '');
    }
  }, [visible, isAdhoc, target?.base_url, target?.key]);

  // ── Basic mode state ─────────────────────────────────────────────────
  const [basicLoading, setBasicLoading] = useState(false);
  const [gptLoading, setGptLoading] = useState(false);
  const [basicResult, setBasicResult] = useState(null);
  const [basicError, setBasicError] = useState('');
  // Independent collapse controls for request / response panels
  const [showBasicRequest, setShowBasicRequest] = useState(true);
  const [showBasicResponse, setShowBasicResponse] = useState(true);

  // ── Cache hit-rate test state ─────────────────────────────────────────
  // Methodology mirrors the backend handler: 1 warm-up + (N-1) serial probes,
  // measure how often cache_read_input_tokens > 0 to derive a hit rate.
  const [presets, setPresets] = useState([]);
  const [cachePresetId, setCachePresetId] = useState('sonnet');
  const [cachePromptText, setCachePromptText] = useState('');
  const [breakpoints, setBreakpoints] = useState([]);
  const [cacheIterations, setCacheIterations] = useState(10);
  const [cacheRunning, setCacheRunning] = useState(false);
  const [cacheResults, setCacheResults] = useState([]); // per-iteration results
  const [cacheSummary, setCacheSummary] = useState(null);
  const [cacheError, setCacheError] = useState('');
  // Request body shared across all iterations of the most recent run.
  // Captured from the SSE "start" event so the per-iteration UI can show
  // request and response side-by-side.
  const [cacheRequestBody, setCacheRequestBody] = useState('');
  // Which iteration sequences are currently expanded.
  const [expandedIters, setExpandedIters] = useState(() => new Set());
  // Tracks whether the user has manually edited cachePromptText (so we
  // don't clobber edits when the preset metadata reloads).
  const [cachePromptDirty, setCachePromptDirty] = useState(false);
  const cacheAbortRef = useRef(null);

  // ── Claude deep-detect state ─────────────────────────────────────────
  // Anthropic-authenticity + channel discrimination via 7 protocol probes.
  // Server streams per-probe results; UI shows score, channel verdict, and
  // a per-probe table with raw request/response and an evidence ledger so the
  // operator can sanity-check every claim.
  const [detectRunning, setDetectRunning] = useState(false);
  const [detectProbes, setDetectProbes] = useState([]); // per-probe results
  const [detectSummary, setDetectSummary] = useState(null);
  const [detectError, setDetectError] = useState('');
  const [detectExpanded, setDetectExpanded] = useState(() => new Set());
  const detectAbortRef = useRef(null);

  // ── RPM mode state ───────────────────────────────────────────────────
  const [concurrency, setConcurrency] = useState(5);
  const [rpmRunning, setRpmRunning] = useState(false);
  const [rpmRequests, setRpmRequests] = useState([]);
  const [rpmSummary, setRpmSummary] = useState(null);
  const [rpmError, setRpmError] = useState('');
  const [selectedRpmReq, setSelectedRpmReq] = useState(null);
  const rpmAbortRef = useRef(null);

  // ── Lifecycle ────────────────────────────────────────────────────────
  useEffect(() => {
    if (!visible) {
      setBasicResult(null);
      setBasicError('');
      setGptLoading(false);
      setCacheResults([]);
      setCacheSummary(null);
      setCacheError('');
      setCacheRequestBody('');
      setExpandedIters(new Set());
      setRpmRequests([]);
      setRpmSummary(null);
      setRpmError('');
      setSelectedRpmReq(null);
      setDetectProbes([]);
      setDetectSummary(null);
      setDetectError('');
      setDetectExpanded(new Set());
      cacheAbortRef.current?.abort();
      rpmAbortRef.current?.abort();
      detectAbortRef.current?.abort();
    }
  }, [visible]);

  // load presets when modal opens (used by cache mode)
  useEffect(() => {
    if (visible && presets.length === 0) {
      API.get('/api/upstream/key-test/presets')
        .then((res) => {
          if (res?.data?.success) setPresets(res.data.data || []);
        })
        .catch(() => {});
    }
  }, [visible, presets.length]);

  // Fetch preset text when the user picks one. We always overwrite — picking
  // a different preset is an explicit "reset to this template" action — and
  // also clear the dirty flag + reset breakpoints to the new hints.
  useEffect(() => {
    if (!visible) return;
    if (cachePresetId === CUSTOM_PROMPT_ID) return;
    API.get(`/api/upstream/key-test/presets/${cachePresetId}`)
      .then((res) => {
        if (res?.data?.success) {
          const text = res.data.data?.text || '';
          setCachePromptText(text);
          setCachePromptDirty(false);
          const hints = res.data.data?.breakpoint_hints || [];
          // Pick the last hint (cache_control at the end of the prompt is
          // what we want for hit-rate testing — the entire prompt cached).
          if (hints.length > 0) {
            setBreakpoints([{ position: hints[hints.length - 1] }]);
          } else {
            setBreakpoints([]);
          }
        }
      })
      .catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cachePresetId, visible]);

  const targetPayload = useMemo(() => {
    if (isAdhoc) {
      const url = adhocBaseUrl.trim();
      const key = adhocKey.trim();
      if (!url || !key) return null;
      return { base_url: url, key };
    }
    if (target?.site_id && target?.group_name) {
      return { site_id: target.site_id, group_name: target.group_name };
    }
    if (target?.base_url && target?.key) {
      return { base_url: target.base_url, key: target.key };
    }
    return null;
  }, [target, isAdhoc, adhocBaseUrl, adhocKey]);

  // ── Handlers ─────────────────────────────────────────────────────────

  const handleBasicRun = async () => {
    if (!targetPayload) return;
    setBasicLoading(true);
    setBasicError('');
    setBasicResult(null);
    try {
      const res = await API.post('/api/upstream/key-test/quick', {
        ...targetPayload,
        model_name: modelName,
        prompt_text: promptText,
      });
      if (res?.data?.success) {
        setBasicResult(res.data.data);
        showSuccess(t('测试成功'));
      } else {
        setBasicResult(res?.data?.data || null);
        setBasicError(res?.data?.message || t('测试失败'));
      }
    } catch (err) {
      setBasicError(err?.message || t('测试失败'));
    } finally {
      setBasicLoading(false);
    }
  };

  const handleGptRun = async () => {
    if (!targetPayload) return;
    setGptLoading(true);
    setBasicError('');
    setBasicResult(null);
    try {
      const res = await API.post('/api/upstream/key-test/gpt-quick', {
        ...targetPayload,
        model_name: gptModelName || DEFAULT_GPT_MODEL,
        prompt_text: promptText || 'hello',
      });
      if (res?.data?.success) {
        setBasicResult(res.data.data);
        showSuccess(t('GPT Key 测试成功'));
      } else {
        setBasicResult(res?.data?.data || null);
        setBasicError(res?.data?.message || t('GPT Key 测试失败'));
      }
    } catch (err) {
      setBasicError(err?.message || t('GPT Key 测试失败'));
    } finally {
      setGptLoading(false);
    }
  };

  // Cache test = run the quick endpoint twice with cache_control segments.
  const handleCacheRun = async () => {
    if (!targetPayload) return;
    if (!cachePromptText) {
      showError(t('请先选择 Prompt 预设'));
      return;
    }
    const usableBreakpoints = breakpoints.filter(
      (b) => b.position > 0 && b.position < cachePromptText.length,
    );
    if (usableBreakpoints.length === 0) {
      showError(t('请至少添加一个有效的缓存断点'));
      return;
    }
    setCacheRunning(true);
    setCacheResults([]);
    setCacheSummary(null);
    setCacheError('');

    const controller = new AbortController();
    cacheAbortRef.current = controller;

    try {
      const baseURL = import.meta.env.VITE_REACT_APP_SERVER_URL || '';
      const resp = await fetch(`${baseURL}/api/upstream/key-test/cache-hitrate`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'New-Api-User': String(getUserIdFromLocalStorage()),
        },
        body: JSON.stringify({
          ...targetPayload,
          prompt_preset: cachePresetId,
          prompt_text: cachePromptText,
          breakpoints: usableBreakpoints,
          iterations: cacheIterations,
          model_name: modelName,
        }),
        credentials: 'include',
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        setCacheError(await resp.text() || `HTTP ${resp.status}`);
        return;
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const raw = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          let type = 'message';
          let dataLine = '';
          for (const line of raw.split('\n')) {
            if (line.startsWith('event:')) type = line.slice(6).trim();
            else if (line.startsWith('data:')) dataLine = line.slice(5).trim();
          }
          if (!dataLine) continue;
          let parsed;
          try {
            parsed = JSON.parse(dataLine);
          } catch {
            continue;
          }
          if (type === 'start') {
            const payload = parsed?.payload ?? parsed;
            if (payload?.request_body) setCacheRequestBody(payload.request_body);
          } else if (type === 'iteration') {
            setCacheResults((p) => [...p, parsed?.payload ?? parsed]);
          } else if (type === 'summary') {
            const payload = parsed?.payload ?? parsed;
            setCacheSummary(payload);
            if (payload?.aborted) {
              showError(payload?.explanation || payload?.reason || t('测试失败'));
            } else if (payload?.hit_rate_pct >= 100) {
              showSuccess(t('缓存命中率 100%'));
            }
          }
        }
      }
    } catch (err) {
      if (err?.name !== 'AbortError') {
        setCacheError(err?.message || String(err));
      }
    } finally {
      setCacheRunning(false);
    }
  };

  const handleRpmRun = async () => {
    if (!targetPayload) return;
    setRpmRunning(true);
    setRpmRequests([]);
    setRpmSummary(null);
    setRpmError('');
    setSelectedRpmReq(null);

    const controller = new AbortController();
    rpmAbortRef.current = controller;
    try {
      const baseURL = import.meta.env.VITE_REACT_APP_SERVER_URL || '';
      const resp = await fetch(`${baseURL}/api/upstream/key-test/advanced`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'New-Api-User': String(getUserIdFromLocalStorage()),
        },
        body: JSON.stringify({
          ...targetPayload,
          prompt_text: promptText,
          breakpoints: [],
          concurrency,
          model_name: modelName,
        }),
        credentials: 'include',
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        setRpmError(await resp.text() || `HTTP ${resp.status}`);
        return;
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const raw = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          let type = 'message';
          let dataLine = '';
          for (const line of raw.split('\n')) {
            if (line.startsWith('event:')) type = line.slice(6).trim();
            else if (line.startsWith('data:')) dataLine = line.slice(5).trim();
          }
          if (!dataLine) continue;
          let parsed;
          try {
            parsed = JSON.parse(dataLine);
          } catch {
            continue;
          }
          if (type === 'request') setRpmRequests((p) => [...p, parsed?.payload ?? parsed]);
          else if (type === 'summary') setRpmSummary(parsed?.payload ?? parsed);
          else if (type === 'error')
            setRpmError(parsed?.payload?.message || JSON.stringify(parsed));
        }
      }
    } catch (err) {
      if (err?.name !== 'AbortError') setRpmError(err?.message || String(err));
    } finally {
      setRpmRunning(false);
    }
  };

  // Claude deep detection — POST SSE handler. Streams P1..P7 probe rows then
  // a final summary row. Mirrors the cache SSE pattern; we deliberately keep
  // the parser identical so any future change to the cache stream parser
  // can be lifted into a shared util.
  const handleDetectRun = async () => {
    if (!targetPayload) return;
    setDetectRunning(true);
    setDetectProbes([]);
    setDetectSummary(null);
    setDetectError('');
    setDetectExpanded(new Set());

    const controller = new AbortController();
    detectAbortRef.current = controller;
    try {
      const baseURL = import.meta.env.VITE_REACT_APP_SERVER_URL || '';
      const resp = await fetch(`${baseURL}/api/upstream/key-test/claude-detect`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'New-Api-User': String(getUserIdFromLocalStorage()),
        },
        body: JSON.stringify({
          ...targetPayload,
          model_name: modelName,
        }),
        credentials: 'include',
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        setDetectError(await resp.text() || `HTTP ${resp.status}`);
        return;
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      let sawSummary = false;
      let probeCount = 0;
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const raw = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          let type = 'message';
          let dataLine = '';
          for (const line of raw.split('\n')) {
            if (line.startsWith('event:')) type = line.slice(6).trim();
            else if (line.startsWith('data:')) dataLine = line.slice(5).trim();
          }
          if (!dataLine) continue;
          let parsed;
          try {
            parsed = JSON.parse(dataLine);
          } catch {
            continue;
          }
          if (type === 'probe') {
            probeCount += 1;
            setDetectProbes((p) => [...p, parsed?.payload ?? parsed]);
          } else if (type === 'summary') {
            sawSummary = true;
            setDetectSummary(parsed?.payload ?? parsed);
          } else if (type === 'error') {
            setDetectError((parsed?.payload?.message) || JSON.stringify(parsed));
          }
        }
      }
      // Stream closed without a summary — the backend died mid-probe. Surface
      // it so the user isn't left staring at a stuck progress card.
      if (!sawSummary) {
        setDetectError(
          t('检测流在收到 summary 之前中断（已收 {{n}} 个探针）。请查看服务端日志，常见原因是某个探针 panic 了。', {
            n: probeCount,
          }),
        );
      }
    } catch (err) {
      if (err?.name !== 'AbortError') setDetectError(err?.message || String(err));
    } finally {
      setDetectRunning(false);
    }
  };

  const rpmMetrics = useMemo(() => {
    const success = rpmRequests.filter((r) => r.success).length;
    const failed = rpmRequests.length - success;
    const latencies = rpmRequests.filter((r) => r.success).map((r) => r.latency_ms).sort((a, b) => a - b);
    const avg = latencies.length ? Math.round(latencies.reduce((a, b) => a + b, 0) / latencies.length) : 0;
    return {
      total: rpmRequests.length,
      success,
      failed,
      avg,
      max: latencies.length ? latencies[latencies.length - 1] : 0,
      p50: percentile(latencies, 50),
      p95: percentile(latencies, 95),
    };
  }, [rpmRequests]);

  // ── Renderers ────────────────────────────────────────────────────────

  /**
   * Render the basic request/response as TWO side-by-side panels. Each
   * panel has its own collapse toggle so the user can hide one and let
   * the other take the full width.
   */
  const renderBasicReqResp = () => {
    if (!basicResult) return null;
    const { request_body, response_body } = basicResult;
    const reqSpan = showBasicRequest ? (showBasicResponse ? 12 : 24) : 0;
    const respSpan = showBasicResponse ? (showBasicRequest ? 12 : 24) : 0;
    return (
      <div className='flex flex-col gap-2'>
        <Space>
          <Text strong>{t('请求 / 响应')}</Text>
          <Button
            size='small'
            theme='borderless'
            onClick={() => setShowBasicRequest((v) => !v)}
          >
            {showBasicRequest ? t('隐藏请求') : t('显示请求')}
          </Button>
          <Button
            size='small'
            theme='borderless'
            onClick={() => setShowBasicResponse((v) => !v)}
          >
            {showBasicResponse ? t('隐藏响应') : t('显示响应')}
          </Button>
        </Space>
        <Row gutter={8}>
          {showBasicRequest && (
            <Col span={reqSpan}>
              <Card
                title={t('请求体')}
                headerStyle={{ padding: '8px 12px' }}
                bodyStyle={{ padding: 0 }}
              >
                <pre
                  style={{
                    margin: 0,
                    padding: 10,
                    fontSize: 12,
                    maxHeight: 320,
                    overflow: 'auto',
                    background: 'var(--semi-color-fill-0)',
                    borderBottomLeftRadius: 6,
                    borderBottomRightRadius: 6,
                  }}
                >
                  {formatJSON(request_body)}
                </pre>
              </Card>
            </Col>
          )}
          {showBasicResponse && (
            <Col span={respSpan}>
              <Card
                title={t('响应体')}
                headerStyle={{ padding: '8px 12px' }}
                bodyStyle={{ padding: 0 }}
              >
                <pre
                  style={{
                    margin: 0,
                    padding: 10,
                    fontSize: 12,
                    maxHeight: 320,
                    overflow: 'auto',
                    background: 'var(--semi-color-fill-0)',
                    borderBottomLeftRadius: 6,
                    borderBottomRightRadius: 6,
                  }}
                >
                  {formatJSON(response_body) || '(empty)'}
                </pre>
              </Card>
            </Col>
          )}
        </Row>
      </div>
    );
  };

  const renderBasicResultHeader = () => {
    if (basicLoading) {
      return (
        <div style={{ textAlign: 'center', padding: 30 }}>
          <Spin /> {t('测试中...')}
        </div>
      );
    }
    if (basicError && !basicResult) {
      return <Banner type='danger' description={basicError} closeIcon={null} />;
    }
    if (!basicResult) return null;
    const { status_code, latency_ms, fingerprint, same_source, usage } = basicResult;
    const ok = status_code >= 200 && status_code < 300;
    return (
      <div className='flex flex-col gap-3'>
        {basicError && (
          <Banner type='warning' description={basicError} closeIcon={null} />
        )}
        <Row gutter={[8, 8]}>
          <Col span={6}>
            <Card bodyStyle={{ padding: 10 }}>
              <Text type='tertiary' size='small'>
                {t('连通性')}
              </Text>
              <div style={{ fontWeight: 600, marginTop: 4 }}>
                <Tag color={ok ? 'green' : 'red'} type='solid'>
                  {ok ? t('成功') : `HTTP ${status_code}`}
                </Tag>
              </div>
            </Card>
          </Col>
          <Col span={6}>
            <Card bodyStyle={{ padding: 10 }}>
              <Text type='tertiary' size='small'>
                {t('延迟')}
              </Text>
              <div style={{ fontWeight: 600, fontSize: 16 }}>{latency_ms}ms</div>
            </Card>
          </Col>
          <Col span={6}>
            <Card bodyStyle={{ padding: 10 }}>
              <Text type='tertiary' size='small'>
                {t('指纹')}
              </Text>
              <Tooltip content={fingerprint?.composite || ''}>
                <Tag color='blue'>{shortHash(fingerprint?.composite)}</Tag>
              </Tooltip>
            </Card>
          </Col>
          <Col span={6}>
            <Card bodyStyle={{ padding: 10 }}>
              <Text type='tertiary' size='small'>
                {t('Tokens')}
              </Text>
              <div style={{ fontSize: 12, marginTop: 2 }}>
                <Text>{usage?.input_tokens ?? 0} / {usage?.output_tokens ?? 0}</Text>
              </div>
            </Card>
          </Col>
        </Row>
        {same_source?.channels?.length > 0 && (
          <Banner
            type='warning'
            closeIcon={null}
            description={
              <Text>
                {t('同源渠道')}: #{same_source.channels.join(', #')}
              </Text>
            }
          />
        )}
      </div>
    );
  };

  const renderCacheSection = () => {
    const probeResults = cacheResults.filter((r) => r.role === 'probe');
    const liveHits = probeResults.filter((r) => r.cache_status === 'hit').length;
    const liveTotal = probeResults.length;
    const liveRate = liveTotal > 0 ? (liveHits / liveTotal) * 100 : 0;
    return (
      <div className='flex flex-col gap-3'>
        <Banner
          type='info'
          closeIcon={null}
          description={t(
            '方法：先发送 1 次 warmup 建立缓存，等响应回来后再串行发送 N-1 次完全相同的请求，统计 cache_read 命中比例。命中率 100% = 直连；部分命中 = 上游可能在 key 池轮询；0% = 缓存未透传 / 过期 / prompt 太短。',
          )}
        />

        <Row gutter={12}>
          <Col span={12}>
            <Text strong>{t('Prompt 预设')}</Text>
            <Select
              value={cachePresetId}
              onChange={(v) => {
                setCachePresetId(v ?? 'sonnet');
                setBreakpoints([]);
              }}
              style={{ width: '100%', marginTop: 4 }}
              optionList={presets.map((p) => ({
                value: p.id,
                label: p.label,
              }))}
            />
            <Text type='tertiary' size='small'>
              {presets.find((p) => p.id === cachePresetId)?.description || ''}
            </Text>
          </Col>
          <Col span={6}>
            <Text strong>{t('总次数')}</Text>
            <Tooltip content={t('总请求数（含 1 次 warmup）。命中率 = (N-1 次中命中数) / (N-1)。')}>
              <InputNumber
                value={cacheIterations}
                onChange={(v) => setCacheIterations(Math.max(2, Math.min(50, Number(v) || 10)))}
                min={2}
                max={50}
                style={{ width: '100%', marginTop: 4 }}
              />
            </Tooltip>
            <Text type='tertiary' size='small'>
              {t('1 warmup + {{n}} 命中探测', { n: Math.max(0, cacheIterations - 1) })}
            </Text>
          </Col>
          <Col span={6}>
            <Text strong>{t('Prompt 长度')}</Text>
            <div style={{ marginTop: 4, fontWeight: 600 }}>
              {cachePromptText.length} {t('字符')}
              {cachePromptDirty && (
                <Tag size='small' color='orange' style={{ marginLeft: 6 }}>
                  {t('已修改')}
                </Tag>
              )}
            </div>
          </Col>
        </Row>

        <div>
          <Space style={{ marginBottom: 4 }}>
            <Text strong>{t('Prompt 内容')}</Text>
            <Tooltip content={t('在此修改测试用的 prompt。修改后所有迭代都用同一份新内容（必须 byte-identical 才能命中缓存）。')}>
              <Tag size='small' color='blue'>?</Tag>
            </Tooltip>
            {cachePromptDirty && (
              <Button
                size='small'
                theme='borderless'
                onClick={() => {
                  setCachePromptDirty(false);
                  // re-fetch preset text by toggling
                  const cur = cachePresetId;
                  setCachePresetId('');
                  setTimeout(() => setCachePresetId(cur), 0);
                }}
              >
                {t('恢复预设')}
              </Button>
            )}
          </Space>
          <TextArea
            value={cachePromptText}
            onChange={(v) => {
              setCachePromptText(v);
              setCachePromptDirty(true);
            }}
            autosize={{ minRows: 4, maxRows: 12 }}
            placeholder={t('选择一个预设作为起点，或在此处粘贴自定义的长 prompt')}
          />
        </div>

        <div>
          <Space>
            <Text strong>{t('缓存断点')}</Text>
            <Tooltip
              content={t(
                'cache_control 边界。建议放在 prompt 末尾让整个 prompt 都进入缓存。Anthropic 不同模型有不同最低 tokens 阈值（Sonnet 4.x: 1024 / Haiku 3.5: 2048 / Opus 4.5+ 和 Haiku 4.5: 4096）。',
              )}
            >
              <Tag size='small' color='blue'>
                ?
              </Tag>
            </Tooltip>
          </Space>
          <div className='flex flex-col gap-1' style={{ marginTop: 4 }}>
            {breakpoints.map((b, i) => (
              <Space key={i}>
                <Text size='small'>{t('位置')}:</Text>
                <InputNumber
                  value={b.position}
                  onChange={(v) => {
                    const next = [...breakpoints];
                    next[i] = { position: Number(v) || 0 };
                    setBreakpoints(next);
                  }}
                  min={1}
                  max={Math.max(1, cachePromptText.length - 1)}
                  step={50}
                  style={{ width: 130 }}
                />
                <Button
                  size='small'
                  type='danger'
                  theme='borderless'
                  onClick={() => setBreakpoints(breakpoints.filter((_, j) => j !== i))}
                >
                  {t('移除')}
                </Button>
              </Space>
            ))}
            <Button
              size='small'
              theme='borderless'
              onClick={() =>
                setBreakpoints([
                  ...breakpoints,
                  { position: Math.max(1, cachePromptText.length - 1) },
                ])
              }
              disabled={!cachePromptText}
            >
              + {t('添加断点')}
            </Button>
          </div>
        </div>

        <Button
          type='primary'
          theme='solid'
          loading={cacheRunning}
          onClick={handleCacheRun}
        >
          {t('运行缓存命中率测试')}
        </Button>

        {cacheError && <Banner type='danger' description={cacheError} closeIcon={null} />}

        {(cacheRunning || cacheResults.length > 0) && (
          <>
            <Row gutter={[8, 8]}>
              <Col span={8}>
                <Card bodyStyle={{ padding: 10 }}>
                  <Text type='tertiary' size='small'>
                    {t('命中率')}
                  </Text>
                  <div
                    style={{
                      fontWeight: 700,
                      fontSize: 22,
                      color:
                        cacheSummary?.hit_rate_pct === 100
                          ? 'var(--semi-color-success)'
                          : cacheSummary?.hit_rate_pct === 0
                            ? 'var(--semi-color-danger)'
                            : 'var(--semi-color-warning)',
                    }}
                  >
                    {(cacheSummary?.hit_rate_pct ?? liveRate).toFixed(1)}%
                  </div>
                </Card>
              </Col>
              <Col span={8}>
                <Card bodyStyle={{ padding: 10 }}>
                  <Text type='tertiary' size='small'>
                    {t('命中 / 探测')}
                  </Text>
                  <div style={{ fontWeight: 600, fontSize: 16 }}>
                    {cacheSummary?.cache_hits ?? liveHits} / {cacheSummary?.total_probes ?? liveTotal}
                  </div>
                </Card>
              </Col>
              <Col span={8}>
                <Card bodyStyle={{ padding: 10 }}>
                  <Text type='tertiary' size='small'>
                    {t('进度')}
                  </Text>
                  <div style={{ fontWeight: 600, fontSize: 16 }}>
                    {cacheResults.length} / {cacheIterations}
                  </div>
                </Card>
              </Col>
            </Row>

            {cacheSummary?.aborted && cacheSummary?.explanation && (
              <Banner
                type={cacheSummary.reason === 'prompt_below_cache_minimum' ? 'warning' : 'danger'}
                closeIcon={null}
                description={
                  <div className='flex flex-col gap-1'>
                    <Text>{cacheSummary.explanation}</Text>
                    {cacheSummary.reason === 'upstream_strips_cache_control' && (
                      <Text type='tertiary' size='small'>
                        {t('提示：建议把这个上游标记为不支持缓存，或者更换为直连源。')}
                      </Text>
                    )}
                    {cacheSummary.reason === 'prompt_below_cache_minimum' && (
                      <Text type='tertiary' size='small'>
                        {t('提示：当前模型阈值 = {{th}} tokens，warm-up 实际 = {{it}} tokens。请换更大的 Prompt 预设，或者把缓存断点放到 prompt 末尾。', {
                          th: cacheSummary.model_threshold,
                          it: cacheSummary.input_tokens,
                        })}
                      </Text>
                    )}
                  </div>
                }
              />
            )}
            {!cacheSummary?.aborted && cacheSummary?.interpretation && (
              <Banner
                type={
                  cacheSummary.hit_rate_pct >= 100
                    ? 'success'
                    : cacheSummary.hit_rate_pct === 0
                      ? 'danger'
                      : 'warning'
                }
                closeIcon={null}
                description={cacheSummary.interpretation}
              />
            )}

            <div>
              <Text strong>{t('逐次结果')}</Text>
              <div
                style={{
                  maxHeight: 260,
                  overflowY: 'auto',
                  border: '1px solid var(--semi-color-border)',
                  borderRadius: 6,
                  marginTop: 4,
                }}
              >
                {cacheResults.length === 0 && cacheRunning && (
                  <div style={{ padding: 16, textAlign: 'center' }}><Spin /></div>
                )}
                {cacheResults.map((r) => {
                  const key = `${r.role}-${r.sequence}`;
                  const expanded = expandedIters.has(key);
                  const statusColor =
                    r.cache_status === 'hit'
                      ? 'green'
                      : r.cache_status === 'created'
                        ? 'violet'
                        : r.cache_status === 'below_minimum'
                          ? 'orange'
                          : r.cache_status === 'not_propagated'
                            ? 'red'
                            : r.cache_status === 'no_usage'
                              ? 'grey'
                              : r.cache_status === 'error'
                                ? 'red'
                                : 'grey';
                  const statusLabel =
                    r.cache_status === 'hit'
                      ? t('命中')
                      : r.cache_status === 'created'
                        ? t('写入')
                        : r.cache_status === 'below_minimum'
                          ? t('Prompt过短')
                          : r.cache_status === 'not_propagated'
                            ? t('上游未透传')
                            : r.cache_status === 'no_usage'
                              ? t('无usage')
                              : r.cache_status === 'error'
                                ? t('错误')
                                : '-';
                  const toggle = () => {
                    setExpandedIters((prev) => {
                      const next = new Set(prev);
                      if (next.has(key)) next.delete(key);
                      else next.add(key);
                      return next;
                    });
                  };
                  return (
                    <div
                      key={key}
                      style={{
                        borderBottom: '1px solid var(--semi-color-border)',
                      }}
                    >
                      <div
                        onClick={toggle}
                        style={{
                          padding: '6px 10px',
                          cursor: 'pointer',
                          userSelect: 'none',
                          display: 'flex',
                          alignItems: 'center',
                          gap: 6,
                          flexWrap: 'wrap',
                        }}
                      >
                        <Text size='small' style={{ width: 14 }}>
                          {expanded ? '▾' : '▸'}
                        </Text>
                        <Tag size='small'>
                          {r.role === 'warmup' ? 'warm' : `#${r.sequence}`}
                        </Tag>
                        <Tag size='small' color={statusColor} type='solid'>
                          {statusLabel}
                        </Tag>
                        <Text>{r.latency_ms}ms</Text>
                        {r.cache_read_tokens > 0 && (
                          <Tag size='small' color='blue'>
                            read: {r.cache_read_tokens}
                          </Tag>
                        )}
                        {r.cache_creation_tokens > 0 && (
                          <Tag size='small' color='violet'>
                            create: {r.cache_creation_tokens}
                          </Tag>
                        )}
                        <Text type='tertiary' size='small'>
                          input: {r.input_tokens} · output: {r.output_tokens}
                        </Text>
                        {r.error_message && (
                          <Text type='danger' ellipsis={{ rows: 1 }} style={{ maxWidth: 280 }}>
                            {r.error_message}
                          </Text>
                        )}
                      </div>
                      {expanded && (
                        <Row gutter={6} style={{ padding: '0 10px 10px' }}>
                          <Col span={12}>
                            <Card
                              title={t('请求体')}
                              headerStyle={{ padding: '6px 10px' }}
                              bodyStyle={{ padding: 0 }}
                            >
                              <pre
                                style={{
                                  margin: 0,
                                  padding: 8,
                                  fontSize: 11,
                                  maxHeight: 260,
                                  overflow: 'auto',
                                  background: 'var(--semi-color-fill-0)',
                                }}
                              >
                                {formatJSON(cacheRequestBody) || '(unavailable)'}
                              </pre>
                            </Card>
                          </Col>
                          <Col span={12}>
                            <Card
                              title={t('响应体')}
                              headerStyle={{ padding: '6px 10px' }}
                              bodyStyle={{ padding: 0 }}
                            >
                              <pre
                                style={{
                                  margin: 0,
                                  padding: 8,
                                  fontSize: 11,
                                  maxHeight: 260,
                                  overflow: 'auto',
                                  background: 'var(--semi-color-fill-0)',
                                }}
                              >
                                {formatJSON(r.response_body) || r.error_message || '(empty)'}
                              </pre>
                            </Card>
                          </Col>
                        </Row>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          </>
        )}
      </div>
    );
  };

  /**
   * Claude 检测引擎 v2 报告（对标 ztest.ai）：~18 个行为/能力级探针，归到
   * 6 个维度组，给出 0-100 综合分 + 风险档位（推荐/良好/一般/不建议/不可用）。
   * 直击 TG 套壳站的掺水/降级：S1 量 token 注入、D11 让模型自报身份、D10/D16
   * 能力题、D13 多模态识图。每个探针可展开看原始 details，便于人工复核。
   */
  const renderDetectSection = () => {
    const statusColor = (s) =>
      s === 'success' ? 'green' : s === 'partial' ? 'orange' : s === 'skipped' ? 'grey' : 'red';
    const statusLabel = (s) =>
      s === 'success' ? t('通过') : s === 'partial' ? t('部分') : s === 'skipped' ? t('跳过') : t('失败');
    const statusGlyph = (s) =>
      s === 'success' ? '✓' : s === 'partial' ? '?' : s === 'skipped' ? '–' : '✗';
    const toggleProbe = (code) =>
      setDetectExpanded((prev) => {
        const next = new Set(prev);
        if (next.has(code)) next.delete(code);
        else next.add(code);
        return next;
      });
    // 综合分 → 主题色（与后端档位对齐：80推荐绿 / 60良好黄 / 40一般橙 / <40 红）。
    const scoreColor = (sc) =>
      sc >= 80
        ? 'var(--semi-color-success)'
        : sc >= 60
          ? 'var(--semi-color-warning)'
          : sc >= 40
            ? 'hsl(28 90% 55%)'
            : 'var(--semi-color-danger)';
    const groupColor = (pct) =>
      pct >= 90
        ? 'var(--semi-color-success)'
        : pct >= 60
          ? 'var(--semi-color-warning)'
          : 'var(--semi-color-danger)';
    const alertType = (sev) => (sev === 'high' ? 'danger' : sev === 'medium' ? 'warning' : 'info');
    const sum = detectSummary || {};
    const composite = sum.composite_score;
    const verdict = sum.verdict || {};
    const groups = sum.dimension_groups || [];
    const alerts = sum.risk_alerts || [];
    const positive = sum.proxy_suspicion?.signals?.positive || [];
    const strong = sum.proxy_suspicion?.signals?.strong || [];
    const medium = sum.proxy_suspicion?.signals?.medium || [];

    return (
      <div className='flex flex-col gap-3'>
        <Banner
          type='info'
          closeIcon={null}
          description={t(
            '检测引擎 v2（对标 ztest）：用 ~18 个行为/能力级探针验证「这个中转有没有把你付费的模型原样交付」。直击套壳掺水：量 Token 注入、让模型自报身份、做能力题、多模态识图。给出 0-100 综合分 + 风险档位，每步可展开看原始请求/响应。',
          )}
        />
        <Button
          type='primary'
          theme='solid'
          loading={detectRunning}
          onClick={handleDetectRun}
          disabled={!targetPayload}
        >
          {t('运行 Claude 深度检测')}
        </Button>
        {detectError && <Banner type='danger' description={detectError} closeIcon={null} />}

        {/* 综合分 + 档位 + 结论 */}
        {(detectRunning || detectProbes.length > 0) && (
          <Card bodyStyle={{ padding: 14 }}>
            <Row gutter={[12, 12]} align='middle'>
              <Col span={6} style={{ textAlign: 'center' }}>
                <div style={{ fontSize: 44, fontWeight: 800, lineHeight: 1.1, color: composite != null ? scoreColor(composite) : 'var(--semi-color-text-2)' }}>
                  {composite != null ? composite : (detectRunning ? '…' : '-')}
                </div>
                <Text type='tertiary' size='small'>{t('综合可信度')} / 100</Text>
              </Col>
              <Col span={18}>
                {verdict.label && (
                  <Tag color={composite >= 80 ? 'green' : composite >= 60 ? 'orange' : 'red'} type='solid' size='large' style={{ marginBottom: 6 }}>
                    {verdict.label}
                  </Tag>
                )}
                <div>
                  <Text>{verdict.headline || (detectRunning ? t('检测进行中…') : '')}</Text>
                </div>
                <Text type='tertiary' size='small'>
                  {t('进度')} {detectProbes.length}{sum.probe_total ? ` / ${sum.probe_total}` : ''}
                </Text>
              </Col>
            </Row>
            {verdict.key_findings?.length > 0 && (
              <div style={{ marginTop: 10 }} className='flex flex-col gap-1'>
                {verdict.key_findings.map((kf, i) => (
                  <Text key={i} size='small'>• {kf}</Text>
                ))}
              </div>
            )}
          </Card>
        )}

        {/* 风险告警 */}
        {alerts.map((a, i) => (
          <Banner
            key={i}
            type={alertType(a.severity)}
            closeIcon={null}
            description={`⚠ [${a.source_probe}] ${a.title}：${a.description}`}
          />
        ))}

        {/* 6 维度卡 */}
        {groups.length > 0 && (
          <Row gutter={[8, 8]}>
            {groups.map((g) => (
              <Col span={8} key={g.key}>
                <Card bodyStyle={{ padding: 10 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                    <Text strong size='small'>{g.name}</Text>
                    <Text style={{ fontWeight: 700, color: groupColor(g.score_percent) }}>{g.score_percent}%</Text>
                  </div>
                  <div style={{ marginTop: 6, display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                    {(g.probes || []).map((pr) => (
                      <Tag key={pr.code} size='small' color={statusColor(pr.status)} type='light'>
                        {pr.code} {statusGlyph(pr.status)}
                      </Tag>
                    ))}
                  </div>
                </Card>
              </Col>
            ))}
          </Row>
        )}

        {/* 信号：可疑 + 正向 */}
        {(strong.length > 0 || medium.length > 0 || positive.length > 0) && (
          <Card title={t('信号')} headerStyle={{ padding: '8px 12px' }} bodyStyle={{ padding: 10 }}>
            <div className='flex flex-col gap-1'>
              {[...strong.map((s) => ({ ...s, _t: 'strong' })), ...medium.map((s) => ({ ...s, _t: 'medium' })), ...positive.map((s) => ({ ...s, _t: 'positive' }))].map((s, i) => (
                <Space key={i} wrap size={4}>
                  <Tag size='small' color={s._t === 'strong' ? 'red' : s._t === 'medium' ? 'orange' : 'green'} type='solid'>
                    {s._t === 'positive' ? t('正向') : t('可疑')}
                  </Tag>
                  <Text size='small' strong>[{s.source_probe}] {s.title}</Text>
                  <Text size='small' type='tertiary'>{s.description}</Text>
                </Space>
              ))}
            </div>
          </Card>
        )}

        {/* 探针明细（可展开看 details） */}
        {(detectRunning || detectProbes.length > 0) && (
          <div>
            <Text strong>{t('探针明细')}</Text>
            <div
              style={{
                maxHeight: 420,
                overflowY: 'auto',
                border: '1px solid var(--semi-color-border)',
                borderRadius: 6,
                marginTop: 4,
              }}
            >
              {detectProbes.length === 0 && detectRunning && (
                <div style={{ padding: 16, textAlign: 'center' }}>
                  <Spin />
                </div>
              )}
              {detectProbes.map((p) => {
                const code = p.probe_code;
                const expanded = detectExpanded.has(code);
                const diag = p.diagnosis;
                return (
                  <div key={code} style={{ borderBottom: '1px solid var(--semi-color-border)' }}>
                    <div
                      onClick={() => toggleProbe(code)}
                      style={{
                        padding: '6px 10px',
                        cursor: 'pointer',
                        userSelect: 'none',
                        display: 'flex',
                        alignItems: 'center',
                        gap: 6,
                        flexWrap: 'wrap',
                      }}
                    >
                      <Text size='small' style={{ width: 14 }}>{expanded ? '▾' : '▸'}</Text>
                      <Tag size='small'>{code}</Tag>
                      <Text strong>{p.probe_name}</Text>
                      <Tag size='small' color='violet' type='light'>{p.dimension}</Tag>
                      <Tag size='small' color={statusColor(p.status)} type='solid'>
                        {statusLabel(p.status)}
                      </Tag>
                      <Tag size='small' color='blue'>{p.score}</Tag>
                      <Text type='tertiary' size='small'>{p.latency_ms}ms</Text>
                      {diag?.title && (
                        <Text type='warning' size='small' ellipsis={{ rows: 1 }} style={{ maxWidth: 360 }}>
                          {diag.title}
                        </Text>
                      )}
                    </div>
                    {expanded && (
                      <div style={{ padding: '0 10px 10px' }}>
                        {diag && (
                          <Card title={t('诊断')} headerStyle={{ padding: '6px 10px' }} bodyStyle={{ padding: 8 }} style={{ marginBottom: 6 }}>
                            <Text strong size='small'>{diag.title}</Text>
                            {(diag.suggestions || []).map((s, i) => (
                              <div key={i}><Text size='small' type='tertiary'>• {s}</Text></div>
                            ))}
                          </Card>
                        )}
                        <Card title={t('原始 details')} headerStyle={{ padding: '6px 10px' }} bodyStyle={{ padding: 0 }}>
                          <pre
                            style={{
                              margin: 0,
                              padding: 8,
                              fontSize: 11,
                              maxHeight: 320,
                              overflow: 'auto',
                              background: 'var(--semi-color-fill-0)',
                            }}
                          >
                            {p.details ? JSON.stringify(p.details, null, 2) : '(empty)'}
                          </pre>
                        </Card>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    );
  };

  const renderRpmSection = () => (
    <div className='flex flex-col gap-3'>
      <Row gutter={12} align='bottom'>
        <Col span={8}>
          <Text strong>{t('并发数')}</Text>
          <InputNumber
            value={concurrency}
            onChange={(v) => setConcurrency(Math.max(1, Number(v) || 1))}
            min={1}
            max={1000}
            style={{ width: '100%', marginTop: 4 }}
          />
        </Col>
        <Col span={16}>
          <Banner
            type='info'
            closeIcon={null}
            description={t('并发不会被限流。如果上游开始报错（最近 10 个请求失败率 ≥ 30%），系统会记录上面成功的最大数作为实际 RPM。')}
          />
        </Col>
      </Row>

      <Button
        type='primary'
        theme='solid'
        loading={rpmRunning}
        onClick={handleRpmRun}
      >
        {t('运行 RPM 测试')}
      </Button>

      {rpmError && <Banner type='danger' description={rpmError} closeIcon={null} />}

      {(rpmRunning || rpmRequests.length > 0) && (
        <>
          <Row gutter={[8, 8]}>
            <Col span={6}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>{t('已完成 / 总数')}</Text>
                <div style={{ fontWeight: 600 }}>{rpmMetrics.total} / {concurrency}</div>
              </Card>
            </Col>
            <Col span={6}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>{t('成功 / 失败')}</Text>
                <div style={{ fontWeight: 600 }}>
                  <span style={{ color: 'var(--semi-color-success)' }}>{rpmMetrics.success}</span>
                  {' / '}
                  <span style={{ color: 'var(--semi-color-danger)' }}>{rpmMetrics.failed}</span>
                </div>
              </Card>
            </Col>
            <Col span={6}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>{t('实际 RPM')}</Text>
                <div style={{ fontWeight: 600 }}>
                  {rpmSummary?.effective_rpm ?? rpmMetrics.success}
                </div>
              </Card>
            </Col>
            <Col span={6}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>{t('平均延迟')}</Text>
                <div style={{ fontWeight: 600 }}>{rpmMetrics.avg}ms</div>
              </Card>
            </Col>
            <Col span={8}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>P50</Text>
                <div style={{ fontWeight: 600 }}>{rpmMetrics.p50}ms</div>
              </Card>
            </Col>
            <Col span={8}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>P95</Text>
                <div style={{ fontWeight: 600 }}>{rpmMetrics.p95}ms</div>
              </Card>
            </Col>
            <Col span={8}>
              <Card bodyStyle={{ padding: 10 }}>
                <Text type='tertiary' size='small'>{t('最大')}</Text>
                <div style={{ fontWeight: 600 }}>{rpmMetrics.max}ms</div>
              </Card>
            </Col>
          </Row>

          {rpmSummary?.same_source?.channels?.length > 0 && (
            <Banner
              type='warning'
              closeIcon={null}
              description={`${t('同源渠道')}: #${rpmSummary.same_source.channels.join(', #')}`}
            />
          )}

          <div>
            <Text strong>{t('请求明细')}</Text>
            <div
              style={{
                maxHeight: 220,
                overflowY: 'auto',
                border: '1px solid var(--semi-color-border)',
                borderRadius: 6,
                marginTop: 4,
              }}
            >
              <List
                size='small'
                dataSource={rpmRequests}
                renderItem={(r) => (
                  <List.Item
                    style={{ cursor: 'pointer', padding: '6px 10px' }}
                    onClick={() => setSelectedRpmReq(r)}
                    main={
                      <Space>
                        <Tag size='small'>#{r.sequence}</Tag>
                        <Tag size='small' color={r.success ? 'green' : 'red'}>
                          {r.success ? r.status_code : 'ERR'}
                        </Tag>
                        <Text>{r.latency_ms}ms</Text>
                        {!r.success && r.error_message && (
                          <Text type='danger' ellipsis={{ rows: 1 }} style={{ maxWidth: 360 }}>
                            {r.error_message}
                          </Text>
                        )}
                      </Space>
                    }
                  />
                )}
                emptyContent={rpmRunning ? <Spin /> : null}
              />
            </div>
          </div>
          {selectedRpmReq && (
            <Collapse defaultActiveKey={['d']}>
              <Collapse.Panel
                header={t('请求详情') + ` #${selectedRpmReq.sequence}`}
                itemKey='d'
              >
                <pre
                  style={{
                    maxHeight: 200,
                    overflow: 'auto',
                    background: 'var(--semi-color-fill-0)',
                    padding: 10,
                    borderRadius: 4,
                    fontSize: 12,
                  }}
                >
                  {JSON.stringify(selectedRpmReq, null, 2)}
                </pre>
              </Collapse.Panel>
            </Collapse>
          )}
        </>
      )}
    </div>
  );

  // When no target is provided AT ALL and the dialog isn't visible, render
  // nothing. Otherwise (including ad-hoc) we always render so the operator
  // can paste a URL + key.
  if (!visible && !target) return null;

  const title = target?.group_name
    ? `${t('测试 Key')} — ${target.group_name}`
    : isAdhoc
      ? t('快速测试上游 Key')
      : t('测试上游 Key');

  return (
    <Modal
      title={title}
      visible={visible}
      onCancel={onCancel}
      // Responsive width: ~90% of viewport, capped at 1600px so it stays
      // sensible on ultrawide monitors and still fills mid-size screens.
      width={Math.min(Math.max(window.innerWidth * 0.9, 720), 1600)}
      style={{ maxWidth: '95vw' }}
      bodyStyle={{ maxHeight: '78vh', overflowY: 'auto' }}
      footer={
        <Space>
          <Button onClick={onCancel}>{t('关闭')}</Button>
          <Button
            theme='outline'
            loading={gptLoading}
            onClick={handleGptRun}
            disabled={!targetPayload || basicLoading || !gptModelName}
          >
            {t('GPT Key 测试')}
          </Button>
          <Button
            type='primary'
            theme='solid'
            loading={basicLoading}
            onClick={handleBasicRun}
            disabled={!promptText || !modelName || !targetPayload || gptLoading}
          >
            {t('运行测试')}
          </Button>
        </Space>
      }
    >
      <div className='flex flex-col gap-3'>
        {isAdhoc ? (
          <Row gutter={12}>
            <Col span={14}>
              <Text strong>{t('上游 Base URL')}</Text>
              <Input
                value={adhocBaseUrl}
                onChange={(v) => setAdhocBaseUrl(v)}
                placeholder='https://example.com'
                style={{ marginTop: 4 }}
              />
              <Text type='tertiary' size='small'>
                {t('完整 URL（不含 /v1/messages 路径）')}
              </Text>
            </Col>
            <Col span={10}>
              <Text strong>{t('Key')}</Text>
              <Input
                value={adhocKey}
                onChange={(v) => setAdhocKey(v)}
                placeholder='sk-...'
                style={{ marginTop: 4 }}
                type='password'
              />
              <Text type='tertiary' size='small'>
                {t('完整 key，含 sk- 前缀')}
              </Text>
            </Col>
          </Row>
        ) : target?.base_url ? (
          <Text type='tertiary' size='small'>
            URL: <Text code>{target.base_url}</Text>
          </Text>
        ) : null}

        <Row gutter={12}>
          <Col span={16}>
            <Text strong>{t('Prompt 内容')}</Text>
            <TextArea
              value={promptText}
              onChange={(v) => setPromptText(v)}
              rows={3}
              autosize={{ minRows: 3, maxRows: 8 }}
              placeholder='hello'
              style={{ marginTop: 4 }}
            />
            <Text type='tertiary' size='small'>
              {t('长度')}: {promptText.length}
            </Text>
          </Col>
          <Col span={8}>
            <Text strong>{t('模型')}</Text>
            <Input
              value={modelName}
              onChange={(v) => setModelName(v)}
              style={{ marginTop: 4 }}
            />
            <Text type='tertiary' size='small'>
              {t('默认 claude-sonnet-4-6')}
            </Text>
          </Col>
        </Row>

        <Row gutter={12}>
          <Col span={8} offset={16}>
            <Text strong>{t('GPT 测试模型')}</Text>
            <Input
              value={gptModelName}
              onChange={(v) => setGptModelName(v)}
              placeholder={DEFAULT_GPT_MODEL}
              style={{ marginTop: 4 }}
            />
            <Text type='tertiary' size='small'>
              {t('GPT Key 测试默认使用 gpt-5.5')}
            </Text>
          </Col>
        </Row>

        <Divider margin='6px' />

        {renderBasicResultHeader()}
        {renderBasicReqResp()}

        <Divider margin='6px' />

        <Collapse>
          <Collapse.Panel
            header={
              <Space>
                <Text strong>{t('缓存命中率测试')}</Text>
                <Text type='tertiary' size='small'>
                  {t('串行发 N 次相同请求，测量上游缓存命中率')}
                </Text>
              </Space>
            }
            itemKey='cache'
          >
            {renderCacheSection()}
          </Collapse.Panel>
          <Collapse.Panel
            header={
              <Space>
                <Text strong>{t('Claude 深度检测')}</Text>
                <Text type='tertiary' size='small'>
                  {t('协议级探针：后端真实性 + 链路诚信双轴判定')}
                </Text>
              </Space>
            }
            itemKey='detect'
          >
            {renderDetectSection()}
          </Collapse.Panel>
          <Collapse.Panel
            header={
              <Space>
                <Text strong>{t('RPM 测试')}</Text>
                <Text type='tertiary' size='small'>
                  {t('并发探测 key 的实际 RPM 上限')}
                </Text>
              </Space>
            }
            itemKey='rpm'
          >
            {renderRpmSection()}
          </Collapse.Panel>
        </Collapse>
      </div>
    </Modal>
  );
};

export default UpstreamKeyTestModal;
