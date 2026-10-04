// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import {
  deskCloseTermAction,
  deskMsgAction,
  deskStartTermAction,
  deskStatusAction,
  deskTermInAction,
} from '@/lib/actions';
import type { DeskActions, DeskCall } from './DeviceDesk';

// deviceDeskActions binds the peers drawer to the controller desk and
// to the Next routes that proxy files and the terminal stream.
export function deviceDeskActions(peerID: string): DeskActions {
  return {
    status: () => deskStatusAction(peerID),
    sendMsg: (text) => deskMsgAction(peerID, text),
    startTerm: () => deskStartTermAction(peerID),
    termIn: (opId, data) => deskTermInAction(peerID, opId, data),
    closeTerm: () => deskCloseTermAction(peerID),
    termOutURL: (opId) =>
      `/api/peers/${encodeURIComponent(peerID)}/desk/term/out?op=${encodeURIComponent(opId)}`,
    putFile: (path, file) => postFile(peerID, path, file),
    getFile: (path) => downloadFile(peerID, path),
  };
}

async function postFile(peerID: string, path: string, file: File): Promise<DeskCall> {
  const url = `/api/peers/${encodeURIComponent(peerID)}/desk/put?path=${encodeURIComponent(path)}`;
  const res = await fetch(url, { method: 'POST', body: file });
  if (!res.ok) return { ok: false, error: await res.text() };
  return { ok: true };
}

async function downloadFile(peerID: string, path: string): Promise<DeskCall> {
  const url = `/api/peers/${encodeURIComponent(peerID)}/desk/get?path=${encodeURIComponent(path)}`;
  const res = await fetch(url);
  if (!res.ok) return { ok: false, error: await res.text() };
  const blob = await res.blob();
  const objectURL = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectURL;
  anchor.download = path.split('/').pop() || 'download';
  anchor.click();
  URL.revokeObjectURL(objectURL);
  return { ok: true };
}
