// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { updateDNS64Action } from '@/lib/actions';

// DNS64Editor is the one writable surface on the DNS page. It edits the
// tenant-level DNS64 master switch + NAT64 synthesis prefix, wired to
// PATCH /api/v1/dns (admin-only on the wire). Draft state is seeded from
// the server-rendered config; Save is disabled until something changes,
// and a 403 for a non-admin viewer surfaces inline as the error string.
export function DNS64Editor({
  dns64Enabled,
  nat64Prefix,
}: {
  dns64Enabled: boolean;
  nat64Prefix: string;
}) {
  const t = useTranslations('dns');
  const [enabled, setEnabled] = useState(dns64Enabled);
  const [prefix, setPrefix] = useState(nat64Prefix);
  const [pending, startTransition] = useTransition();
  const [result, setResult] = useState<{ ok: boolean; msg?: string } | null>(null);

  const dirty = enabled !== dns64Enabled || prefix.trim() !== nat64Prefix;

  function save() {
    setResult(null);
    startTransition(async () => {
      const r = await updateDNS64Action({
        dns64Enabled: enabled,
        nat64Prefix: prefix.trim(),
      });
      setResult(r.ok ? { ok: true } : { ok: false, msg: r.error });
    });
  }

  return (
    <div className="space-y-3">
      <label className="flex items-center gap-2 text-sm text-bamboo-50">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => {
            setEnabled(e.target.checked);
            setResult(null);
          }}
          className="h-4 w-4 rounded border-bamboo-200/40 bg-ink-950 accent-bamboo-400"
        />
        {t('dns64Enabled')}
      </label>
      <p className="text-xs text-bamboo-200/60">{t('dns64Hint')}</p>

      <div className="grid grid-cols-[10rem_1fr] items-center gap-3 text-sm">
        <label htmlFor="nat64-prefix" className="text-bamboo-200/60">
          {t('nat64Prefix')}
        </label>
        <input
          id="nat64-prefix"
          type="text"
          value={prefix}
          onChange={(e) => {
            setPrefix(e.target.value);
            setResult(null);
          }}
          placeholder="64:ff9b::/96"
          spellCheck={false}
          autoComplete="off"
          className="min-w-0 rounded-md border border-bamboo-200/30 bg-ink-950 px-3 py-1.5 font-mono text-xs text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
        />
      </div>
      <p className="text-xs text-bamboo-200/60">{t('nat64PrefixHint')}</p>

      <div className="flex items-center gap-3 pt-1">
        <button
          type="button"
          onClick={save}
          disabled={pending || !dirty}
          className="rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {pending ? t('saving') : t('save')}
        </button>
        {result?.ok && (
          <span role="status" className="text-sm text-bamboo-300">
            {t('saved')}
          </span>
        )}
      </div>

      {result && !result.ok && (
        <div
          role="alert"
          className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400"
        >
          {t('saveError')}: {result.msg}
        </div>
      )}
    </div>
  );
}
