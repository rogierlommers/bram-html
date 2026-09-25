# bram-html

A child-friendly HTML and CSS playground with an instant preview, passwordless email sign-in, and private page storage.

## Project structure

```text
frontend/   Static HTML, CSS, and JavaScript
backend/    Go API, SQLite storage, authentication, and tests
Dockerfile  Production container build
```

## Run locally

You need Go 1.25 or newer.

```sh
go run ./backend
```

Open [http://localhost:8080](http://localhost:8080). When you request a sign-in code, it is printed in the terminal. Enter that code in the sign-in dialog.

The SQLite database is created at `data/bram-html.db` and is excluded from Git.

## Configure email

Copy `.env.example` values into your environment. `bram-html` uses SMTP when `SMTP_HOST` is set and otherwise uses the development console fallback.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` | Server listen address |
| `BASE_URL` | `http://localhost:8080` | Public origin used for security-sensitive URL and cookie settings |
| `DATA_DIR` | `data` | SQLite database directory |
| `STATIC_DIR` | `frontend` | Directory containing the static frontend files |
| `AUTH_RATE_LIMIT_PER_IP` | `5` | Sign-in requests per client IP per 10 minutes; use `-1` behind a trusted rate-limiting proxy |
| `AUTH_RATE_LIMIT_GLOBAL` | `100` | Sign-in requests per process per minute |
| `AUTH_VERIFY_RATE_LIMIT_PER_IP` | `10` | Code verification attempts per client IP per 10 minutes; use `-1` behind a trusted rate-limiting proxy |
| `AUTH_VERIFY_RATE_LIMIT_GLOBAL` | `500` | Code verification attempts per process per minute |
| `AUTH_CODE_SECRET` | random on localhost | Secret used to protect stored sign-in codes; required outside local development and must be identical on every instance |
| `ADMIN_EMAILS` | empty | Comma-separated email addresses allowed to open the statistics page at `/admin` |
| `SMTP_HOST` | empty | SMTP host; empty enables console links |
| `SMTP_PORT` | `587` | SMTP port |
| `SMTP_USERNAME` | empty | SMTP username |
| `SMTP_PASSWORD` | empty | SMTP password |
| `SMTP_FROM` | `bram-html@example.com` | Sender address |

In production, set `BASE_URL` to an HTTPS URL. Session cookies automatically use the `Secure` flag when `BASE_URL` begins with `https://`.
SMTP delivery requires STARTTLS so that sign-in codes are never sent to the mail server in plaintext. The app also applies small in-memory limits to sign-in requests and verification attempts. Production deployments should add rate limiting at the reverse proxy or edge; when all traffic reaches Go from one trusted proxy, set both `AUTH_RATE_LIMIT_PER_IP=-1` and `AUTH_VERIFY_RATE_LIMIT_PER_IP=-1` so unrelated users do not share an application-level allowance. Keep both global limits enabled.

## Test

```sh
cd backend
go test ./...

cd ../frontend
node --test script.test.js
```

The tests exercise one-time sign-in codes, session cookies, authentication requirements, the complete saved-page lifecycle, and sign-in form behavior.

## Docker

Build and run from the repository root:

```sh
docker build -t bram-html .
docker run --rm -p 8080:8080 -v bram-html-data:/app/data bram-html
```

For production, pass `BASE_URL` and SMTP settings with `--env-file` or your container platform's secret management. The image runs as an unprivileged user and stores SQLite data in `/app/data`.

## GitHub Actions

`.github/workflows/build-application.yml` builds and tests the Go backend, publishes coverage artifacts, builds the Docker image for pull requests, and pushes `rogierlommers/bram-html` on `main`. Main-branch builds deploy the immutable commit tag to the `bram-html` service in `/srv/local`.

Configure these repository-level Actions secrets for publishing the image:

- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`

Create a protected GitHub Environment named `production` and configure these deployment secrets on it:

- `TS_OAUTH_CLIENT_ID`
- `TS_OAUTH_SECRET`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `SSH_KNOWN_HOSTS` — a pinned entry using the `services` host alias, such as `services ssh-ed25519 AAAA...`
