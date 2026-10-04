// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DeviceDesk, type DeskActions } from './DeviceDesk';

vi.mock('next-intl', () => {
  const translate = (key: string) => key;
  return { useTranslations: () => translate };
});

function actions(overrides: Partial<DeskActions> = {}): DeskActions {
  return {
    status: vi.fn(async () => ({ ok: true as const, online: true, execEnabled: false })),
    sendMsg: vi.fn(async () => ({ ok: true as const })),
    putFile: vi.fn(async () => ({ ok: true as const })),
    getFile: vi.fn(async () => ({ ok: true as const })),
    startTerm: vi.fn(async () => ({ ok: true as const, opId: 'op-1' })),
    termIn: vi.fn(async () => ({ ok: true as const })),
    closeTerm: vi.fn(async () => ({ ok: true as const })),
    termOutURL: (opId: string) => `/api/peers/p/desk/term/out?op=${opId}`,
    ...overrides,
  };
}

describe('DeviceDesk', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('shows the mesh command and keeps the terminal off when exec is disabled', async () => {
    const desk = actions();
    render(<DeviceDesk meshIP="100.64.0.8" peerKey="pubkey" actions={desk} />);
    expect(await screen.findByText('execOff')).toBeInTheDocument();
    expect(screen.getByText('bamboo term 100.64.0.8:54110 --peer-key pubkey')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  it('sends a message and does not start a terminal while exec is off', async () => {
    const user = userEvent.setup();
    const desk = actions();
    render(<DeviceDesk meshIP="100.64.0.8" peerKey="pubkey" actions={desk} />);
    await screen.findByText('execOff');
    const message = screen.getByLabelText('message');
    await user.type(message, 'hello device');
    const form = message.closest('form');
    expect(form).not.toBeNull();
    await user.click(within(form as HTMLElement).getByRole('button', { name: 'send' }));
    expect(desk.sendMsg).toHaveBeenCalledWith('hello device');
    expect(desk.startTerm).not.toHaveBeenCalled();
  });

  it('shows offline without offering a terminal', async () => {
    const desk = actions({
      status: vi.fn(async () => ({ ok: true as const, online: false, execEnabled: false })),
    });
    render(<DeviceDesk meshIP="100.64.0.8" peerKey="pubkey" actions={desk} />);
    expect(await screen.findByText('offline')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  it('downloads the path the operator typed', async () => {
    const user = userEvent.setup();
    const desk = actions();
    render(<DeviceDesk meshIP="" peerKey="" actions={desk} />);
    await screen.findByText('execOff');
    await user.type(screen.getByLabelText('path'), 'notes/a.txt');
    await user.click(screen.getByRole('button', { name: 'download' }));
    expect(desk.getFile).toHaveBeenCalledWith('notes/a.txt');
  });

  it('closes the terminal stream on exit and ignores the follow-up error', async () => {
    const user = userEvent.setup();
    const sources: FakeEventSource[] = [];
    vi.stubGlobal(
      'EventSource',
      class extends FakeEventSource {
        constructor(url: string) {
          super(url);
          sources.push(this);
        }
      },
    );
    const desk = actions({
      status: vi.fn(async () => ({ ok: true as const, online: true, execEnabled: true })),
    });
    render(<DeviceDesk meshIP="100.64.0.8" peerKey="pubkey" actions={desk} />);
    expect(await screen.findByText('execOn')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'start' }));
    await waitFor(() => expect(sources).toHaveLength(1));
    const source = sources[0];
    expect(source.url).toBe('/api/peers/p/desk/term/out?op=op-1');
    source.emit('data', JSON.stringify({ data: btoa('hi') }));
    source.emit('exit');
    source.fail();
    expect(source.closed).toBe(true);
    const term = await screen.findByLabelText('terminal');
    await waitFor(() => {
      expect(term).toHaveTextContent('hi');
      expect(term).toHaveTextContent('closed');
    });
    expect(screen.queryByText('streamClosed')).not.toBeInTheDocument();
  });
});

class FakeEventSource {
  url: string;
  closed = false;
  private listeners = new Map<string, (ev: Event) => void>();
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, fn: (ev: Event) => void) {
    this.listeners.set(type, fn);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data = '') {
    this.listeners.get(type)?.({ data } as MessageEvent);
  }

  fail() {
    this.onerror?.();
  }
}
