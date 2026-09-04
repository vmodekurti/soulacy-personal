#!/usr/bin/env node
/*
 * Experimental WhatsApp Web sidecar for Soulacy.
 *
 * This uses Baileys, an unofficial WhatsApp Web library. It is intentionally
 * shipped as a sidecar rather than linked into the official WhatsApp Cloud API
 * channel. Install dependency in the runtime environment:
 *
 *   npm install @whiskeysockets/baileys
 *
 * The process speaks newline-delimited JSON:
 *   stdout events:  {type:"qr"|"status"|"message"|"error", ...}
 *   stdin commands: {type:"send", to:"<jid>", text:"..."}
 */

import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline';

function arg(name, fallback = '') {
  const i = process.argv.indexOf(name);
  return i >= 0 && i + 1 < process.argv.length ? process.argv[i + 1] : fallback;
}

const sessionDir = arg('--session-dir', path.join(process.cwd(), '.soulacy', 'whatsapp-web'));
const accountID = arg('--account-id', 'default');
const channelID = arg('--channel-id', 'whatsapp_web');
const authDir = path.join(sessionDir, accountID);

function emit(event) {
  process.stdout.write(JSON.stringify(event) + '\n');
}

function textFromMessage(message) {
  if (!message) return '';
  return (
    message.conversation ||
    message.extendedTextMessage?.text ||
    message.imageMessage?.caption ||
    message.videoMessage?.caption ||
    ''
  );
}

