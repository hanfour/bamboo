// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { fetchPolicy, fetchRecommendations, type ApiRecommendation } from '@/lib/api';
import type { AclPolicy } from '@/lib/types';
import { FetchErrorState } from '@/components/FetchErrorState';
import { PageHeader } from '@/components/PageHeader';

export default async function AclPage() {
  const [policy, recommendations] = await Promise.all([
    fetchPolicy(),
    fetchRecommendations(),
  ]);

  if (policy.kind !== 'ok') {
    return <FetchErrorState kind={policy.kind} />;
  }

  return (
    <Acl
      policy={policy.value}
      recommendations={recommendations.kind === 'ok' ? recommendations.value : []}
    />
  );
}

function Acl({
  policy,
  recommendations,
}: {
  policy: AclPolicy;
  recommendations: ApiRecommendation[];
}) {
  const t = useTranslations('acl');

  // Empty state — distinct page header that explains the situation
  // rather than rendering a header-then-card hybrid.
  if (!policy.hclSource) {
    return (
      <div>
        <PageHeader kicker="policy · 存取政策" title={t('title')} />
        <p className="rounded-md border border-dashed border-white/[0.08] px-8 py-12 text-center text-sm leading-relaxed text-zinc-400">
          {t('empty')}
        </p>
      </div>
    );
  }

  return (
    <div>
      <PageHeader
        kicker="policy · 存取政策"
        title={t('title')}
        description={
          <span className="font-mono text-[12px] uppercase tracking-[0.18em] text-zinc-500">
            <span className="text-zinc-400">{t('revision')}</span>
            <span className="mx-2 text-zinc-700">·</span>
            <span className="text-bamboo-300">rev {policy.revision}</span>
            {policy.updatedAt && (
              <>
                <span className="mx-2 text-zinc-700">·</span>
                <span>{t('lastUpdated')}: </span>
                <time dateTime={policy.updatedAt}>
                  {new Date(policy.updatedAt).toLocaleString()}
                </time>
              </>
            )}
          </span>
        }
        meta={
          <button
            type="button"
            className="rounded-md border border-white/[0.08] px-3 py-1.5 font-mono text-[11px] uppercase tracking-[0.18em] text-zinc-300 transition hover:border-bamboo-400/40 hover:bg-bamboo-500/[0.06]"
          >
            {t('edit')}
          </button>
        }
      />

      <section className="mb-12">
        <SectionHeading>{t('viewSource')}</SectionHeading>
        <pre className="overflow-x-auto rounded-md border border-white/[0.06] bg-ink-900/60 p-5 font-mono text-[12px] leading-relaxed text-zinc-200">
          <code>{policy.hclSource}</code>
        </pre>
      </section>

      <section className="mb-12">
        <SectionHeading>{t('rules')}</SectionHeading>
        <ul className="space-y-3">
          {policy.rules.map((r) => (
            <li
              key={r.id}
              className="rounded-md border border-white/[0.06] bg-white/[0.01] p-5"
            >
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <code className="font-mono text-[11px] uppercase tracking-[0.16em] text-zinc-500">
                  {r.id}
                </code>
                <ActionPill action={r.action} />
              </div>
              {r.description && (
                <p className="mt-2 text-sm text-zinc-300">{r.description}</p>
              )}
              <dl className="mt-4 grid gap-3 sm:grid-cols-2">
                <RuleField label={t('rule.sources')} values={r.sources} />
                <RuleField label={t('rule.destinations')} values={r.destinations} />
              </dl>
            </li>
          ))}
        </ul>
      </section>

      {recommendations.length > 0 && (
        <section className="mb-12">
          <SectionHeading>AI recommendations</SectionHeading>
          <ul className="space-y-3">
            {recommendations.map((r) => (
              <li
                key={r.id}
                className="rounded-md border border-amber-400/30 bg-amber-500/[0.04] p-5"
              >
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm font-medium text-amber-200">
                    {r.summary}
                  </span>
                  <span className="font-mono text-[10px] uppercase tracking-[0.20em] text-amber-300/80">
                    confidence {(r.confidence * 100).toFixed(0)}%
                  </span>
                </div>
                {r.diff && (
                  <pre className="mt-3 overflow-x-auto rounded border border-amber-400/20 bg-amber-950/30 p-3 font-mono text-[11px] leading-snug text-amber-100">
                    <code>{r.diff}</code>
                  </pre>
                )}
                {r.evidence?.length > 0 && (
                  <ul className="mt-3 list-inside list-disc text-[12px] text-amber-200/80">
                    {r.evidence.map((e, i) => (
                      <li key={i}>{e}</li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-4 flex items-baseline gap-3">
      <h2 className="font-mono text-[11px] uppercase tracking-[0.24em] text-zinc-500">
        {children}
      </h2>
      <span aria-hidden className="h-px flex-1 bg-white/[0.06]" />
    </div>
  );
}

function ActionPill({ action }: { action: 'allow' | 'deny' }) {
  const tone =
    action === 'allow'
      ? 'border-bamboo-400/40 bg-bamboo-500/[0.08] text-bamboo-300'
      : 'border-red-400/40 bg-red-500/[0.08] text-red-300';
  const dot = action === 'allow' ? 'bg-bamboo-400' : 'bg-red-400';
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.16em] ${tone}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${dot}`} aria-hidden />
      {action}
    </span>
  );
}

function RuleField({ label, values }: { label: string; values: string[] }) {
  return (
    <div>
      <dt className="font-mono text-[10px] uppercase tracking-[0.22em] text-zinc-500">
        {label}
      </dt>
      <dd className="mt-1.5 flex flex-wrap gap-1.5">
        {values.length === 0 ? (
          <span className="font-mono text-[11px] text-zinc-600">—</span>
        ) : (
          values.map((v, i) => (
            <code
              key={i}
              className="rounded border border-white/[0.06] bg-white/[0.02] px-1.5 py-0.5 font-mono text-[11px] text-zinc-300"
            >
              {v}
            </code>
          ))
        )}
      </dd>
    </div>
  );
}
