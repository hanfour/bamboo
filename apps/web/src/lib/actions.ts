// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server Actions for peer mutations. Each function runs on the
// Next.js server so the bamboo_session cookie + tenant headers can
// be attached to the controller request the same way fetchPeers
// does. On success we revalidate the /peers route so the drawer +
// table re-render with fresh server-rendered data on the next
// navigation tick.

'use server';

import { cookies } from 'next/headers';
import { revalidatePath } from 'next/cache';
import type { PolicySimulation } from '@/lib/types';

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

export type ActionResult = { ok: true } | { ok: false; error: string };

// MintResult adds the minted secret on success. Used by
// mintPreAuthKeyAction; the modal needs the plaintext to render the
// install instructions immediately (the controller only returns it
// once — it never appears on a subsequent GET).
export type MintResult =
  | { ok: true; secret: string; id: string; description: string }
  | { ok: false; error: string };

async function buildHeaders(): Promise<Record<string, string>> {
  const headers: Record<string, string> = {
    'X-Tenant-Slug': TENANT,
    'Content-Type': 'application/json',
  };
  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  if (session?.value) {
    headers['Cookie'] = `${SESSION_COOKIE}=${session.value}`;
  }
  return headers;
}

async function patchPeer(
  id: string,
  body: Record<string, unknown>,
): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: await buildHeaders(),
      body: JSON.stringify(body),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

export async function renamePeerAction(id: string, hostname: string): Promise<ActionResult> {
  const trimmed = hostname.trim();
  if (trimmed === '') {
    return { ok: false, error: 'hostname cannot be empty' };
  }
  return patchPeer(id, { hostname: trimmed });
}

export async function setPeerStatusAction(
  id: string,
  status: 'online' | 'offline' | 'disabled',
): Promise<ActionResult> {
  return patchPeer(id, { status });
}

export async function setPeerTagsAction(id: string, tags: string[]): Promise<ActionResult> {
  return patchPeer(id, { tags });
}

// renamePeerDnsNameAction updates the MagicDNS label. Server-side
// validation: the controller's PATCH handler enforces strict slug
// rules ([a-z0-9-], no leading/trailing dash, ≤63 chars) and
// returns 409 on collision within the tenant. Pass an empty string
// to clear the column (peer becomes unresolvable until the next
// register auto-slugifies again).
export async function renamePeerDnsNameAction(
  id: string,
  dnsName: string,
): Promise<ActionResult> {
  // Don't trim or lowercase here — the controller does the strict
  // validation we want to surface to the user. Trimming silently
  // would mask a leading-space typo before the server sees it.
  return patchPeer(id, { dnsName });
}

// mintPreAuthKeyAction creates a one-time-use (or reusable) pre-
// auth key and returns the plaintext secret. The secret is only
// available in this response — the controller hashes it before
// storage, so the modal MUST render it immediately and the caller
// must capture it before closing.
export async function mintPreAuthKeyAction(input: {
  description: string;
  reusable: boolean;
  // autoApprove opts the minted key out of the device-approval queue
  // (issue #133). Defaults false so the safe path is the path of
  // least resistance.
  autoApprove?: boolean;
}): Promise<MintResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/preauth-keys`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({
        description: input.description.trim(),
        reusable: input.reusable,
        ephemeral: false,
        autoApprove: input.autoApprove ?? false,
      }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    const body = (await res.json()) as {
      id: string;
      description?: string;
      secret: string;
    };
    return {
      ok: true,
      id: body.id,
      description: body.description ?? '',
      secret: body.secret,
    };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// InviteResult adds the minted invitation token on success. Used by
// inviteUserAction; the modal needs the plaintext to display it once
// — the controller stores only the bcrypt hash, so subsequent GETs
// will not return it. `duplicate` is set on 409 so the UI can render
// a friendlier "already invited this address" message instead of the
// generic status-code error. `emailSent` is true when the controller
// successfully handed the invite email to its SMTP relay; false when
// SMTP is unconfigured OR delivery failed. The UI uses it to choose
// between "we emailed them" and "copy this token and share it
// manually" copy.
export type InviteResult =
  | {
      ok: true;
      id: string;
      email: string;
      token: string;
      expiresAt: string;
      emailSent: boolean;
    }
  | { ok: false; error: string; duplicate?: boolean };

// inviteUserAction mints a tenant invitation. Admin-only on the wire
// (the controller's requireAdmin gate returns 403 for member-role
// callers). The plaintext token is shown in the result modal once;
// subsequent reads return only the bcrypt hash.
//
// Currently the redeem path (OIDC-callback claim-on-first-login) is
// not wired — the UI surfaces this honestly in the result modal so
// admins don't distribute tokens that can't yet be used.
export async function inviteUserAction(input: {
  email: string;
  isAdmin: boolean;
}): Promise<InviteResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/invitations`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({
        email: input.email.trim(),
        isAdmin: input.isAdmin,
      }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      const duplicate = res.status === 409;
      return {
        ok: false,
        error: `${res.status} ${text || res.statusText}`,
        ...(duplicate ? { duplicate: true } : {}),
      };
    }
    const body = (await res.json()) as {
      id: string;
      email: string;
      token: string;
      expiresAt: string;
      emailSent?: boolean;
    };
    revalidatePath('/[locale]/users', 'page');
    return {
      ok: true,
      id: body.id,
      email: body.email,
      token: body.token,
      expiresAt: body.expiresAt,
      emailSent: Boolean(body.emailSent),
    };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// revokeInvitationAction flips a pending invitation to revoked.
