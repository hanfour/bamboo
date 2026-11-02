// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import {
  approveIngressAction,
  createIngressAction,
  deleteIngressAction,
  issueIngressTokenAction,
  setIngressEnabledAction,
  type IngressActionResult,
} from '@/lib/ingressActions';
import {
  formatIngressExpiry,
  ingressReadiness,
  ingressState,
  validateIngressLabel,
  validateIngressPort,
  type IngressList,
  type IngressReadiness,
  type IngressService,
  type IngressState,
} from '@/lib/ingress';
import type { Peer } from '@/lib/types';

// IngressView is the public services admin surface (spec §7): list,
// create on a peer, approve (admin), enable/disable, delete, and
// issue/rotate the ingress service token. Mutations are server actions
// that revalidate the page, so the list re-renders from the controller.
export function IngressView({ list, peers, isAdmin }: { list: IngressList; peers: Peer[]; isAdmin: boolean }) {
  const t = useTranslations('ingress');
  const [error, setError] = useState<string | null>(null);
  const approved = peers.filter((p) => p.approvalStatus === 'approved');
  return (
    <div className="space-y-6">
      {error && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {t('errorPrefix')} {error}
        </div>
      )}
      <CreateForm peers={approved} onError={setError} />
      <ServiceTable services={list.services} readiness={ingressReadiness(peers, list.token)} isAdmin={isAdmin} onError={setError} />
      {isAdmin && <TokenPanel list={list} peers={approved.filter((p) => p.tags.includes('ingress'))} onError={setError} />}
    </div>
  );
}

const stateTone: Record<IngressState, string> = {
  live: 'border-bamboo-300/60 text-bamboo-200',
  approved: 'border-bamboo-200/30 text-bamboo-200/70',
  pending: 'border-amber-500/60 text-amber-400',
  peerPending: 'border-amber-500/60 text-amber-400',
};

type RowProps = { readiness: IngressReadiness; isAdmin: boolean; onError: (e: string | null) => void };

