/*
 * Advisory client-side mirror of the Go capability classifier
 * (internal/studio/codeclass). Gives the author LIVE "needs: system/network"
 * chips while typing a Custom Python block, without a backend round-trip.
 *
 * This is ADVISORY ONLY — the authoritative classification (and the per-case
 * consent gate) runs server-side at plan/save. Keep the patterns in rough sync
 * with codeclass.go, but a drift here can never weaken the gate; it only changes
 * what the hint shows.
 */

const NET = /\b(socket|ssl|http\.client|httplib|urllib|urllib2|urllib3|requests|httpx|aiohttp|websocket|websockets|ftplib|smtplib|imaplib|poplib|telnetlib|paramiko|grpc)\b/
const SYS = /(\bsubprocess\b|\bos\.system\b|\bos\.popen\b|\bos\.exec[lv]|\bos\.spawn|\bos\.fork\b|\bpty\b|\bshutil\b|\bos\.remove\b|\bos\.unlink\b|\bos\.rmdir\b|\bos\.removedirs\b|\bos\.rename\b|\bos\.replace\b|\bos\.mkdir\b|\bos\.makedirs\b|\bos\.chmod\b|\bos\.chown\b|\bctypes\b|\bmmap\b|\bsignal\b)/
const WRITE = /open\s*\([^)]*,\s*['"][^'"]*[wax]/
const DYN = /(\beval\s*\(|\bexec\s*\(|\b__import__\s*\(|\bcompile\s*\(|\bimportlib\b|\bmarshal\b|\bpickle\.loads\b|\bgetattr\s*\()/

export function classifyCode(code) {
  if (!code) return { requires: [], dynamic: false }
  const requires = []
  if (SYS.test(code) || WRITE.test(code)) requires.push('system')
  if (NET.test(code)) requires.push('network')
  return { requires, dynamic: DYN.test(code) }
}
