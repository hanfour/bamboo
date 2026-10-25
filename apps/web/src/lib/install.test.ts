// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { agentPrompt, installCommand } from './install';

describe('installCommand', () => {
  it('passes the key through the environment of sudo', () => {
    expect(installCommand('https://bamboo.example.com', 'bka_ab_cd')).toBe(
      "curl -fsSL 'https://bamboo.example.com/install.sh' | sudo env BAMBOO_AUTH_KEY='bka_ab_cd' sh",
    );
  });

  it('quotes a value that tries to break out of the command', () => {
    const cmd = installCommand('https://x', "a'; rm -rf / #");
    expect(cmd).toContain(`BAMBOO_AUTH_KEY='a'\\''; rm -rf / #'`);
  });
});

describe('agentPrompt', () => {
  it('contains the command and the do-not-leak instruction', () => {
    const p = agentPrompt('https://x', 'bka_a_b', false);
    expect(p).toContain(installCommand('https://x', 'bka_a_b'));
    expect(p).toContain('Do not print the key again');
    expect(p).not.toContain('single-use');
  });

  it('warns agents not to retry a single-use key', () => {
    expect(agentPrompt('https://x', 'bka_a_b', true)).toContain('do not retry');
  });
});
