# link-shortener-backend

A simple link shortening backend using google cloud datastore key/values for a fast and affordable service.

Sign in is passwordless: an allow-listed email address requests a magic link, clicking it starts a
session, and the session cookie slides forward every time you come back.

## How authentication works

1. `GET /login` serves the sign in page. It's rendered by this service - the html is embedded in the
   binary, so there is nothing else to deploy or keep in sync.
2. `POST /login` looks the address up in the `user` kind. Unknown or disabled addresses get exactly
   the same "check your email" response as real ones, so the form can't be used to find out who has
   an account.
3. A 32 byte token is generated. Only its SHA-256 hash is written to datastore (kind `magic_link`),
   so the token itself exists only in the email. Links last **15 minutes** and work **once**.
4. Resend delivers the email. The link points at `GET /auth/callback`, which renders a confirm
   button rather than signing you in outright - mail scanners and link previewers fetch urls out of
   email, and a bare GET would let them burn the token before you ever clicked it.
5. `POST /auth/callback` spends the token, signs a HS512 JWT and sets it as an http-only,
   `SameSite=Lax`, Secure cookie good for **30 days**.
6. Any authenticated request re-issues that cookie once it is more than an hour old, so an active
   user is never signed out. Someone who stays away for 30 days is.

The API also still accepts `Authorization: bearer <token>` for scripts.

## Routes

| Route                     | Auth      | What it does                                    |
|---------------------------|-----------|-------------------------------------------------|
| `GET /`                   | session   | Dashboard: create a short link, sign out        |
| `GET /login`              | -         | Sign in form                                    |
| `POST /login`             | -         | Emails a magic link                             |
| `GET /auth/callback`      | -         | Confirm step for an emailed link                |
| `POST /auth/callback`     | -         | Spends the token, starts the session            |
| `POST /logout`            | -         | Clears the session cookie                       |
| `GET /api/auth/session`   | session   | `{"email": "..."}` for the current session      |
| `GET /:short`             | -         | Public redirect, counts a click                 |
| `GET /link/:short`        | session   | Link details as json                            |
| `POST /link`              | session   | Creates a short link                            |

Static routes win over `/:short`, and short codes are upper-cased before lookup, so `/login` is the
login page while `/LOGIN` is still a perfectly good short code.

## Redirects and click counts

`GET /:short` counts the click **before** it writes the redirect, inside a datastore
transaction that re-reads the entity. It used to fire a goroutine after responding, which
loses counts two ways on Cloud Run: the container's cpu is throttled the moment the response
goes out, so the goroutine is often frozen before it reaches datastore, and it incremented a
copy of the link read outside the transaction, so simultaneous clicks overwrote each other.

The cost is one datastore round trip on the redirect path. If that ever matters more than
exact counts, the alternative is enabling always-on cpu for the service rather than going
back to a goroutine.

## Configuration

| Variable                | Required | Default             | Notes                                                    |
|-------------------------|----------|---------------------|----------------------------------------------------------|
| `DATASTORE_PROJECT_ID`  | yes      | -                   | Fatal if unset                                            |
| `DATASTORE_NAMESPACE`   | yes      | -                   | Fatal if unset. Also the JWT audience                     |
| `JWT_SIGNING_KEY`       | yes      | -                   | Fatal if unset. Rotating it signs everybody out           |
| `RESEND_API_KEY`        | yes      | -                   | Without it login is disabled and returns 503              |
| `MAIL_FROM`             | yes      | -                   | e.g. `links.ajn.me <login@ajn.me>`, on a verified domain  |
| `APP_BASE_URL`          | yes      | `https://links.ajn.me` | Public origin. Magic links are built from it           |
| `SITE_NAME`             | no       | `links.ajn.me`      | Name shown on the pages and in the login email            |
| `SESSION_COOKIE_NAME`   | no       | `ls_session`        |                                                           |
| `COOKIE_DOMAIN`         | no       | empty (host-only)   | Only set this to share the session across subdomains      |
| `COOKIE_SECURE`         | no       | `true`              | Set `false` only for plain http local development         |
| `PORT`                  | no       | `8080`              | Set by Cloud Run                                          |
| `mode`                  | no       | -                   | `debug` keeps gin in debug mode                           |

`TEMP_PASS` is gone. The `POST /token` endpoint it guarded has been removed - it handed out a token
to anyone who sent a matching `password` header, and when `TEMP_PASS` was unset the empty header
matched the empty value, so it authenticated everybody. Remove it from the Cloud Run service.

## Datastore

Three kinds, all in `DATASTORE_NAMESPACE`:

- **`link`** - unchanged. Key name is the upper-cased short code.
- **`user`** - the allow list. Key name is the lower-cased email address. Having an entity is what
  grants access.
- **`magic_link`** - pending login tokens. Key name is the SHA-256 hash of the emailed token.

All three are looked up by key, so no composite indexes are needed.

### Allowing someone to sign in

In the console, under the right namespace, create an entity of kind `user` with the **key name set
to the lower-cased email address** (`someone@example.com`). Optional properties the service
understands:

| Property       | Type    | Meaning                                                  |
|----------------|---------|----------------------------------------------------------|
| `Email`        | string  | Informational copy of the address                         |
| `Created`      | integer | Unix seconds, informational                               |
| `LastLogin`    | integer | Unix seconds, written on every successful sign in         |
| `LastLinkSent` | integer | Unix seconds, used to throttle links to one per minute    |
| `Disabled`     | boolean | `true` blocks sign in without deleting the entity         |

Extra properties are left alone: logins update one property at a time rather than overwriting the
entity. To revoke access, delete the entity or set `Disabled` to true - existing sessions keep
working until the cookie expires unless you also rotate `JWT_SIGNING_KEY`.

### Cleaning up spent tokens

`magic_link` entities carry an `Expires` timestamp. Turn on a TTL policy so they sweep themselves:

```bash
gcloud firestore fields ttls update Expires --collection-group=magic_link --enable-ttl
```

Nothing breaks without it - spent and expired tokens are already rejected - the kind just grows.

## Resend

1. Verify the sending domain in Resend and add the DNS records it asks for.
2. Create an API key with send permission, put it in `RESEND_API_KEY`.
3. Set `MAIL_FROM` to an address on that verified domain.

Email is sent inline during the request. Cloud Run only guarantees cpu while a request is being
handled, so it must not be moved to a background goroutine.

## Local development

```bash
export DATASTORE_PROJECT_ID=... DATASTORE_NAMESPACE=... JWT_SIGNING_KEY=dev-key
export RESEND_API_KEY=... MAIL_FROM='Dev <you@example.com>'
export APP_BASE_URL=http://localhost:8080 COOKIE_SECURE=false mode=debug
go run ./cmd/link-shortener-backend
```

```bash
go test ./...
```
