# Clearword

Local transcription workspace.

## Requirements

Go 1.21+, Node.js with npm, [Just](https://just.systems/), Docker, and FFmpeg with `ffprobe`.

## Commands

```sh
# Install frontend dependencies (once).
just setup

# Build the frontend and run the app locally.
just run

# Build and run with Docker Compose.
just docker-up

# Follow Docker Compose logs.
just docker-logs

# Stop Docker Compose and keep its data.
just docker-down

# Build the frontend only.
just build-frontend
```

Open [http://clearword.localhost](http://clearword.localhost) after starting the app.

## Translate transcripts on your computer

Clearword can translate completed transcripts on the user's computer from the transcript page. Choose the source and target languages and select **Translate**. The app starts its local translation engine with the rest of Clearword; no separate installation or terminal steps are needed. The first run downloads language models and saves them in a Docker volume. Translation runs on the computer without a per-use API charge.

Clearword uses LibreTranslate, whose open source translation engine is Argos Translate. The local engine is not exposed on the network; the Go backend sends transcript text to it over the private Docker network.
