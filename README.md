| `PATCH /link/:short`      | session   | Changes the destination, the short code, the tags, or any of them |
| `DELETE /link/:short`     | session   | Deletes a link                                      |
| `GET /tags`               | session   | Tags page: name and colour your labels              |
| `GET /api/tags`           | session   | Your tags as json                                   |
| `POST /tags`              | session   | Creates a tag                                       |
| `PATCH /tags/:name`       | session   | Renames a tag, recolours it, or both                |
| `DELETE /tags/:name`      | session   | Deletes a tag and takes it off your links           |
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

## Rate limits

There is no captcha, because the thing a captcha would guard is already shut. `POST /login` only
mails an address that is in the `user` kind, and that check happens *before* anything is sent, so a
bot working through guessed addresses gets the same "check your email" page as everybody else and
costs nothing but a datastore read. Nothing is emailed to a stranger, ever.

What is left to bound is a caller who knows, or guesses, an address that really is on the list.
Four limits stack up:

| Limit                        | Scope              | Where it lives              |
|------------------------------|--------------------|-----------------------------|
| One link per **60 seconds**  | Per email address  | `LastLinkSent` in datastore |
| **5** links per hour         | Per email address  | `RecentLinkSents` in datastore |
| **15** links per day         | Per email address  | `RecentLinkSents` in datastore |
| **10** posts per minute to `/login`, **20** to `/auth/callback` | Per client address | In memory, per instance |

The per address caps are the ones that bound the email bill: whatever happens, this service cannot
send more than `users x 15` login emails in a day. They are counted from datastore, so they hold
across every running instance. Being capped returns the same page as a link going out, so the form
still cannot be used to work out which addresses are real.

The per address caps have one gap worth knowing about: the check reads the user, then the email is
sent, then the send is recorded. Two requests for the same address arriving at the same instant can
both pass the check. That is bounded by how many land together rather than by the cap, and the
sixty second throttle keeps it small, but it is not a hard ceiling.

The per connection limits are a brake on floods, not a promise: they live in memory, so with several
instances up the real ceiling is the limit times the instance count. They protect datastore reads and
instance time rather than the email budget. A caller over one gets a 429 with `Retry-After`. Requests
whose origin cannot be established are **not** limited - see `TRUSTED_PROXY_DEPTH` above, and
`internal/ratelimit` for why guessing at an identity is worse than not having one.

## Routes

| Route                     | Auth      | What it does                                       |
|---------------------------|-----------|----------------------------------------------------|
| `GET /`                   | session   | Dashboard: create, list, edit, tag and delete links. `?tag=` filters |
| `GET /login`              | -         | Sign in form                                        |
| `POST /login`             | -         | Emails a magic link                                 |
| `GET /auth/callback`      | -         | Confirm step for an emailed link                    |
| `POST /auth/callback`     | -         | Spends the token, starts the session                |
| `POST /logout`            | -         | Clears the session cookie                           |
| `GET /api/auth/session`   | session   | `{"email": "..."}` for the current session          |
| `GET /:short`             | -         | Public redirect, counts a click. Unknown code redirects to `/` |
| `GET /links`              | session   | Your links as json. Admins add `?all=1` for everyone's. `?tag=` filters |
| `GET /link/:short`        | session   | Link details as json                                |
| `POST /link`              | session   | Creates a short link                                |
| `PATCH /link/:short`      | session   | Changes the destination, the short code, the tags, or any of them |
| `DELETE /link/:short`     | session   | Deletes a link                                      |
| `GET /tags`               | session   | Tags page: name and colour your labels              |
| `GET /api/tags`           | session   | Your tags as json                                   |
| `POST /tags`              | session   | Creates a tag                                       |
| `PATCH /tags/:name`       | session   | Renames a tag, recolours it, or both                |
| `DELETE /tags/:name`      | session   | Deletes a tag and takes it off your links           |
| `GET /users`              | admin     | Access page: who can sign in, and add someone       |
| `POST /users`             | admin     | Adds an address to the allow list                   |
| `PATCH /users/:email`     | admin     | Changes somebody's role, or blocks them signing in |

Static routes win over `/:short`, and short codes are upper-cased before lookup, so `/login` is the
login page while `/LOGIN` is still a perfectly good short code. Short codes that would collide with
a route the service serves itself - `link`, `links`, `user`, `users`, `tag`, `tags`, `login`,
`logout`, `auth`, `api`, `static` - are rejected at creation, along with anything outside
`A-Z a-z 0-9 - _`.

## Who can change what

Every link records the address that created it in `CreatedBy`. That is the ownership record.

- A normal user sees only their own links, and can only edit or delete their own. Tags are theirs
  outright: an admin does not get to reach into somebody else's set.
- An **admin** sees their own by default and everybody's on request, can edit or delete anything,
  and runs the Access page: adding users, changing roles, and blocking people. Make somebody an
  admin by ticking the box when adding them, from their row on the Access page, or by setting
  `Admin` to true on their `user` entity.

Asking to change a link that exists but belongs to somebody else returns **404, not 403**, for a
normal user - confirming that a code exists but is not yours is more than the answer needs to give
away. Admins get a real 403, since they can already see everything.

