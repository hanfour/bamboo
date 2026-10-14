// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

export type DeskStatus = { ok: true; online: boolean; execEnabled: boolean } | { ok: false; error: string };
export type DeskCall = { ok: true } | { ok: false; error: string };
export type DeskStart = { ok: true; opId: string } | { ok: false; error: string };

// DeskActions is the browser side of the device desk. Tests pass fakes;
// the peers drawer passes the controller-backed implementation.
export type DeskActions = {
  status: () => Promise<DeskStatus>;
  sendMsg: (text: string) => Promise<DeskCall>;
  putFile: (path: string, file: File) => Promise<DeskCall>;
  getFile: (path: string) => Promise<DeskCall>;
  startTerm: () => Promise<DeskStart>;
  termIn: (opId: string, data: string) => Promise<DeskCall>;
  closeTerm: () => Promise<DeskCall>;
  termOutURL: (opId: string) => string;
};

const statusRefreshMs = 15_000;

const buttonClass =
  'rounded border border-bamboo-50 bg-bamboo-50 px-2 text-xs font-medium text-ink-950 transition-colors hover:bg-ink-800 disabled:opacity-50';
const fieldClass =
  'w-full rounded border border-ink-800 bg-transparent px-2 py-1 font-mono text-xs text-bamboo-100';

