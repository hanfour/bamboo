// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { PreAuthKeyTable } from '@/components/PreAuthKeyTable';
import { FetchErrorState } from '@/components/FetchErrorState';
import { PageHeader } from '@/components/PageHeader';
import { fetchPreAuthKeys } from '@/lib/api';
import type { PreAuthKey } from '@/lib/types';

export default async function PreAuthKeysPage() {
  const keys = await fetchPreAuthKeys();
  if (keys.kind !== 'ok') {
    return <FetchErrorState kind={keys.kind} />;
  }
  return <PreAuthKeysView keys={keys.value} />;
}

function PreAuthKeysView({ keys }: { keys: PreAuthKey[] }) {
  const t = useTranslations('preAuthKeys');
  return (
    <div>
      <PageHeader
        kicker="auth · 預先驗證金鑰"
        title={t('title')}
        description={t('subtitle')}
      />
      <PreAuthKeyTable keys={keys} />
    </div>
  );
}