Ownership is checked **inside** the datastore transaction that does the write, so it cannot be
raced: a link cannot change hands between the check and the update.

Two consequences worth knowing:

- **Links created before magic-link login have `CreatedBy` set to whatever the old `/token`
  endpoint hardcoded.** If you sign in as a different address, those links are not yours and will
  not appear in your list. Sign in as that address, make yourself an admin, or rewrite the
  `CreatedBy` property on the entities.
- **Links with an empty `CreatedBy` belong to nobody** and are only reachable by an admin.

### Renaming a link

`PATCH` accepts a new `short`, which moves the entity, because the short code *is* the datastore
key. Clicks and the original creation details come along. The old code stops resolving immediately,
so anything already sharing it gets a 404 - that is inherent to renaming, not a bug to work around.

## Tags

Links can be labelled. A tag has a **name** and a **colour**, and it belongs to the person who made
it: your tags are yours, nobody else sees them, and two people can both have one called `work`
without them being the same tag.

A link carries its tags as one **comma separated string** in a `Tags` property - `work,urgent`.
That is deliberately a single property rather than a repeated one: a link's whole labelling arrives
with the entity, and there is no index row per tag to pay for.

- Names are compared **case insensitively** and with runs of whitespace collapsed, so `Work` and
  `work` are the same tag. The casing you created it with is the casing that gets shown.
- Up to **10 tags per link**, each up to **32 characters**. Commas, slashes and control characters
  are rejected - a comma is the separator, and the name ends up in a url path and a datastore key.
- Typing a tag onto a link **creates it** if you don't have it yet, with a colour picked from the
  palette that you aren't already using. You never have to visit the tags page first.
- Renaming a tag rewrites the name on every link of yours carrying it. Deleting one takes it off
  those links and leaves the links otherwise alone.

Filter by tag with **`?tag=work`** on the dashboard or on `GET /links`, or by clicking a pill. The
filter is applied in memory after the list comes back, for the same reason the list is sorted there:
a datastore filter on tags alongside the `CreatedBy` one would need a composite index.

An **admin** looking at everybody's links still sees their own tag list in the filters, because tags
are a private filing system rather than part of the service's state. A link carrying somebody else's
tag still shows the label, drawn in the default grey. An admin who tags another person's link puts
that tag in **that person's** set, not their own.

Two limits worth knowing, both inherited from `LINK_LIST_LIMIT`:

- Filtering happens after the first 500 links are loaded, so it filters that page rather than
  searching everything behind it.
- Renaming or deleting a tag rewrites the same first 500. Past that, the tag would stay on the
  links beyond the limit. Both are the point at which the `CreatedBy` + `Created` composite index
  is worth adding.

## Redirects and click counts

`GET /:short` counts the click **before** it writes the redirect, inside a datastore
transaction that re-reads the entity. It used to fire a goroutine after responding, which
loses counts two ways on Cloud Run: the container's cpu is throttled the moment the response
goes out, so the goroutine is often frozen before it reaches datastore, and it incremented a
copy of the link read outside the transaction, so simultaneous clicks overwrote each other.

The cost is one datastore round trip on the redirect path. If that ever matters more than
exact counts, the alternative is enabling always-on cpu for the service rather than going
back to a goroutine.

An unknown short code is not an error page. Any failure to look the code up - most often a
code that was never created - answers with a 302 to `/`, which is the dashboard when you are
signed in and `/login` when you are not, so somebody who followed a dead link lands on the
front of the site instead of on a JSON body. Lookups that fail for a reason other than
"no such link" still get logged before the redirect goes out.

## Configuration

| Variable                | Required | Default             | Notes                                                    |
|-------------------------|----------|---------------------|----------------------------------------------------------|
| `DATASTORE_PROJECT_ID`  | yes      | -                   | Fatal if unset                                            |
| `DATASTORE_NAMESPACE`   | yes      | -                   | Fatal if unset. Also the JWT audience                     |
| `JWT_SIGNING_KEY`       | yes      | -                   | Fatal if unset. Rotating it signs everybody out           |
| `RESEND_API_KEY`        | yes      | -                   | Without it login is disabled and returns 503              |
| `MAIL_FROM`             | yes      | -                   | e.g. `ajn.me <login@ajn.me>`, on a verified domain        |
| `APP_BASE_URL`          | yes      | `https://ajn.me`    | Public origin. Magic links are built from it              |
| `SITE_NAME`             | no       | `ajn.me`            | Name shown on the pages and in the login email            |
| `SESSION_COOKIE_NAME`   | no       | `ls_session`        |                                                           |
| `COOKIE_DOMAIN`         | no       | empty (host-only)   | Only set this to share the session across subdomains      |
| `COOKIE_SECURE`         | no       | `true`              | Set `false` only for plain http local development         |
| `PORT`                  | no       | `8080`              | Set by Cloud Run                                          |
| `mode`                  | no       | -                   | `debug` keeps gin in debug mode                           |
| `TRUSTED_PROXY_DEPTH`   | no       | `0`                 | Proxy hops in front of this service that append to `X-Forwarded-For`. `0` suits a Cloud Run domain mapping; add one per extra load balancer or CDN |
| `TRUSTED_PROXY_DEBUG`   | no       | unset               | Set to anything to log how each caller's address was resolved, to check the setting above against real traffic |

