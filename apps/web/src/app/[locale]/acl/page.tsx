// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { fetchPolicy, fetchRecommendations, type ApiRecommendation } from '@/lib/api';
import type { AclPolicy } from '@/lib/types';
import { AclEditor } from '@/components/AclEditor';
import { FetchErrorState } from '@/components/FetchErrorState';

export default async function AclPage() {
 const [policy, recommendations] = await Promise.all([
 fetchPolicy(),
 fetchRecommendations(),
 ]);

 // The ACL page is the policy editor. Policy is load-bearing; if we
 // can't load it we surface the failure rather than render the empty
 // placeholder that lies about there being no policy. Recommendations
 // are advisory — failing to load them just hides the section.
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

 if (!policy.hclSource) {
 return (
 <div className="space-y-6">
 <header>
 <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
 </header>
 <p className="rounded-lg border border-dashed border-bamboo-200/30 p-8 text-center text-sm text-bamboo-200/60 dark:text-bamboo-200/40">
 {t('empty')}
 </p>
 </div>
 );
 }

 return (
 <div className="space-y-6">
 <header>
 <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
 <p className="text-sm text-bamboo-200/60">
 {t('revision')}: <span className="font-mono">{policy.revision}</span>
 {policy.updatedAt && (
 <>
 {' · '}
 {t('lastUpdated')}:{' '}
 <time dateTime={policy.updatedAt}>
 {new Date(policy.updatedAt).toLocaleString()}
 </time>
 </>
 )}
 </p>
 </header>

 <AclEditor hclSource={policy.hclSource} revision={policy.revision} />

 <section className="space-y-2">
 <h2 className="text-sm font-medium uppercase tracking-wide text-bamboo-200/60">
 {t('rules')}
 </h2>
 <ul className="space-y-2">
 {policy.rules.map((r) => (
 <li
 key={r.id}
 className="rounded-lg border border-ink-800 p-4 dark:border-ink-800"
 >
 <div className="flex flex-wrap items-baseline justify-between gap-2">
 <code className="font-mono text-xs text-bamboo-200/60">
 {r.id}
 </code>
 <span
 className={`rounded-full px-2 py-0.5 text-xs font-medium ${
 r.action === 'allow'
 ? 'bg-ink-800 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300'
 : 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300'
 }`}
 >
 {r.action}
 </span>
 </div>
 {r.description && (
 <p className="mt-2 text-sm text-bamboo-200/70 dark:text-bamboo-100">
 {r.description}
 </p>
 )}
 <dl className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
 <div>
 <dt className="text-bamboo-200/60">
 {t('rule.sources')}
 </dt>
 <dd className="mt-0.5 font-mono">{r.sources.join(', ')}</dd>
 </div>
 <div>
 <dt className="text-bamboo-200/60">
 {t('rule.destinations')}
 </dt>
 <dd className="mt-0.5 font-mono">{r.destinations.join(', ')}</dd>
 </div>
 </dl>
 </li>
 ))}
 </ul>
 </section>

 {recommendations.length > 0 && (
 <section className="space-y-3">
 <h2 className="text-sm font-medium uppercase tracking-wide text-bamboo-200/60">
 AI recommendations
 </h2>
 <ul className="space-y-3">
 {recommendations.map((r) => (
 <li
 key={r.id}
 className="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-900/60 dark:bg-amber-950/30"
 >
 <div className="flex flex-wrap items-baseline justify-between gap-2">
 <span className="font-medium text-amber-900 dark:text-amber-200">
 {r.summary}
 </span>
 <span className="text-xs font-mono text-amber-700 dark:text-amber-300">
 confidence {(r.confidence * 100).toFixed(0)}%
 </span>
 </div>
 {r.diff && (
 <pre className="mt-2 overflow-x-auto rounded bg-ink-950 p-2 font-mono text-xs leading-snug text-bamboo-50 dark:bg-ink-900 dark:text-bamboo-50">
 <code>{r.diff}</code>
 </pre>
 )}
 {r.evidence?.length > 0 && (
 <ul className="mt-2 list-inside list-disc text-xs text-amber-800 dark:text-amber-200">
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
