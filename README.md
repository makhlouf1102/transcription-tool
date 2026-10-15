# Clearword

A local transcription workspace for video and audio files. It extracts mono 128 kbps MP3 audio with FFmpeg, keeps uploads below 25 MB, and supports OpenAI and AssemblyAI transcription.

The frontend is built with React and Mantine, then bundled locally with esbuild; the Go server serves the generated bundle and keeps the existing JSON API.

The backend's OpenAPI contract is `web/openapi.json`. `npm run build` deterministically generates `src/api-schema.d.ts` with the pinned `openapi-typescript` generator, type-checks the TypeScript frontend and OpenAPI fetch calls, then bundles the app. Run `npm run generate:api` after changing the API contract; do not edit the generated schema file by hand.

## Run

1. Install Go 1.21 or later, Node.js with npm, [Just](https://just.systems/), Docker, and FFmpeg (including `ffprobe`).
2. Run `just setup` once to install the React build dependencies.
3. From this folder, run `just run` (this builds the React frontend, starts Caddy in Docker, then starts Go; Go downloads the SQLite driver on first launch). To run the binary directly instead, build with `go build -buildvcs=false -o clearword .` and run `./clearword`.
4. Open [http://clearword.localhost](http://clearword.localhost). When running the binary directly without Caddy, open [http://127.0.0.1:8766](http://127.0.0.1:8766).
5. Choose OpenAI or AssemblyAI in Settings and enter that provider's API key, then add a media file and start transcription. You can submit another file while earlier jobs continue in the background.

## Run with Docker Compose

Run `just setup` once to install the frontend dependencies, then run `just docker-up` from this folder. Open [http://clearword.localhost](http://clearword.localhost). Caddy serves plain local HTTP, so no certificate setup is needed. The URL also works when running `docker compose up --build` directly. To change Docker's host port, set `APP_PORT` (for example, `APP_PORT=9000 just docker-up`).

Compose builds the Go backend and React/Nginx frontend separately. Nginx serves the UI and forwards `/api/` requests to Go. The backend listens on port 8080 inside the Compose network; only the frontend port is published to the host. The named volume `transcription_data` is mounted at `/app/data`, where the server stores `transcriptions.sqlite3` and saved audio in `audio/`. The volume survives `docker compose down` and container rebuilds. `just docker-down` stops the stack without deleting that data.

Local scripts and agents can use the [transcription API](API.md); its OpenAPI description is served at `http://clearword.localhost/openapi.json` when started with `just run` or `just docker-up`, or `http://127.0.0.1:8766/openapi.json` when running the binary directly.

The API keys and transcription preferences are saved in the local SQLite database at `data/transcriptions.sqlite3` so they remain available after a refresh. Keys are stored as plain text in that local database; the data directory is restricted to owner access (mode `0700`) and the database file to owner read/write (mode `0600`). Keys are returned only to this loopback-only UI and are not included in transcription history. Transcription records, progress, and transcript text are persisted there too. Each upload starts a server-side background worker, and the page can monitor multiple jobs or be refreshed while they run. The converted mono MP3 is retained under `data/audio/` so it can be replayed from a saved transcription; it uses about 58 MB per hour at 128 kbps. Original uploads and temporary chunks are removed after processing. Delete MP3s from `data/audio/` to reclaim disk space; those recordings will no longer play. If the server stops mid-job, completed transcript parts remain saved and the job is marked interrupted when the server restarts. OpenAI transcription uses its `/v1/audio/transcriptions` endpoint; AssemblyAI uses its upload and transcript APIs with Universal-2. Long audio is split into 18-minute windows with 12-second overlaps. The splitter prefers nearby silences; overlaps protect words at fallback cuts, repeated words are removed when plain text chunks are joined, and speaker labels are reconciled across diarized OpenAI chunks using their shared audio. OpenAI's VAD chunking is also enabled inside each diarized upload.

The UI estimates API cost from the media duration and the selected model's listed per-minute rate. `gpt-4o-transcribe` and `whisper-1` are estimated at **$0.006 USD per minute**. Speaker diarization is token-priced, so the UI shows that billing basis instead of inventing a per-minute estimate. See [OpenAI's live pricing](https://developers.openai.com/api/docs/pricing) for current rates.

OpenAI currently plans to remove `gpt-4o-transcribe-diarize` on February 26, 2027. Check the current [model deprecations](https://developers.openai.com/api/docs/deprecations) before relying on it after that date.

`just run` uses Portless to provide the stable `https://clearword.localhost` URL. The server remains accessible only from this computer. Docker Compose continues to use `http://127.0.0.1:8766` by default.
