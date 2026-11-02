/** True for a URL served by this machine — the only thing the launch toolbar's DevTools button will hand to
 *  the browser. A tool's announced address comes from a running app's output, which is untrusted. */
export function isLoopbackUrl(raw: string): boolean {
  try {
    const url = new URL(raw)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return false
    const host = url.hostname.replace(/^\[|\]$/g, '')
    return host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '0.0.0.0'
  } catch {
    return false
  }
}
