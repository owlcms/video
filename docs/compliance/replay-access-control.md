# Replay Access Control — Compliance Specification

| | |
| --- | --- |
| Status | Proposed |
| Applies to | OWLCMS (accounts mode) and OWLCMS Video (Replays module) |
| Mode | Optional, intended for high-profile events |
| Default behaviour | Unchanged (no access control) when the mode is disabled |

## 1. Purpose

Ensure that jury replays and the lift video archive served by OWLCMS Video can only be viewed by people who have authenticated to OWLCMS with an account explicitly authorized for replays. Unauthenticated parties on the competition network ("aliens") must not be able to view replays, list sessions, download videos, or receive live replay notifications.

## 2. Scope

In scope:

- Every HTTP route served by the Replays module: `/`, `/static/*`, `/videos/*`, `/api/sessions`, `/api/sessions/{session}/lifts`, `/api/replay-state`, `/ws`, `/replay/{n}`, `/replay/{n}.mp4`.
- The machine-to-machine route `/api/cameras/config` used when Cameras and Replays run on separate machines.
- The OWLCMS login and post-login handoff for replay accounts.

Out of scope:

- The MQTT channel between OWLCMS and OWLCMS Video.
- Camera streams on the network (multicast/unicast).
- Physical access to the video machine and its file system.
- OWLCMS deployments in PIN mode. This mode requires accounts mode.

## 3. Threat model

| Actor | Capability | Addressed |
| --- | --- | --- |
| Spectator, coach or team member on venue Wi‑Fi | Browses to the video machine's address | Yes |
| Person reusing a replay URL from history, screenshot or bookmark | Replays a previously seen URL | Yes |
| Person sitting at the video machine using its browser | Local (loopback) access | Yes — no local exemption |
| Attacker passively capturing LAN traffic in real time | Reads plain HTTP traffic | No — see residual risks |
| Attacker with file-system access to the video machine | Reads configuration and video files | No — out of scope |

## 4. Roles and accounts

- **R-1** A new base role `REPLAYS` is added to OWLCMS. It is grantable and platform-scopable.
- **R-2** `REPLAYS` is not included in the expansion of `COMPETITION_DIRECTOR`. It is included in `ADMIN` (all base roles).
- **R-3** One replays account is provisioned per platform, scoped to that platform.
- **R-4** An account whose only explicit role is `REPLAYS` lands on the video module after login. Accounts with additional roles keep their normal landing page.

## 5. Shared secret

- **S-1** OWLCMS generates a pairing secret of at least 256 bits from a cryptographically secure random source.
- **S-2** The secret is configured per video machine (per platform when each platform has its own video machine).
- **S-3** OWLCMS shows the secret only on an administrator-only configuration page, with a **Regenerate** action.
- **S-4** On the video machine the secret is stored in the configuration directory, readable only by the account running OWLCMS Video.
- **S-5** The secret is never transmitted over MQTT, never placed in a URL, and never written to any log.
- **S-6** Regenerating the secret invalidates all outstanding tokens and all existing sessions.

## 6. Arrival token

- **T-1** When OWLCMS accepts the password of a replays account, it records the acceptance time `t` (milliseconds since the Unix epoch, UTC).
- **T-2** OWLCMS redirects the same browser tab to `<videoUrl>/auth?a=<account>&p=<platform>&t=<t>&s=<signature>`, where `signature = base64url(HMAC-SHA256(secret, a|p|t))`.
- **T-3** OWLCMS only redirects to the configured video URL; it never accepts a redirect target from the request.
- **T-4** The video module accepts the token only if all of the following hold:
  - the signature is valid (constant-time comparison);
  - `now − 30 s ≤ t ≤ now + 5 s` on the video machine's clock;
  - the platform `p` matches the platform served by this video machine.
- **T-5** Accepted signatures are remembered in memory for 30 seconds and rejected if presented again (single use).
- **T-6** After acceptance, the video module redirects to `/` so the token does not remain in the address bar.

## 7. Session

