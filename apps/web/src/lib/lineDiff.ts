// SPDX-License-Identifier: AGPL-3.0-or-later

// lineDiff compares two text blocks line-by-line and emits a list of
// rows ready for side-by-side rendering in the ACL Versions tab
// (§3a roadmap follow-up to #134's Versions UI).
//
// Why LCS-by-hand instead of a library: the ACL HCL document is the
// only diff site in the app, the inputs are short (typically <100
// lines per revision), and the production dependency set is
// deliberately tight (5 prod packages). The trade-off of a few dozen
// lines of LCS DP vs adding react-diff-viewer-continued (~50 KB +
// transitive `diff` package) lands on the side of writing it in-tree.
//
// Algorithm: classic O(n·m) LCS DP table, then walk the table from
// (n,m) back to (0,0) to emit alignment rows. Equal lines collapse
// into a single "same" row holding both sides; non-equal segments
// emit "remove" (left-only) and "add" (right-only) rows.
//
// Edge cases handled:
//   * empty inputs on either side — degenerate to all-add or all-
//     remove rows
//   * trailing newline mismatch — splitLines normalizes it so a
//     visible-blank "different trailing newline" doesn't dominate
//     the diff
//   * Windows line endings — \r\n is normalized to \n at the same
//     splitLines layer
//
// What we deliberately do NOT do here:
//   * Intra-line word-level highlighting. Token-level diff inside a
//     changed line is a v3 follow-up; for HCL whose grammar is line-
//     dominated this is rarely needed and the rendering cost grows
//     fast.
//   * Move detection. A block "moved" from line 10 to line 40 reads
//     as remove+add. Standard for line-level diff.

export type DiffRowKind = 'same' | 'add' | 'remove';

// One row in the rendered side-by-side diff. `kind`:
//   * 'same'   — left and right are equal; oldLine === newLine
//   * 'add'    — right-only line; oldLine is undefined
//   * 'remove' — left-only line; newLine is undefined
// oldLineNumber / newLineNumber are 1-based gutter labels for the
// rendered table. Undefined when the side is empty for that row.
export type DiffRow = {
  kind: DiffRowKind;
  oldLine?: string;
  newLine?: string;
  oldLineNumber?: number;
  newLineNumber?: number;
};

// splitLines normalizes CRLF to LF and strips a single trailing
// newline so an "ends with \n" mismatch doesn't manifest as a stray
// blank-line diff. Returns an empty array for an empty string —
// distinct from [""], which would render as one visible blank row.
export function splitLines(s: string): string[] {
  if (s === '') return [];
  const normalized = s.replace(/\r\n/g, '\n');
  const trimmed = normalized.endsWith('\n') ? normalized.slice(0, -1) : normalized;
  return trimmed.split('\n');
}

// lineDiff is the entry point. Pure: same inputs → same output.
// Suitable for use in a server component or a client component.
export function lineDiff(oldText: string, newText: string): DiffRow[] {
  const oldLines = splitLines(oldText);
  const newLines = splitLines(newText);
  const n = oldLines.length;
  const m = newLines.length;

  // LCS length table. dp[i][j] = length of the LCS of
  // oldLines[0..i-1] and newLines[0..j-1].
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = 1; i <= n; i++) {
    for (let j = 1; j <= m; j++) {
      if (oldLines[i - 1] === newLines[j - 1]) {
        dp[i][j] = dp[i - 1][j - 1] + 1;
      } else {
        dp[i][j] = dp[i - 1][j] >= dp[i][j - 1] ? dp[i - 1][j] : dp[i][j - 1];
      }
    }
  }

  // Walk back from (n,m) emitting rows. We push in reverse and
  // reverse at the end so the emit logic stays straightforward.
  const out: DiffRow[] = [];
  let i = n;
  let j = m;
  while (i > 0 && j > 0) {
    if (oldLines[i - 1] === newLines[j - 1]) {
      out.push({
        kind: 'same',
        oldLine: oldLines[i - 1],
        newLine: newLines[j - 1],
        oldLineNumber: i,
        newLineNumber: j,
      });
      i--;
      j--;
    } else if (dp[i - 1][j] >= dp[i][j - 1]) {
      // Prefer removing from the old side first when ties — this
      // groups removals above additions in the rendered output,
      // matching what most diff renderers do (and what readers
      // expect for "this block got replaced by that block").
      out.push({ kind: 'remove', oldLine: oldLines[i - 1], oldLineNumber: i });
      i--;
    } else {
      out.push({ kind: 'add', newLine: newLines[j - 1], newLineNumber: j });
      j--;
    }
  }
  while (i > 0) {
    out.push({ kind: 'remove', oldLine: oldLines[i - 1], oldLineNumber: i });
    i--;
  }
  while (j > 0) {
    out.push({ kind: 'add', newLine: newLines[j - 1], newLineNumber: j });
    j--;
  }
  return out.reverse();
}