// Backend returns 204 on success and 404 on "already terminal" (the
// repo's MarkRevoked only flips pending → revoked; accepted /
// already-revoked rows return ErrNotFound). We treat 404 as a soft
// success — the row is in its desired terminal state regardless —
// so the UI doesn't error when the user double-clicks or refreshes
// after a successful revoke.
export async function revokeInvitationAction(id: string): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/invitations/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok && res.status !== 204 && res.status !== 404) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/users', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// revokePreAuthKeyAction marks a pre-auth key as revoked. Idempotent
// at the controller (re-revoking returns 204), so the UI doesn't
// have to guard against double-clicks.
export async function revokePreAuthKeyAction(id: string): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/preauth-keys/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/preauth-keys', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// PolicySaveResult includes the new revision on success so the editor
// can present "saved (revision N)" feedback and update its
// optimistic-concurrency baseline. On parse error the controller
// returns 400 with the parser message in the response body; we
// surface it verbatim under `error` so the editor renders it inline
// near the textarea. `staleRevision` is true on 409 — the UI shows
// "someone else saved; refresh to merge" instead of the bare error.
export type PolicySaveResult =
  | { ok: true; revision: number }
  | { ok: false; error: string; staleRevision?: boolean };

// setPolicyAction sends a new HCL policy to the controller. The
// expectedRevision parameter is the policy.revision the editor
// loaded with — when non-zero, the controller's Put returns 409 if
// another admin saved in the meantime. We treat that as a soft error
// the UI handles, not a transport failure.
export async function setPolicyAction(input: {
  hclSource: string;
  expectedRevision: number;
}): Promise<PolicySaveResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/policy`, {
      method: 'PUT',
      headers: await buildHeaders(),
      body: JSON.stringify({
        hclSource: input.hclSource,
        expectedRevision: input.expectedRevision,
      }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      const staleRevision = res.status === 409;
      return {
        ok: false,
        error: text || `${res.status} ${res.statusText}`,
        ...(staleRevision ? { staleRevision: true } : {}),
      };
    }
    const body = (await res.json()) as { revision: number };
    revalidatePath('/[locale]/acl', 'page');
    return { ok: true, revision: body.revision };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// validatePolicyAction runs the controller's HCL parser against a
// draft without writing (issue #134). Returns ok+rules on success;
// ok:false + error on parse failure (the editor renders the error
// inline). Distinct from setPolicyAction's 400 path because the
// Validate button fires standalone, not as part of a save attempt.
export type ValidatePolicyResult =
  | { ok: true; rules: number }
  | { ok: false; error: string };

export async function validatePolicyAction(
  hclSource: string,
): Promise<ValidatePolicyResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/policy/validate`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({ hclSource }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: text || `${res.status} ${res.statusText}` };
    }
    const body = (await res.json()) as { rules: number };
    return { ok: true, rules: body.rules };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// simulatePolicyAction asks the controller to compile a draft HCL
// against the current peer registry and return the allow/deny
// matrix. Powers the editor's "Preview" tab.
export type SimulatePolicyResult =
  | { ok: true; simulation: PolicySimulation }
  | { ok: false; error: string };

