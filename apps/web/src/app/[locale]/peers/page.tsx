// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { PeerTable } from '@/components/PeerTable';
import { mockPeers } from '@/lib/mockData';

export default function PeersPage() {
  const t = useTranslations('peers');
  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <button
          type="button"
          className="rounded-md bg-bamboo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-bamboo-700 dark:bg-bamboo-500 dark:hover:bg-bamboo-400"
        >
          {t('addPeer')}
        </button>
      </header>
      <PeerTable peers={mockPeers} />
    </div>
  );
}
