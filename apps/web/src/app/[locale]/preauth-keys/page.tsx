// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { PreAuthKeyTable } from '@/components/PreAuthKeyTable';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchPreAuthKeys } from '@/lib/api';
import type { PreAuthKey } from '@/lib/types';

export default async function PreAuthKeysPage() {
  const keys = await fetchPreAuthKeys();
  // Pre-auth-key management is admin-only: in prod mode a non-admin
  // user hitting this page should see "you need admin", not an empty
  // table.
  if (keys.kind !== 'ok') {
    return <FetchErrorState kind={keys.kind} />;
  }
  return <PreAuthKeysView keys={keys.value} />;
}

function PreAuthKeysView({ keys }: { keys: PreAuthKey[] }) {
  const t = useTranslations('preAuthKeys');
  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
      </header>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('subtitle')}</p>
      <PreAuthKeyTable keys={keys} />
    </div>
  );
}