- **C-1** On token acceptance, the video module sets a session cookie whose value is `a|p|HMAC-SHA256(secret, "session"|a|p|t)`.
- **C-2** The cookie has no `Expires` or `Max-Age` attribute; it ends when the browser is closed.
- **C-3** The cookie is `HttpOnly`, `SameSite=Lax`, `Path=/`. It is `Secure` when the video module is served over HTTPS.
- **C-4** Every route listed in §2, except `/auth` and `/api/cameras/config`, requires a valid session cookie. There is no exemption for loopback or any IP address.
- **C-5** A request without a valid cookie receives HTTP 403 with a page instructing the user to log in through OWLCMS with the replays account. No video, session list or replay state is disclosed.
- **C-6** A `/ws` upgrade request without a valid cookie is refused before the upgrade.
- **C-7** *(Optional)* Top-level document requests other than `/auth` are refused when `Sec-Fetch-Site: none` (typed or bookmarked URLs). Enable only after verifying that browser reload does not trigger the refusal.

## 8. Machine-to-machine access

- **M-1** `/api/cameras/config` requires a request header containing an HMAC of the request derived from the shared secret. It does not accept the session cookie.

## 9. Clock synchronization

- **K-1** The OWLCMS and video machines must be synchronized with NTP. This is already required for replay trimming, which relies on the clock and decision times sent by OWLCMS.

## 10. Logging and audit

- **L-1** OWLCMS records each replay handoff in its audit log: account, platform, time, client IP.
- **L-2** The video module logs each `/auth` outcome (accepted / rejected with reason), account, platform and client IP.
- **L-3** The video module logs refused requests with path and client IP, rate-limited to prevent log flooding.
- **L-4** Neither component logs the secret, the token signature, or the cookie value.

## 11. Operational procedures

| Event | Action |
| --- | --- |
| Event setup | Enable the mode, generate the secret in OWLCMS, paste it into the video configuration, create one replays account per platform. |
| Lost or borrowed device | Regenerate the secret and update the video configuration; all sessions end. |
| Account no longer authorized | Disable the account in OWLCMS. Existing sessions end when the browser is closed; regenerate the secret for immediate effect. |
| Browser closed or cookies cleared | Log in again to OWLCMS with the replays account. |
| End of event | Regenerate or remove the secret; disable replays accounts. |

## 12. Residual risks

| Risk | Rating | Mitigation |
| --- | --- | --- |
| Session cookie or token captured by real-time traffic capture on plain HTTP | Medium | Put jury and replay machines on a wired or isolated network, or serve over HTTPS. |
| Disabling an account does not end sessions already open | Low | Session cookie ends with the browser; regenerate the secret for immediate revocation. |
| Secret disclosed from the video machine | Low | Restrictive file permissions; regenerate the secret. Such access already exposes the video files. |
| Clock drift beyond the token window | Low | NTP is already mandatory for replays; tolerance of +5 s for skew. |
| Bookmarked page reopened in a browser that still holds the session | Low | Optional control C-7. |

## 13. Verification checklist

- [ ] Direct access to `/` without a cookie returns 403, including from the video machine itself.
- [ ] Direct access to `/videos/<file>`, `/replay/1.mp4`, `/api/sessions` and `/ws` without a cookie is refused.
- [ ] Login with a replays account lands on the video page in the same tab.
- [ ] Login with an account lacking `REPLAYS` does not produce a token.
- [ ] A token older than 30 seconds is rejected.
- [ ] A token presented twice within 30 seconds is rejected the second time.
- [ ] A token with a modified account, platform or time is rejected.
- [ ] A token for another platform is rejected.
- [ ] The address bar shows no token after arrival.
- [ ] Closing and reopening the browser requires a new OWLCMS login.
- [ ] Regenerating the secret invalidates existing sessions.
- [ ] `/api/cameras/config` is refused without the header and accepted with it.
- [ ] Logs contain no secret, signature or cookie value.
- [ ] With the mode disabled, behaviour is identical to previous releases.