async function main() {
  fs.mkdirSync(authDir, { recursive: true });

  let baileys;
  try {
    baileys = await import('@whiskeysockets/baileys');
  } catch (error) {
    emit({
      type: 'error',
      detail: 'Missing @whiskeysockets/baileys. Install it with: npm install @whiskeysockets/baileys',
      error: String(error?.message || error),
    });
    process.exitCode = 1;
    return;
  }

  const makeWASocket = baileys.default?.default || baileys.default || baileys.makeWASocket;
  const useMultiFileAuthState = baileys.useMultiFileAuthState || baileys.default?.useMultiFileAuthState;
  const DisconnectReason = baileys.DisconnectReason || baileys.default?.DisconnectReason || {};
  const jidNormalizedUser = baileys.jidNormalizedUser || baileys.default?.jidNormalizedUser
    || ((jid) => String(jid || '').replace(/:\d+(?=@)/, ''));

  if (!makeWASocket || !useMultiFileAuthState) {
    emit({ type: 'error', detail: 'Unsupported Baileys export shape. Update @whiskeysockets/baileys.' });
    process.exitCode = 1;
    return;
  }

  let sock;

  // Ids of messages WE sent — used to tell the user's own typing in the
  // "Message yourself" chat apart from the agent's replies echoing back
  // (both arrive with fromMe=true). Without this, self-chat support would
  // loop: agent reply → upsert → agent reply…
  const sentIds = new Set();
  function rememberSent(id) {
    if (!id) return;
    sentIds.add(id);
    if (sentIds.size > 500) {
      for (const old of sentIds) {
        sentIds.delete(old);
        if (sentIds.size <= 250) break;
      }
    }
  }

  async function connect() {
    // Auth state is (re-)loaded on every connect so a cleared session dir
    // yields a FRESH registration — that's what makes Baileys emit a QR.
    const { state, saveCreds } = await useMultiFileAuthState(authDir);
    sock = makeWASocket({
      auth: state,
      printQRInTerminal: true,
      browser: ['Soulacy', 'Chrome', '1.0.0'],
      markOnlineOnConnect: false,
    });

    sock.ev.on('creds.update', saveCreds);

    sock.ev.on('connection.update', (update) => {
      const { connection, lastDisconnect, qr } = update;
      if (qr) {
        emit({ type: 'qr', value: qr, detail: 'scan QR with WhatsApp Linked Devices' });
      }
      if (connection === 'open') {
        emit({ type: 'status', connected: true, detail: `connected (${channelID})` });
      }
      if (connection === 'close') {
        const code = lastDisconnect?.error?.output?.statusCode;
        const loggedOut = code === DisconnectReason.loggedOut;
        if (loggedOut) {
          // Stale/unlinked credentials permanently block pairing: WhatsApp
          // refuses them and Baileys never shows a QR while they exist.
          // Wipe the session and reconnect fresh so a new QR appears.
          emit({ type: 'status', connected: false, detail: 'logged out — clearing stale session, generating a new QR…' });
          try {
            fs.rmSync(authDir, { recursive: true, force: true });
            fs.mkdirSync(authDir, { recursive: true });
          } catch (error) {
            emit({ type: 'error', detail: 'failed to clear stale session dir: ' + authDir, error: String(error?.message || error) });
            return;
          }
          setTimeout(() => { connect().catch(reportCrash); }, 1500);
        } else {
          emit({ type: 'status', connected: false, detail: 'disconnected; reconnecting' });
          setTimeout(() => { connect().catch(reportCrash); }, 3000);
        }
      }
    });

    const bare = (jid) => jidNormalizedUser(String(jid || '')).split('@')[0];

    sock.ev.on('messages.upsert', ({ messages, type }) => {
      if (type !== 'notify') {
        process.stderr.write(`upsert type=${type} ignored (${(messages || []).length} msgs)\n`);
        return;
      }
      // The "Message yourself" chat may be addressed by the phone jid
      // (@s.whatsapp.net) OR the linked identity (@lid) — match either.
      const me = [sock.user?.id, sock.user?.lid].filter(Boolean).map(bare);
      for (const msg of messages || []) {
        if (!msg.message) continue;
        if (msg.key?.fromMe) {
          // Own messages are ignored EXCEPT in the "Message yourself"
          // chat, which doubles as a private playground for the agent.
          // The agent's own replies (ids we sent) never re-trigger it.
          const chatBare = bare(msg.key?.remoteJid);
          const isSelfChat = chatBare !== '' && me.includes(chatBare);
          if (!isSelfChat || sentIds.has(msg.key?.id)) {
            process.stderr.write(`skip fromMe: chat=${msg.key?.remoteJid} me=[${me.join(',')}] sentEcho=${sentIds.has(msg.key?.id)}\n`);
            continue;
          }
        }
        const text = textFromMessage(msg.message).trim();
        if (!text) continue;
        emit({
          type: 'message',
          id: msg.key?.id || '',
          chat_id: msg.key?.remoteJid || '',
          sender_id: msg.key?.participant || msg.key?.remoteJid || '',
          sender_name: msg.pushName || '',
          text,
          timestamp: Number(msg.messageTimestamp || 0),
          is_group: String(msg.key?.remoteJid || '').endsWith('@g.us'),
        });
      }
    });
  }

  function reportCrash(error) {
    emit({ type: 'error', detail: 'reconnect failed', error: String(error?.message || error) });
    process.exitCode = 1;
  }

  await connect();

  // Typing indicators: WhatsApp expires "composing" after ~10s, so while
  // the agent generates we refresh it every 8s until the reply is sent.
  const typingTimers = new Map(); // jid → interval
  function stopTyping(jid) {
    const t = typingTimers.get(jid);
    if (t) {
      clearInterval(t);
      typingTimers.delete(jid);
    }
  }
  async function setTyping(jid, state) {
    if (!jid) return;
    stopTyping(jid);
    try {
      if (state === 'composing') {
        await sock.sendPresenceUpdate('composing', jid);
        typingTimers.set(jid, setInterval(() => {
          sock.sendPresenceUpdate('composing', jid).catch(() => {});
        }, 8000));
      } else {
        await sock.sendPresenceUpdate('paused', jid);
      }
    } catch {
      // cosmetic — never let presence failures break messaging
    }
  }

  const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
  rl.on('line', async (line) => {
    let cmd;
    try {
      cmd = JSON.parse(line);
    } catch {
      emit({ type: 'error', detail: 'invalid command JSON' });
      return;
    }
    if (cmd.type === 'typing') {
      await setTyping(cmd.to, cmd.state || 'composing');
      return;
    }
    if (cmd.type !== 'send') return;
    try {
      await setTyping(cmd.to, 'paused');
      const sent = await sock.sendMessage(cmd.to, { text: String(cmd.text || '') });
      rememberSent(sent?.key?.id);
    } catch (error) {
      emit({ type: 'error', detail: 'send failed', error: String(error?.message || error) });
    }
  });
}

main().catch((error) => {
  emit({ type: 'error', detail: 'sidecar crashed', error: String(error?.message || error) });
  process.exitCode = 1;
});
