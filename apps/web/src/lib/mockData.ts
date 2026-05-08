// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mock fixtures used by the scaffold before the controller's gRPC →
// gRPC-Web (or REST) bridge is wired. Replaced in a follow-up PR.

import type { AclPolicy, Peer } from './types';

export const mockPeers: Peer[] = [
  {
    id: 'peer-1',
    tenantId: 'tenant-1',
    hostname: 'alpha.dev.local',
    ip: '100.64.0.1',
    tags: ['dev', 'laptop'],
    os: 'darwin',
    clientVersion: '0.0.1',
    status: 'online',
    lastSeenAt: new Date(Date.now() - 30_000).toISOString(),
  },
  {
    id: 'peer-2',
    tenantId: 'tenant-1',
    hostname: 'beta.staging.local',
    ip: '100.64.0.2',
    tags: ['staging', 'server'],
    os: 'linux',
    clientVersion: '0.0.1',
    status: 'online',
    lastSeenAt: new Date(Date.now() - 5_000).toISOString(),
  },
  {
    id: 'peer-3',
    tenantId: 'tenant-1',
    hostname: 'ci-runner-3',
    ip: '100.64.0.3',
    tags: ['ci'],
    os: 'linux',
    clientVersion: '0.0.1',
    status: 'offline',
    lastSeenAt: new Date(Date.now() - 3_600_000).toISOString(),
  },
];

export const mockPolicy: AclPolicy = {
  revision: 1,
  updatedAt: new Date(Date.now() - 86_400_000).toISOString(),
  updatedBy: 'founder@example.com',
  hclSource: `policy "default" {
  rule "allow-dev-to-staging" {
    src = ["tag:dev"]
    dst = ["tag:staging:443,5432"]
  }

  rule "allow-ci-to-ghcr" {
    src = ["tag:ci"]
    dst = ["cidr:0.0.0.0/0:443"]
  }
}`,
  rules: [
    {
      id: 'rule-1',
      action: 'allow',
      sources: ['tag:dev'],
      destinations: ['tag:staging:443', 'tag:staging:5432'],
      description: 'Developers can reach staging services on standard ports.',
    },
    {
      id: 'rule-2',
      action: 'allow',
      sources: ['tag:ci'],
      destinations: ['cidr:0.0.0.0/0:443'],
      description: 'CI runners can reach the public internet over TLS.',
    },
  ],
};
