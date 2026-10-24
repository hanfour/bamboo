// SPDX-License-Identifier: AGPL-3.0-or-later

// parseTagList turns a comma-separated input into the tag names the
// controller stores. Policy rules write `tag:db`, but the stored tag is
// `db`, so a typed `tag:` prefix is dropped. Empties and repeats go too.
export function parseTagList(input: string): string[] {
  const out: string[] = [];
  for (const raw of input.split(',')) {
    const tag = raw.trim().replace(/^tag:/i, '').trim();
    if (tag !== '' && !out.includes(tag)) out.push(tag);
  }
  return out;
}
