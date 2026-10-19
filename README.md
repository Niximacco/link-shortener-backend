| `PATCH /link/:short`      | session   | Changes the destination, the short code, the tags, or any of them |
| `DELETE /link/:short`     | session   | Deletes a link                                      |
| `GET /tags`               | session   | Tags page: name and colour your labels              |
| `GET /api/tags`           | session   | Your tags as json                                   |
| `POST /tags`              | session   | Creates a tag                                       |
| `PATCH /tags/:name`       | session   | Renames a tag, recolours it, or both                |
| `DELETE /tags/:name`      | session   | Deletes a tag and takes it off your links           |
# link-shortener-backend

A simple link shortening backend. Its data is a SQLite database of its own on the pillar-of-autumn host: one file, its tables declared in `pillar.json` and made by the deploy.

Sign in is passwordless: an allow-listed email address requests a magic link, clicking it starts a
session, and the session cookie slides forward every time you come back.

The link itself comes from [ajn_auth](https://github.com/Niximacco/ajn_auth), the shared service at
`auth.ajn.me`. Minting the token, mailing it, hosting the "yes, it was me" page and the per-address
send caps used to live in this repository, byte-identical to the copies in three other sites. What
stays here is the half a shared service cannot hold: **who may sign in**. The user list, the session
cookie and its signing key are ours, and the service is asked nothing but "did this person prove
they can read that address".

## How authentication works

1. `GET /login` serves the sign in page. It's rendered by this service - the html is embedded in the
   binary, so there is nothing else to deploy or keep in sync.
2. `POST /login` looks the address up in the `user` kind. Unknown or disabled addresses get exactly
   the same "check your email" response as real ones, and no call is made, so the form can't be used
   to find out who has an account and a stranger's address never costs an email.
3. For an address that is on the list, this service asks `auth.ajn.me` to mail a link. The token
   behind it is minted and stored there, never here. Links last **15 minutes** and work **once**.
4. The link points at the service's own confirm page rather than at anything here - a button rather
   than a bare GET, because mail scanners and link previewers fetch urls out of email and would
   otherwise burn the token before you ever clicked it. Pressing it spends the token and redirects
   the browser to `GET /auth/callback` here, carrying a single-use exchange code good for two
   minutes.
5. `GET /auth/callback` trades that code for the address behind it, checks the `user` kind **again**
   - a link can sit in an inbox for fifteen minutes and an account can be removed in fourteen of
   them - then signs a HS512 JWT and sets it as an http-only, `SameSite=Lax`, Secure cookie good for
   **30 days**.
6. Any authenticated request re-issues that cookie once it is more than an hour old, so an active
   user is never signed out. Someone who stays away for 30 days is.

The address never travels in the redirect. What crosses the front channel is the code, which is
worthless in an access log, in browser history and in a `Referer` header a moment later, and
worthless to anybody without this site's api key at any time.

The API also still accepts `Authorization: bearer <token>` for scripts.

## Rate limits

There is no captcha, because the thing a captcha would guard is already shut. `POST /login` only
mails an address that is in the `user` kind, and that check happens *before* anything is sent, so a
bot working through guessed addresses gets the same "check your email" page as everybody else and
costs nothing but one indexed read. Nothing is emailed to a stranger, ever.

What is left to bound is a caller who knows, or guesses, an address that really is on the list.
Four limits stack up:

| Limit                        | Scope              | Where it lives              |
|------------------------------|--------------------|-----------------------------|
| One link per **60 seconds**  | Per email address  | At `auth.ajn.me`            |
| **5** links per hour         | Per email address  | At `auth.ajn.me`            |
| **15** links per day         | Per email address  | At `auth.ajn.me`            |
| **10** posts per minute to `/login`, **20** to `/auth/callback` | Per client address | In memory, per instance |

The per address caps moved with the sending, to the side of the wire where the Resend bill lands.
They are counted per site and per address there, so they hold across every running instance of this
service and every other site that shares the same key. Being capped comes back looking exactly like
a link going out, so the form still cannot be used to work out which addresses are real.

The per connection limits are a brake on floods, not a promise: they live in memory, so with several
instances up the real ceiling is the limit times the instance count. They protect database reads and
instance time rather than the email budget. A caller over one gets a 429 with `Retry-After`. Requests
whose origin cannot be established are **not** limited - see `TRUSTED_PROXY_DEPTH` above, and
`internal/ratelimit` for why guessing at an identity is worse than not having one.

## Routes

| Route                     | Auth      | What it does                                       |
|---------------------------|-----------|----------------------------------------------------|
| `GET /`                   | session   | Dashboard: create, list, edit, tag and delete links. `?tag=` filters |
| `GET /login`              | -         | Sign in form                                        |
| `POST /login`             | -         | Asks `auth.ajn.me` to email a magic link            |
| `GET /auth/callback`      | -         | Trades the code the service redirects with, starts the session |
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

Ownership is checked **inside** the SQL transaction that does the write, so it cannot be
raced: a link cannot change hands between the check and the update.

Two consequences worth knowing:

- **Links created before magic-link login have `CreatedBy` set to whatever the old `/token`
  endpoint hardcoded.** If you sign in as a different address, those links are not yours and will
  not appear in your list. Sign in as that address, make yourself an admin, or rewrite the
  `CreatedBy` property on the entities.
- **Links with an empty `CreatedBy` belong to nobody** and are only reachable by an admin.

### Renaming a link

`PATCH` accepts a new `short`, which moves the row, because the short code *is* the primary
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
  are rejected - a comma is the separator, and the name ends up in a url path and a database key.
- Typing a tag onto a link **creates it** if you don't have it yet, with a colour picked from the
  palette that you aren't already using. You never have to visit the tags page first.
- Renaming a tag rewrites the name on every link of yours carrying it. Deleting one takes it off
  those links and leaves the links otherwise alone.

Filter by tag with **`?tag=work`** on the dashboard or on `GET /links`, or by clicking a pill. The
filter is applied in memory after the list comes back, because a link's tags are one comma
separated column rather than a table of their own, and the page is already capped by the list limit.

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

`GET /:short` counts the click **before** it writes the redirect, with a single
`UPDATE` that adds to the number in the row. It used to fire a goroutine after responding, which
loses counts two ways on Cloud Run: the container's cpu is throttled the moment the response
goes out, so the goroutine is often frozen before it reaches the database, and it incremented a
copy of the link read a moment earlier, so simultaneous clicks overwrote each other. One
statement can do neither. The cost is one write to a file in this container, which is cheap
enough that the goroutine it replaced was never buying anything worth the counts it lost.

An unknown short code is not an error page. Any failure to look the code up - most often a
code that was never created - answers with a 302 to `/`, which is the dashboard when you are
signed in and `/login` when you are not, so somebody who followed a dead link lands on the
front of the site instead of on a JSON body. Lookups that fail for a reason other than
"no such link" still get logged before the redirect goes out.

## Configuration

| Variable                | Required | Default             | Notes                                                    |
|-------------------------|----------|---------------------|----------------------------------------------------------|
| `DATABASE_PATH`         | yes      | -                   | The SQLite file. Set by the deploy, which makes the file and its tables before the container starts. Fatal if unset |
| `JWT_SIGNING_KEY`       | yes      | -                   | Fatal if unset. Rotating it signs everybody out           |
| `AJN_AUTH_URL`          | for login | -                  | The magic link service, `https://auth.ajn.me`             |
| `AJN_AUTH_API_KEY`      | for login | -                  | This site's key, from the service's admin pages. Without it login is disabled and returns 503 |
| `AJN_AUTH_REDIRECT_URI` | no       | -                   | Only needed if this site registers more than one callback. Empty uses the first one on the roster |
| `APP_BASE_URL`          | yes      | `https://ajn.me`    | Public origin. Short links are built from it, and it is what this site is registered under |
| `SITE_NAME`             | no       | `ajn.me`            | Name shown on the pages                                   |
| `SESSION_COOKIE_NAME`   | no       | `ls_session`        |                                                           |
| `COOKIE_DOMAIN`         | no       | empty (host-only)   | Only set this to share the session across subdomains      |
| `COOKIE_SECURE`         | no       | `true`              | Set `false` only for plain http local development         |
| `PORT`                  | no       | `8080`              | Set by Cloud Run                                          |
| `mode`                  | no       | -                   | `debug` keeps gin in debug mode                           |
| `TRUSTED_PROXY_DEPTH`   | no       | `0`                 | Proxy hops in front of this service that append to `X-Forwarded-For`. `0` suits a Cloud Run domain mapping; add one per extra load balancer or CDN |
| `TRUSTED_PROXY_DEBUG`   | no       | unset               | Set to anything to log how each caller's address was resolved, to check the setting above against real traffic |

`RESEND_API_KEY` and `MAIL_FROM` are gone. Nothing here sends mail any more - the login email is the
only one this service ever sent, and it is sent by `auth.ajn.me` now, from one verified sender on
one Resend account. Remove both from the Cloud Run service.

### Registering this site with auth.ajn.me

The service will not mail a link on behalf of a site it does not know, and will not deliver an
exchange code to a url that site did not register. Both live in the roster, edited at
`https://auth.ajn.me/admin`:

| Field | Value |
|---|---|
| Id | `link-shortener` |
| Name | `ajn.me` |
| Base url | `https://ajn.me` |
| Redirect uris | `https://ajn.me/auth/callback`, and `http://localhost:8080/auth/callback` to develop against the deployed service |
| From | `ajn.me <login@ajn.me>` |
| Accent | Leave it empty. The default near-black is `#1d1d1f`, which is already this site's own `--accent` |

Then generate a key on that page and set it as `AJN_AUTH_API_KEY` here. The plaintext is shown once
and is not recoverable, so rotating means generating a second, deploying it, and revoking the first.

`TEMP_PASS` is gone. The `POST /token` endpoint it guarded has been removed - it handed out a token
to anyone who sent a matching `password` header, and when `TEMP_PASS` was unset the empty header
matched the empty value, so it authenticated everybody. Remove it from the Cloud Run service.

## The database

One SQLite file, at `DATABASE_PATH`. There is no database server: SQLite is a library in this
service's own process, so the database costs nothing while nobody is using it and no memory but
this service's. The deploy makes the file and its tables, from the `database` block in
`pillar.json`, before the container starts - nothing here runs a migration, and `Open` refuses a
file that is missing a table rather than failing one page at a time.

Three tables:

- **`links`** - primary key is the upper-cased short code, which is how a code is case
  insensitive. Carries its labels in a `tags` text column, comma separated. Indexed on
  `(created_by, created)`, which is the dashboard's query.
- **`users`** - the allow list. Primary key is the lower-cased email address. Having a row is what
  grants access.
- **`tags`** - a label. Primary key is `<lower-cased owner email>:<lower-cased tag name>`, which
  is what makes a tag owned: two people can each have a `work` tag and neither can end up with two
  of them, without a uniqueness query on the way in. `owner` is a column of its own as well, and
  indexed, because that is what the tag list filters on.

Every table is `STRICT`, so a value of the wrong type is an error rather than a surprise. The
flags - `admin`, `disabled` - are integer columns holding 0 or 1.

`color` is checked on the way out, not just on the way in: it ends up in a `style` attribute and in
the json the dashboard builds its pills from, so a value that isn't a hex colour is read as the
default grey rather than reaching the page.

The tests run against a real database. Each opens a temporary file with the tables built from
`pillar.json` itself, so a column this service reads that the file does not declare fails a test
rather than a deploy. On datastore none of this could be tested without the emulator, so none of
it was.

### Allowing someone to sign in

Signed in as an admin, go to **`/users`** - the Access page, linked from the dashboard. It lists
everyone who can sign in, with their role, when they were added and when they last signed in, and
it has the form for adding somebody. Tick the box to make them an admin too. Nothing is emailed -
tell them to visit `/login` and request a link themselves.

Both routes are admin-only. The check is a middleware that reads the caller's admin flag from
the database on every request rather than trusting the session token, so revoking somebody's admin
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
enabling them again restores everything. Deleting a user outright is still done by hand.

The same re-read means an account deleted by hand also loses its live sessions immediately.
It costs one primary key lookup per authenticated request; the public redirect path is untouched.
If the database is unreachable the request is allowed through on the strength of its signed token -
only an explicit "no such user" or "disabled" closes the door.

### Creating a user by hand

The first admin has to be made this way, since adding users requires already being one.

The database has a page on the host dashboard, under **Databases**, with a data explorer. Insert a
row into `users` with the address lower-cased:

```sql
INSERT INTO users (email, created, last_login, admin, disabled)
VALUES ('someone@example.com', unixepoch(), 0, 1, 0);
```

| Column       | Type    | Meaning                                                     |
|--------------|---------|-------------------------------------------------------------|
| `email`      | text    | The address, lower-cased. The primary key                    |
| `created`    | integer | Unix seconds                                                 |
| `last_login` | integer | Unix seconds, written on every successful sign in            |
| `admin`      | integer | `1` lets them see and change every link, not just their own  |
| `disabled`   | integer | `1` blocks sign in without deleting the row                  |

To revoke access, delete the row or set `disabled` to 1 - existing sessions keep working until the
cookie expires unless you also rotate `JWT_SIGNING_KEY`.

### What used to be in datastore

`magic_link` held pending login tokens, keyed by the SHA-256 hash of the emailed one. The tokens
live at `auth.ajn.me` now, and that kind was already dead before the move, so it got no table. It
is named in `ignore_kinds`, which is how the import was told to leave its entities where they are
rather than refusing to run.

The `link`, `user` and `tag` kinds in namespace `links.ajn.me` are what the tables were imported
from, through a `datastore` mapping in `pillar.json`. Once the move had settled the mapping came
out, with `DATASTORE_PROJECT_ID` and `DATASTORE_NAMESPACE`, and the namespace's entities go with
pillar-of-autumn's `clear-datastore`.

`DATASTORE_NAMESPACE` was also the session token audience. That is the constant `links.ajn.me` in
`internal/auth` now - the value the variable held - so the sessions issued before are still good.

## Local development

```bash
go run ./cmd/localdb link-shortener-backend.sqlite
export DATABASE_PATH=link-shortener-backend.sqlite JWT_SIGNING_KEY=dev-key
export APP_BASE_URL=http://localhost:8080 COOKIE_SECURE=false mode=debug
go run ./cmd/link-shortener-backend
```

With no `AJN_AUTH_URL` and `AJN_AUTH_API_KEY` the login form says sign in is unavailable, because
there is nothing to ask for a link. Signing in locally means pointing at the deployed service with a
real key and having `http://localhost:8080/auth/callback` on this site's redirect uris - that is the
one place the service allows plain http, and only for localhost:

```bash
export AJN_AUTH_URL=https://auth.ajn.me AJN_AUTH_API_KEY=ajnauth_xxx AJN_AUTH_REDIRECT_URI=http://localhost:8080/auth/callback
```

The link still arrives by email and its confirm page is still on `auth.ajn.me`; only the redirect at
the end of it comes back to the laptop.

```bash
go test ./...
```