`TEMP_PASS` is gone. The `POST /token` endpoint it guarded has been removed - it handed out a token
to anyone who sent a matching `password` header, and when `TEMP_PASS` was unset the empty header
matched the empty value, so it authenticated everybody. Remove it from the Cloud Run service.

## Datastore

Four kinds, all in `DATASTORE_NAMESPACE`:

- **`link`** - key name is the upper-cased short code. Carries its labels in a `Tags` string
  property, comma separated.
- **`user`** - the allow list. Key name is the lower-cased email address. Having an entity is what
  grants access.
- **`magic_link`** - pending login tokens. Key name is the SHA-256 hash of the emailed token.
- **`tag`** - a label. Key name is `<lower-cased owner email>:<lower-cased tag name>`, which is what
  makes a tag owned: two people can each have a `work` tag and neither can end up with two of them,
  without a uniqueness query on the way in.

| Property  | Type    | Notes                                              |
|-----------|---------|----------------------------------------------------|
| `Name`    | string  | As displayed, in the casing it was created with     |
| `Color`   | string  | `#rrggbb`. Anything else is read as the default grey |
| `Owner`   | string  | Lower-cased email address. Filtered on              |
| `Created` | integer | Unix seconds                                        |

The `user`, `magic_link` and `tag` kinds are looked up by key, the link list filters on `CreatedBy`
with no sort order, and the tag list filters on `Owner` with no sort order - datastore sorts
nothing, the service sorts each page in memory - so **no composite indexes are needed**. Past a few
thousand links, swap that for a `CreatedBy` + `Created` composite index and let datastore do the
ordering.

`Color` is checked on the way out of datastore, not just on the way in: it ends up in a `style`
attribute and in the json the dashboard builds its pills from, so a value edited by hand in the
console into something that isn't a hex colour is read as the default rather than reaching the page.

### Allowing someone to sign in

Signed in as an admin, go to **`/users`** - the Access page, linked from the dashboard. It lists
everyone who can sign in, with their role, when they were added and when they last signed in, and
it has the form for adding somebody. Tick the box to make them an admin too. Nothing is emailed -
tell them to visit `/login` and request a link themselves.

Both routes are admin-only. The check is a middleware that reads the caller's admin flag from
datastore on every request rather than trusting the session token, so revoking somebody's admin
takes effect immediately instead of waiting out a 30 day cookie. Hiding the controls from
non-admins is only cosmetic; the middleware is what enforces it. A non-admin who follows a link to
`/users` gets a 403 page rather than a json error.

```bash
curl -X POST https://ajn.me/users -H 'Content-Type: application/json' \
  -d '{"email":"someone@example.com","admin":false}' -b 'ls_session=...'
```

Re-adding an existing address returns 409 rather than overwriting, so it can never quietly reset
somebody's admin flag or their login history.

The page also carries the controls for changing a role or blocking somebody, described below.

### Changing somebody's role or blocking them

Each row on the Access page has **Make admin / Remove admin** and **Disable / Enable**. Both go
through `PATCH /users/:email`, which takes `{"admin": true}`, `{"disabled": true}`, or both. A field
left out is left alone, so flipping one never disturbs the other.

**An admin cannot change their own role or disable themselves.** Their own row shows no controls and
the endpoint refuses it with a 403. That one rule is also what stops the service from ever being
left without a working admin: only an admin can demote anybody, and nobody can demote themselves, so
whoever is last standing cannot be removed by anyone. To step down, have another admin do it.

Disabling is the way to revoke access. It blocks sign in **and ends any session the person already
has** - every authenticated request re-reads the account, so the change takes effect on their next
click rather than whenever their 30 day cookie expires. Their links and history stay untouched, and
enabling them again restores everything. Deleting a user outright is still a console job.

The same re-read means an account deleted in the console also loses its live sessions immediately.
It costs one datastore key lookup per authenticated request; the public redirect path is untouched.
If datastore is unreachable the request is allowed through on the strength of its signed token -
only an explicit "no such user" or "disabled" closes the door.

### Creating a user by hand

The first admin has to be made this way, since adding users requires already being one.

In the console, under the right namespace, create an entity of kind `user` with the **key name set
to the lower-cased email address** (`someone@example.com`). Optional properties the service
understands:

| Property       | Type    | Meaning                                                  |
|----------------|---------|----------------------------------------------------------|
| `Email`        | string  | Informational copy of the address                         |
| `Created`      | integer | Unix seconds, informational                               |
| `LastLogin`    | integer | Unix seconds, written on every successful sign in         |
| `LastLinkSent` | integer | Unix seconds, used to throttle links to one per minute    |
| `RecentLinkSents` | integer[] | Unix seconds of the links sent in the last day, for the hourly and daily caps. Unindexed |
| `Admin`        | boolean | `true` lets them see and change every link, not just their own |
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
