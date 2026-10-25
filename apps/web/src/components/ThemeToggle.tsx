// SPDX-License-Identifier: AGPL-3.0-or-later
'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
import { applyTheme, readTheme, saveTheme, type Theme } from '@/lib/theme';

const OPTIONS: Theme[] = ['system', 'light', 'dark'];

// ThemeToggle is a three-way segmented control: system, light, dark.
// The stored value is read after mount, so the server render and first
// client render agree; until then no segment is marked.
export function ThemeToggle({ className = '' }: { className?: string }) {
  const t = useTranslations('theme');
  const [theme, setTheme] = useState<Theme | null>(null);

  useEffect(() => {
    setTheme(readTheme());
  }, []);

  // While following the system, track OS changes live.
  useEffect(() => {
    if (theme !== 'system') return;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => applyTheme('system');
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, [theme]);

  return (
    <div
      role="radiogroup"
      aria-label={t('label')}
      className={`inline-flex items-center gap-0.5 rounded-full bg-[#777]/15 p-0.5 ${className}`}
    >
      {OPTIONS.map((opt) => (
        <button
          key={opt}
          type="button"
          role="radio"
          aria-checked={theme === opt}
          aria-label={t(opt)}
          title={t(opt)}
          onClick={() => {
            setTheme(opt);
            saveTheme(opt);
          }}
          className={[
            'flex h-7 w-7 items-center justify-center rounded-full transition-colors',
            theme === opt
              ? 'bg-[#777]/35 text-bamboo-50'
              : 'text-bamboo-200/60 hover:text-bamboo-50',
          ].join(' ')}
        >
          <Icon kind={opt} />
        </button>
      ))}
    </div>
  );
}

function Icon({ kind }: { kind: Theme }) {
  const common = {
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.5,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    className: 'h-4 w-4',
    'aria-hidden': true,
  };
  if (kind === 'light') {
    return (
      <svg {...common}>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6l1.4 1.4M17 17l1.4 1.4M18.4 5.6L17 7M7 17l-1.4 1.4" />
      </svg>
    );
  }
  if (kind === 'dark') {
    return (
      <svg {...common}>
        <path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z" />
      </svg>
    );
  }
  return (
    <svg {...common}>
      <rect x="3" y="4" width="18" height="12" rx="2" />
      <path d="M8 20h8M12 16v4" />
    </svg>
  );
}