function ServiceTable({ services, ...row }: { services: IngressService[] } & RowProps) {
  const t = useTranslations('ingress');
  if (services.length === 0) {
    return (
      <p className="rounded-2xl border border-dashed border-[#777]/30 p-10 text-center text-[13px] text-bamboo-200/60">
        {t('empty')}
      </p>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-[#777]/20 text-[13px]">
        <thead className="bg-ink-900/40">
          <tr>
            {(['label', 'url', 'peer', 'port', 'state', 'actions'] as const).map((c) => (
              <th key={c} className="px-3 py-2 text-left text-xs font-medium text-bamboo-200/60">{t(`columns.${c}`)}</th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-[#777]/20">
          {services.map((s) => (
            <ServiceRow key={s.id} service={s} {...row} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ServiceRow({ service: s, readiness, isAdmin, onError }: { service: IngressService } & RowProps) {
  const t = useTranslations('ingress');
  const [pending, startTransition] = useTransition();
  const [confirming, setConfirming] = useState(false);
  const state = ingressState(s);
  const act = (fn: () => Promise<IngressActionResult>) =>
    startTransition(async () => {
      const res = await fn();
      onError(res.ok ? null : res.error);
    });
  return (
    <tr>
      <td className="px-3 py-2 font-mono text-bamboo-50">{s.label}</td>
      <td className="px-3 py-2 font-mono text-xs text-bamboo-200/80">{s.url || '—'}</td>
      <td className="px-3 py-2">{s.peerHostname} <span className="font-mono text-xs text-bamboo-200/50">{s.peerIp}</span></td>
      <td className="px-3 py-2 font-mono">{s.targetPort}</td>
      <td className="px-3 py-2">
        <span className={`inline-flex rounded-full border px-2 py-0.5 text-xs font-medium ${stateTone[state]}`}>{t(`state.${state}`)}</span>
        {s.aclRule && <div className="mt-1 font-mono text-[11px] text-bamboo-200/50" title={t('generatedRule')}>{s.aclRule}</div>}
        {s.live && readiness !== 'ready' && <p className="mt-1 text-[11px] text-amber-400">{t(`readiness.${readiness}`)}</p>}
      </td>
      <td className="space-x-2 px-3 py-2 whitespace-nowrap">
        {isAdmin && !s.approved && (
          <RowButton disabled={pending} onClick={() => act(() => approveIngressAction(s.id))}>{t('actions.approve')}</RowButton>
        )}
        {isAdmin && s.approved && (
          <RowButton disabled={pending} onClick={() => act(() => setIngressEnabledAction(s.id, !s.enabled))}>
            {s.enabled ? t('actions.disable') : t('actions.enable')}
          </RowButton>
        )}
        {isAdmin && !confirming && (
          <RowButton disabled={pending} onClick={() => setConfirming(true)}>{t('actions.delete')}</RowButton>
        )}
        {isAdmin && confirming && (
          <>
            <button type="button" disabled={pending}
              onClick={() => act(() => deleteIngressAction(s.id))}
              className="rounded-md border border-red-900/50 px-3 py-0.5 text-xs font-medium text-red-400 hover:bg-red-950/40 disabled:opacity-50">
              {t('actions.confirmDelete')}
            </button>
            <RowButton disabled={pending} onClick={() => setConfirming(false)}>{t('actions.cancel')}</RowButton>
          </>
        )}
      </td>
    </tr>
  );
}

function CreateForm({ peers, onError }: { peers: Peer[]; onError: (e: string | null) => void }) {
  const tt = useTranslations('ingress');
  const t = (key: string) => tt(`form.${key}`);
  const [open, setOpen] = useState(false);
  const [peerId, setPeerId] = useState('');
  const [label, setLabel] = useState('');
  const [port, setPort] = useState('');
  const [hint, setHint] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function submit() {
    const l = label.trim().toLowerCase();
    const p = Number(port);
    const bad = validateIngressLabel(l);
    if (bad) return setHint(bad === 'reserved' ? t('reservedLabel') : t('invalidLabel'));
    if (!validateIngressPort(p)) return setHint(t('invalidPort'));
    if (!peerId) return setHint(t('peerRequired'));
    setHint(null);
    startTransition(async () => {
      const res = await createIngressAction(peerId, l, p);
      onError(res.ok ? null : res.error);
      if (res.ok) {
        setOpen(false);
        setLabel('');
        setPort('');
      }
    });
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <button type="button" onClick={() => setOpen((v) => !v)}
          className="rounded-full bg-bamboo-50 px-4 py-1.5 text-[13px] font-medium text-ink-950 hover:bg-bamboo-100">
          {open ? t('cancel') : t('openButton')}
        </button>
      </div>
      {open && (
        <div className="grid gap-3 rounded-2xl bg-ink-900 p-4 sm:grid-cols-3">
          <Field id="ingress-peer" label={t('peerLabel')}>
            <select id="ingress-peer" value={peerId} onChange={(e) => setPeerId(e.target.value)} disabled={pending} className={inputCls}>
              <option value="">—</option>
              {peers.map((p) => <option key={p.id} value={p.id}>{p.hostname} ({p.ip})</option>)}
            </select>
          </Field>
          <Field id="ingress-label" label={t('labelLabel')} hint={t('labelHint')}>
            <input id="ingress-label" value={label} onChange={(e) => setLabel(e.target.value)} disabled={pending} className={inputCls} />
          </Field>
          <Field id="ingress-port" label={t('portLabel')} hint={t('portHint')}>
            <input id="ingress-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} disabled={pending} className={inputCls} />
          </Field>
          {hint && <p className="text-xs text-amber-400 sm:col-span-3">{hint}</p>}
          <div className="sm:col-span-3">
            <button type="button" onClick={submit} disabled={pending}
              className="rounded-full bg-bamboo-50 px-4 py-1.5 text-[13px] font-medium text-ink-950 hover:bg-bamboo-100">
              {t('submit')}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function TokenPanel({ list, peers, onError }: { list: IngressList; peers: Peer[]; onError: (e: string | null) => void }) {
  const tt = useTranslations('ingress');
  const t = (key: string) => tt(`token.${key}`);
  const [peerId, setPeerId] = useState(list.token?.peerId ?? '');
  const locale = useLocale();
  const [revealed, setRevealed] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  const [pending, startTransition] = useTransition();
  const current = list.token ? peers.find((p) => p.id === list.token?.peerId)?.hostname ?? list.token.peerId : null;

  function issue() {
    startTransition(async () => {
      const res = await issueIngressTokenAction(peerId);
      if (res.ok) {
        setRevealed(res.token);
        setCopyFailed(false);
        onError(null);
      } else {
        onError(res.error);
      }
    });
  }

  async function copy(token: string) {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(token);
      setCopyFailed(false);
    } catch {
      setCopyFailed(true);
    }
  }

  return (
    <section className="space-y-3 rounded-2xl bg-ink-900 p-4">
      <h2 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h2>
      <p className="text-[13px] text-bamboo-200/60">{t('description')}</p>
      <p className="text-xs text-bamboo-200/70">
        {list.token ? `${t('current')} ${current} · ${t('expires')} ${formatIngressExpiry(list.token.expiresAt, locale)}` : t('none')}
      </p>
      {peers.length === 0 ? (
        <p className="text-xs text-amber-400">{t('noIngressPeers')}</p>
      ) : (
        <div className="flex flex-wrap items-end gap-3">
          <Field id="ingress-token-peer" label={t('peerLabel')}>
            <select id="ingress-token-peer" value={peerId} onChange={(e) => setPeerId(e.target.value)} disabled={pending} className={inputCls}>
              <option value="">—</option>
              {peers.map((p) => <option key={p.id} value={p.id}>{p.hostname} ({p.ip})</option>)}
            </select>
          </Field>
          <button type="button" onClick={issue} disabled={pending || !peerId}
            className="rounded-full border border-bamboo-200/30 px-4 py-1.5 text-[13px] text-bamboo-50 hover:bg-[#777]/20">
            {list.token ? t('rotate') : t('issue')}
          </button>
        </div>
      )}
      {revealed && (
        <div className="space-y-2 rounded-lg border border-bamboo-300/40 p-3">
          <p className="text-xs font-medium text-bamboo-50">{t('revealTitle')}</p>
          <code className="block break-all font-mono text-xs text-bamboo-100">{revealed}</code>
          <p className="text-xs text-bamboo-200/60">{t('revealHint')}</p>
          <div className="flex gap-3">
            <button type="button" onClick={() => { void copy(revealed); }} className="text-xs text-bamboo-200 underline">{t('copy')}</button>
            <button type="button" onClick={() => setRevealed(null)} className="text-xs text-bamboo-200 underline">{t('close')}</button>
          </div>
          {copyFailed && <p className="text-xs text-amber-400">{t('copyFailed')}</p>}
        </div>
      )}
    </section>
  );
}

const inputCls =
  'w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 font-mono text-xs text-bamboo-50 outline-none focus:border-bamboo-300';

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-xs text-bamboo-200/70">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-bamboo-200/50">{hint}</p>}
    </div>
  );
}

function RowButton({ children, onClick, disabled }: { children: React.ReactNode; onClick: () => void; disabled: boolean }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled}
      className="rounded-full border border-bamboo-200/30 px-3 py-0.5 text-xs text-bamboo-50 hover:bg-[#777]/20 disabled:opacity-50">
      {children}
    </button>
  );
}