export function DeviceDesk({
  meshIP,
  peerKey,
  actions,
}: {
  meshIP: string;
  peerKey: string;
  actions: DeskActions;
}) {
  const t = useTranslations('peers.drawer.desk');
  const closedLabel = t('closed');
  const droppedLabel = t('streamClosed');
  const [status, setStatus] = useState<DeskStatus | null>(null);
  const [message, setMessage] = useState('');
  const [path, setPath] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [output, setOutput] = useState('');
  const [opId, setOpId] = useState<string | null>(null);
  const [line, setLine] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      void actions.status().then((result) => {
        if (!cancelled) setStatus(result);
      });
    };
    load();
    const timer = setInterval(load, statusRefreshMs);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [actions]);

  useEffect(() => {
    if (!opId || typeof EventSource === 'undefined') return;
    const source = new EventSource(actions.termOutURL(opId));
    // The controller closes the response after `exit`. EventSource treats
    // that as a failure and reconnects unless we close first. A later
    // error from that close is not a dropped shell.
    let stopped = false;
    const stop = () => {
      stopped = true;
      source.close();
    };
    const onData = (ev: Event) => {
      const data = (ev as MessageEvent<string>).data;
      setOutput((prev) => prev + decodeDeskChunk(data));
    };
    const onExit = () => {
      stop();
      setOutput((prev) => `${prev}\n[${closedLabel}]\n`);
      setOpId(null);
    };
    source.addEventListener('data', onData);
    source.addEventListener('exit', onExit);
    source.onerror = () => {
      if (stopped) return;
      stop();
      setError(droppedLabel);
    };
    return () => stop();
  }, [actions, opId, closedLabel, droppedLabel]);

  const online = status?.ok === true && status.online;
  const execOn = status?.ok === true && status.execEnabled;
  const mesh = meshCommand(meshIP, peerKey);

  async function run(fn: () => Promise<DeskCall | DeskStart | void>) {
    setPending(true);
    setError(null);
    try {
      const result = await fn();
      if (result && 'ok' in result && !result.ok) setError(result.error);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-sm text-bamboo-200/80">{t('hint')}</p>
      <p className="text-sm text-bamboo-200/80">{t('execHint')}</p>
      <p className="text-xs text-bamboo-100">
        {status == null
          ? t('checking')
          : status.ok
            ? status.online
              ? status.execEnabled
                ? t('execOn')
                : t('execOff')
              : t('offline')
            : status.error}
      </p>
      <button
        type="button"
        className={buttonClass}
        onClick={() => {
          void actions.status().then(setStatus);
        }}
      >
        {t('refresh')}
      </button>

      {mesh && (
        <div>
          <p className="mb-1 text-xs text-bamboo-200/70">{t('mesh')}</p>
          <code className="block break-all rounded border border-ink-800 px-2 py-1 font-mono text-xs text-bamboo-100">
            {mesh}
          </code>
        </div>
      )}

      <form
        className="flex gap-2"
        onSubmit={(ev) => {
          ev.preventDefault();
          const text = message;
          void run(async () => {
            const result = await actions.sendMsg(text);
            if (result.ok) setMessage('');
            return result;
          });
        }}
      >
        <input
          aria-label={t('message')}
          className={fieldClass}
          value={message}
          onChange={(ev) => setMessage(ev.target.value)}
          placeholder={t('message')}
        />
        <button type="submit" className={buttonClass} disabled={pending || message.trim() === ''}>
          {t('send')}
        </button>
      </form>

      <div className="space-y-2">
        <p className="text-xs text-bamboo-200/70">{t('fileCap')}</p>
        <input
          aria-label={t('path')}
          className={fieldClass}
          value={path}
          onChange={(ev) => setPath(ev.target.value)}
          placeholder={t('path')}
        />
        <input
          aria-label={t('upload')}
          type="file"
          className="text-xs text-bamboo-100"
          onChange={(ev) => setFile(ev.target.files?.[0] ?? null)}
        />
        <div className="flex gap-2">
          <button
            type="button"
            className={buttonClass}
            disabled={pending || !file || path.trim() === ''}
            onClick={() => {
              if (!file) return;
              void run(() => actions.putFile(path.trim(), file));
            }}
          >
            {t('upload')}
          </button>
          <button
            type="button"
            className={buttonClass}
            disabled={pending || path.trim() === ''}
            onClick={() => {
              void run(() => actions.getFile(path.trim()));
            }}
          >
            {t('download')}
          </button>
        </div>
      </div>

      <div className="space-y-2">
        <div className="flex gap-2">
          <button
            type="button"
            className={buttonClass}
            disabled={pending || !online || !execOn || opId != null}
            onClick={() => {
              void run(async () => {
                const result = await actions.startTerm();
                if (result.ok) {
                  setOutput('');
                  setOpId(result.opId);
                }
                return result;
              });
            }}
          >
            {t('start')}
          </button>
          <button
            type="button"
            className={buttonClass}
            disabled={pending || opId == null}
            onClick={() => {
              void run(async () => {
                const result = await actions.closeTerm();
                if (result.ok) setOpId(null);
                return result;
              });
            }}
          >
            {t('close')}
          </button>
        </div>
        <pre
          aria-label={t('terminal')}
          className="max-h-64 overflow-auto whitespace-pre-wrap rounded border border-ink-800 bg-ink-950 p-2 font-mono text-xs text-bamboo-50"
        >
          {output || t('noOutput')}
        </pre>
        <form
          className="flex gap-2"
          onSubmit={(ev) => {
            ev.preventDefault();
            if (!opId) return;
            const text = line;
            void run(async () => {
              const result = await actions.termIn(opId, `${text}\n`);
              if (result.ok) setLine('');
              return result;
            });
          }}
        >
          <input
            aria-label={t('input')}
            className={fieldClass}
            value={line}
            disabled={opId == null}
            onChange={(ev) => setLine(ev.target.value)}
            placeholder={t('input')}
          />
          <button type="submit" className={buttonClass} disabled={pending || opId == null || line === ''}>
            {t('send')}
          </button>
        </form>
      </div>
      {error && <p className="text-sm text-red-400">{error}</p>}
    </div>
  );
}

function meshCommand(ip: string, peerKey: string): string {
  const host = ip.trim();
  const key = peerKey.trim();
  if (host === '' || key === '') return '';
  const addr = host.includes(':') ? `[${host}]` : host;
  return `bamboo term ${addr}:54110 --peer-key ${key}`;
}

function decodeDeskChunk(data: string): string {
  try {
    const body = JSON.parse(data) as { data?: string };
    if (!body.data) return '';
    const binary = atob(body.data);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
    return new TextDecoder().decode(bytes);
  } catch {
    return data;
  }
}
