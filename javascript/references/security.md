# Security playbook

Treat every byte from outside the process (body, query, headers, cookies, files, webhooks, env from users, third-party APIs) as hostile until validated.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `` exec(`convert ${file}`) `` | `execFile('convert', [file])` | shell injection |
| `` query(`… WHERE id = ${id}`) `` | `query('… WHERE id = $1', [id])` | SQL injection |
| `el.innerHTML = userText` | `el.textContent = userText` | XSS |
| `Math.random().toString(36)` as token | `crypto.randomUUID()` / `randomBytes(32)` | predictable |
| `signature === expected` | `timingSafeEqual` on equal-length digests | timing leak |
| `jwt.verify(token, key)` | `jwt.verify(token, key, { algorithms: ['RS256'] })` | algorithm confusion |
| `db.users.update(id, req.body)` | pick allowed fields after schema parse | mass assignment (`role: 'admin'`) |
| `merge(config, req.body)` | schema-validate, then `safeMerge` | prototype pollution |
| `res.status(500).json({ error: err.message })` | generic message + `requestId` | leaks internals |

## Input at the boundary

- Validate shape, types, lengths and ranges with a schema validator (e.g. zod, valibot, ajv). Reject unknown fields, or strip them.
- Never pass `req.body` straight into a DB update or model constructor (mass assignment: `{ role: 'admin' }`). Pick allowed fields explicitly.
- Limit body size and upload size; check file type by content, not extension; store uploads outside the web root.

## Injection

- SQL: parameterized queries / query builder bindings only. Identifiers (sort column) come from an allowlist.
- NoSQL: `{ email: req.body.email }` lets `{ "$gt": "" }` through. Validate it's a string first.
- Paths: `const p = path.resolve(base, input); if (!p.startsWith(base + path.sep)) throw ...`.
- SSRF: server-side fetch of user URLs → allowlist hosts/protocols, block private/link-local IPs (after DNS resolution), disable or re-check redirects.
- Never `eval`, `new Function`, `vm` (not a sandbox) or `setTimeout(string)` with dynamic data.

## Prototype pollution

Deep merge/set of user JSON can write `__proto__`/`constructor.prototype` and change every object in the process.

```js
const FORBIDDEN = new Set(['__proto__', 'constructor', 'prototype']);

export function safeMerge(target, source) {
  for (const key of Object.keys(source)) {
    if (FORBIDDEN.has(key)) continue;
    const value = source[key];
    target[key] = value && typeof value === 'object' && !Array.isArray(value)
      ? safeMerge(Object.hasOwn(target, key) && typeof target[key] === 'object' ? target[key] : {}, value)
      : value;
  }
  return target;
}
```

Better: validate against a schema first (unknown keys dropped), and use `Map` or `Object.create(null)` for user-keyed dictionaries. Read with `Object.hasOwn`, not `obj[key]` truthiness.

## XSS (browser and server-rendered HTML)

- Use `textContent`/framework escaping. `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `dangerouslySetInnerHTML`, `v-html` need sanitized HTML (e.g. DOMPurify).
- Validate URL protocols before `href`/`src` (`javascript:` URLs).
- CSP without `'unsafe-inline'`/`'unsafe-eval'` (use nonces/hashes); set `X-Content-Type-Options: nosniff`, `frame-ancestors`.

## Crypto & secrets

- Tokens/IDs/OTPs: `crypto.randomUUID()`, `randomBytes`, `randomInt`. Never `Math.random`.
- Compare secrets (API keys, HMAC signatures, tokens) in constant time:

```js
import { createHash, createHmac, timingSafeEqual } from 'node:crypto';

const digest = (s) => createHash('sha256').update(s).digest(); // equal length for timingSafeEqual
export const safeEqual = (a, b) => timingSafeEqual(digest(a), digest(b));

export const verifyWebhook = (rawBody, signature, secret) =>
  safeEqual(createHmac('sha256', secret).update(rawBody).digest('hex'), signature ?? '');
```

- Verify webhooks over the **raw** body, plus a timestamp window to block replays.
- Passwords: Argon2id (or scrypt) via async API; never fast hashes.
- JWT: pin `algorithms`, verify `exp`/`iss`/`aud`, keep access tokens short-lived; never accept `alg: none`. Don't put PII in payloads (base64 ≠ encryption).
- Browser sessions: `HttpOnly; Secure; SameSite=Lax|Strict` cookies, not localStorage; CSRF protection for cookie auth.
- Secrets from env/secret manager, validated at boot, never in code, logs, error messages, URLs or client bundles (`NEXT_PUBLIC_`/`VITE_` vars are public).

## Errors & logs

- Client gets `{ error: 'Internal error', requestId }`; logs get the stack. Never send `err.message` from DB/driver errors to clients.
- Logger redaction for `authorization`, `cookie`, `password`, `token`, card/ID numbers.
- Authorization checked per resource on the server (IDOR: `/orders/:id` must verify ownership), not just "logged in".

## Abuse

Rate-limit auth, OTP, search and expensive endpoints (per IP + per account); cap pagination `limit`; set request timeouts.

## Dependencies

Commit the lockfile, install with `npm ci`, run `npm audit --omit=dev` (or equivalent) in CI, prefer well-maintained packages over micro-deps, consider `--ignore-scripts` for installs, pin the Node version.

## Checklist

- [ ] All external input schema-validated; no mass assignment
- [ ] Queries parameterized; no shell/eval with input; paths/URLs checked
- [ ] Merges guarded against `__proto__`
- [ ] No unsanitized HTML sinks
- [ ] Secrets: CSPRNG, constant-time compare, not logged
- [ ] AuthZ per resource; rate limits on sensitive routes
