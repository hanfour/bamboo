// SPDX-License-Identifier: AGPL-3.0-or-later

// Commands and prompts shown after a pre-auth key is minted. The key goes
// in the environment (not an argument) so it stays out of the process list,
// and is single-quoted so a pasted command cannot be altered by the shell.

function shellQuote(v: string): string {
  return `'${v.replace(/'/g, `'\\''`)}'`;
}

// installCommand is the one-liner for a Linux machine.
export function installCommand(origin: string, secret: string): string {
  return `curl -fsSL ${shellQuote(`${origin}/install.sh`)} | sudo env BAMBOO_AUTH_KEY=${shellQuote(secret)} sh`;
}

// agentPrompt is text to paste into a coding agent (Claude Code, Codex,
// Grok and the like) running on the machine to enroll.
export function agentPrompt(origin: string, secret: string, singleUse: boolean): string {
  const lines = [
    'Install the bamboo mesh VPN client on this Linux machine and connect it to my network.',
    '',
    'Run exactly this command (it downloads the client, verifies its checksum, registers this machine and starts a systemd service):',
    '',
    installCommand(origin, secret),
    '',
    'Then confirm it worked: run `systemctl status bamboo --no-pager` and `sudo bamboo status`, and tell me whether this machine joined and which mesh IP it got.',
    '',
    'Do not print the key again, write it to any file, or commit it.',
  ];
  if (singleUse) {
    lines.push('The key is single-use: if the command fails after registering, do not retry it. Tell me so I can mint a new key.');
  }
  return lines.join('\n');
}
