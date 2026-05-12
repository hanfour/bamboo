// SPDX-License-Identifier: AGPL-3.0-or-later
//
// bamboo brand mark. Two readings on purpose:
//
//   1. A bamboo culm — three nodal joints stacked on a vertical stem,
//      the way real bamboo bands as it grows.
//   2. A network topology — three nodes connected by a single line,
//      which is what bamboo as a product actually does.
//
// The glyph is single-color (currentColor) so it inherits the accent
// from the wrapping element. The wordmark uses Newsreader italic for
// editorial calm; the trailing period is the only ornament and it
// echoes the bamboo-400 accent of the glyph.

import { Link } from '@/i18n/routing';

type Size = 'sm' | 'lg';

export function Wordmark({
  size = 'sm',
  href = '/dashboard',
  className = '',
}: {
  size?: Size;
  href?: string | null;
  className?: string;
}) {
  const inner = (
    <span
      className={`inline-flex items-baseline ${
        size === 'lg' ? 'gap-3 sm:gap-4' : 'gap-2'
      } ${className}`}
    >
      <BambooGlyph
        className={`shrink-0 self-center text-bamboo-400 ${
          size === 'lg' ? 'h-9 w-9 sm:h-12 sm:w-12' : 'h-[18px] w-[18px]'
        }`}
      />
      <span
        className={`font-serif font-light italic leading-none text-zinc-900 dark:text-zinc-50 ${
          size === 'lg'
            ? 'text-5xl tracking-[-0.02em] sm:text-7xl'
            : 'text-xl tracking-[-0.01em]'
        }`}
      >
        bamboo
        <span className="ml-px text-bamboo-400">.</span>
      </span>
    </span>
  );

  if (href === null) {
    return inner;
  }
  return (
    <Link
      href={href}
      className="inline-flex items-baseline rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-bamboo-400/40"
      aria-label="bamboo"
    >
      {inner}
    </Link>
  );
}

// Three horizontal bands joined by a vertical hairline. Sized to 24x24
// viewBox, scales via Tailwind h-/w- utilities on the wrapper. The
// shapes use rx so a low-DPR raster still reads as a node, not a slab.
function BambooGlyph({ className = '' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden
      className={className}
    >
      {/* vertical stem */}
      <path d="M12 4.5 V10 M12 12.5 V18" stroke="currentColor" strokeWidth="0.9" strokeLinecap="round" />
      {/* three nodes (bamboo joints / network peers) */}
      <rect x="5"  y="2"  width="14" height="2.6" rx="1.1" fill="currentColor" />
      <rect x="5"  y="10" width="14" height="2.6" rx="1.1" fill="currentColor" />
      <rect x="5"  y="18" width="14" height="2.6" rx="1.1" fill="currentColor" />
    </svg>
  );
}
