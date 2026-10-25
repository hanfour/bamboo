// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers behind the overview activity chart. Days are UTC dates
// ("YYYY-MM-DD"), matching how the controller buckets online seconds.

export type PresenceEntry = { peerId: string; day: string; seconds: number };

export type HeatCell = { day: string; seconds: number; level: 0 | 1 | 2 | 3 | 4 } | null;

export type PresenceStats = {
  totalSeconds: number;
  mostActiveMonth: string | null; // "YYYY-MM"
  mostActiveDay: string | null;
  longestStreak: number;
  currentStreak: number;
};

const DAY_MS = 86_400_000;

function toDate(day: string): Date {
  return new Date(`${day}T00:00:00Z`);
}

function fmt(d: Date): string {
  return d.toISOString().slice(0, 10);
}

// sumByDay folds entries into day -> seconds, optionally for one peer.
export function sumByDay(entries: PresenceEntry[], peerId?: string): Map<string, number> {
  const m = new Map<string, number>();
  for (const e of entries) {
    if (peerId && e.peerId !== peerId) continue;
    m.set(e.day, (m.get(e.day) ?? 0) + e.seconds);
  }
  return m;
}

// buildGrid lays `weeks` week-columns ending in the week of `today`
// (Sunday first). Cells after today are null. Levels split the busiest
// day into four bands so one long day does not flatten the rest.
export function buildGrid(byDay: Map<string, number>, today: string, weeks = 53): HeatCell[][] {
  const end = toDate(today);
  const start = new Date(end.getTime() - (end.getUTCDay() + (weeks - 1) * 7) * DAY_MS);
  let max = 0;
  for (const v of byDay.values()) max = Math.max(max, v);
  const level = (s: number): 0 | 1 | 2 | 3 | 4 => {
    if (s <= 0 || max <= 0) return 0;
    return Math.min(4, Math.ceil((s / max) * 4)) as 1 | 2 | 3 | 4;
  };
  const cols: HeatCell[][] = [];
  for (let w = 0; w < weeks; w++) {
    const col: HeatCell[] = [];
    for (let d = 0; d < 7; d++) {
      const date = new Date(start.getTime() + (w * 7 + d) * DAY_MS);
      if (date > end) {
        col.push(null);
        continue;
      }
      const day = fmt(date);
      const seconds = byDay.get(day) ?? 0;
      col.push({ day, seconds, level: level(seconds) });
    }
    cols.push(col);
  }
  return cols;
}

// computeStats derives the summary row. A day counts toward a streak
// when the node was online at all. The current streak survives an idle
// today if yesterday was active, so it does not reset at midnight UTC.
export function computeStats(byDay: Map<string, number>, today: string): PresenceStats {
  let total = 0;
  const months = new Map<string, number>();
  let bestDay: string | null = null;
  let bestSecs = 0;
  const active: string[] = [];
  for (const [day, s] of byDay) {
    if (s <= 0) continue;
    total += s;
    months.set(day.slice(0, 7), (months.get(day.slice(0, 7)) ?? 0) + s);
    if (s > bestSecs) {
      bestSecs = s;
      bestDay = day;
    }
    active.push(day);
  }
  active.sort();
  let longest = 0;
  let run = 0;
  let prev: number | null = null;
  for (const day of active) {
    const t = toDate(day).getTime();
    run = prev !== null && t - prev === DAY_MS ? run + 1 : 1;
    longest = Math.max(longest, run);
    prev = t;
  }
  let current = 0;
  let cursor = toDate(today).getTime();
  if (!(byDay.get(fmt(new Date(cursor))) ?? 0)) cursor -= DAY_MS;
  while ((byDay.get(fmt(new Date(cursor))) ?? 0) > 0) {
    current++;
    cursor -= DAY_MS;
  }
  let bestMonth: string | null = null;
  let bestMonthSecs = 0;
  for (const [m, s] of months) {
    if (s > bestMonthSecs) {
      bestMonthSecs = s;
      bestMonth = m;
    }
  }
  return {
    totalSeconds: total,
    mostActiveMonth: bestMonth,
    mostActiveDay: bestDay,
    longestStreak: longest,
    currentStreak: current,
  };
}

// formatHours renders seconds as "12.5 h", or minutes below one hour.
export function formatDuration(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)} m`;
  const h = seconds / 3600;
  return `${h >= 100 ? Math.round(h) : Math.round(h * 10) / 10} h`;
}