export async function simulatePolicyAction(
  hclSource: string,
): Promise<SimulatePolicyResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/policy/simulate`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({ hclSource }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: text || `${res.status} ${res.statusText}` };
    }
    const body = (await res.json()) as PolicySimulation;
    return { ok: true, simulation: body };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// rollbackPolicyAction re-applies a past revision as a new current
// (issue #134). Server-side this is a Put of the older HCL — the
// audit row makes the action visible. Idempotent at the controller
// (404 on unknown revision is treated as a hard error here; the UI
// stays on the Versions tab so the operator can refresh + retry).
export async function rollbackPolicyAction(revision: number): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/policy/rollback`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({ revision }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: text || `${res.status} ${res.statusText}` };
    }
    revalidatePath('/[locale]/acl', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

export async function deletePeerAction(id: string): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// approvePeerAction flips a pending peer into approved (issue #133).
// Admin-only on the wire; the controller returns 403 for non-admins,
// 409 for already-rejected peers (the audit trail rules out un-
// rejection), and 404 for unknown ids. Idempotent: re-approving an
// already-approved row returns 200 with no audit entry written —
// the UI just refreshes.
export async function approvePeerAction(id: string): Promise<ActionResult> {
  return peerApprovalAction(id, 'approve');
}

// rejectPeerAction is the sibling of approvePeerAction. Rejection is
// terminal: the row is retained for audit, but `peerApprovalAction`
// cannot bring it back to approved. Admin can follow up with
// deletePeerAction to free the pubkey for a fresh registration.
export async function rejectPeerAction(id: string): Promise<ActionResult> {
  return peerApprovalAction(id, 'reject');
}

// setApprovedRoutesAction is the admin sign-off side of subnet
// routing (issue #136). Body is the full approved subset — the
// controller validates that every CIDR in `routes` is a subset of
// the peer's current advertised_routes (api.go:1163), so passing
// an empty array effectively revokes all previously-approved routes.
// Returns the updated apiPeerJSON, but the action just reports ok.
export async function setApprovedRoutesAction(
  id: string,
  routes: string[],
): Promise<ActionResult> {
  try {
    const res = await fetch(
      `${BASE}/api/v1/peers/${encodeURIComponent(id)}/routes`,
      {
        method: 'POST',
        headers: await buildHeaders(),
        body: JSON.stringify({ routes }),
        cache: 'no-store',
      },
    );
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// setExitNodeApprovedAction is the admin sign-off side of exit
// nodes (issue #137). Approving requires the peer to already be
// exit_node_capable (api.go:1234) — the controller returns 409
// otherwise, which the UI surfaces verbatim. Revoking (false) is
// always allowed regardless of capable state.
export async function setExitNodeApprovedAction(
  id: string,
  approved: boolean,
): Promise<ActionResult> {
  try {
    const res = await fetch(
      `${BASE}/api/v1/peers/${encodeURIComponent(id)}/exit-node`,
      {
        method: 'POST',
        headers: await buildHeaders(),
        body: JSON.stringify({ approved }),
        cache: 'no-store',
      },
    );
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

async function peerApprovalAction(
  id: string,
  verb: 'approve' | 'reject',
): Promise<ActionResult> {
  try {
    const res = await fetch(
      `${BASE}/api/v1/peers/${encodeURIComponent(id)}/${verb}`,
      {
        method: 'POST',
        headers: await buildHeaders(),
        cache: 'no-store',
      },
    );
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// WebhookCreateResult is the response shape from createWebhookAction.
// `secret` is the plaintext HMAC key — surfaced ONCE on this
// response, never returned by subsequent list / patch reads. The
// modal must capture it before close.
export type WebhookCreateResult =
  | { ok: true; id: string; secret: string }
  | { ok: false; error: string };

// createWebhookAction mints a new webhook subscription. URL +
// optional event-type allowlist + optional description. The
// controller validates url scheme + event-type grammar; we trim
// inputs at the boundary but otherwise pass through verbatim so
// the operator sees the controller's diagnostic verbatim on a
// rejection.
export async function createWebhookAction(input: {
  url: string;
  eventTypes: string[];
  description: string;
}): Promise<WebhookCreateResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/webhooks`, {
      method: 'POST',
      headers: await buildHeaders(),
      body: JSON.stringify({
        url: input.url.trim(),
        eventTypes: input.eventTypes
          .map((e) => e.trim())
          .filter((e) => e.length > 0),
        description: input.description.trim(),
      }),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    const body = (await res.json()) as { id: string; secret: string };
    revalidatePath('/[locale]/webhooks', 'page');
    return { ok: true, id: body.id, secret: body.secret };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// updateWebhookAction sends a partial PATCH. Each field is optional
// so the UI can wire "toggle active" + "edit URL" + "edit event
// types" through one action.
export async function updateWebhookAction(
  id: string,
  patch: {
    url?: string;
    eventTypes?: string[];
    description?: string;
    active?: boolean;
  },
): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/webhooks/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: await buildHeaders(),
      body: JSON.stringify(patch),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/webhooks', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// deleteWebhookAction removes a subscription. Idempotent at the
// controller (re-deleting still returns 204) so the UI doesn't
// have to guard against double-clicks.
export async function deleteWebhookAction(id: string): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/webhooks/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/webhooks', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// WebhookTestResult mirrors the controller's apiTestWebhookResponse.
// `delivered` is the headline; `status` is the receiver's HTTP code
// (0 when the connection never completed); `error` is the transport-
// level message on connection failure. The UI uses all three:
// delivered=true → green check, delivered=false + status>0 → "got
// {status}" amber badge, delivered=false + status=0 → "{error}" red
// banner.
export type WebhookTestResult =
  | { ok: true; delivered: boolean; status: number; error?: string }
  | { ok: false; error: string };

// testWebhookAction fires a synthetic POST to the subscription's
// receiver via the controller's /test endpoint. Synchronous — the
// admin gets immediate yes/no feedback rather than having to scrape
// audit logs after the fact.
export async function testWebhookAction(id: string): Promise<WebhookTestResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/webhooks/${encodeURIComponent(id)}/test`, {
      method: 'POST',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    const body = (await res.json()) as {
      delivered: boolean;
      status: number;
      error?: string;
    };
    return { ok: true, delivered: body.delivered, status: body.status, error: body.error };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}
