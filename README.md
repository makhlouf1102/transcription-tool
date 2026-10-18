# Clearword

Local transcription workspace.

## Requirements

Go 1.21+, Node.js with npm, [Just](https://just.systems/), Docker, and FFmpeg with `ffprobe`.

## Commands

```sh
# Install workspace dependencies (once).
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

## Nx workspace

The Go server and React frontend are separate Nx projects under `apps/`.

```sh
# List projects and inspect the dependency graph.
npx nx show projects
npx nx graph

# Run project tasks directly.
npx nx run frontend:build
npx nx run frontend:typecheck
npx nx run backend:test
npx nx run backend:build
```

Open [http://clearword.localhost](http://clearword.localhost) after starting the app.

## Local transcription

Select **Local Whisper (no API fee)** under Settings to transcribe audio on this computer without an OpenAI or AssemblyAI key. Clearword runs [whisper.cpp](https://github.com/ggml-org/whisper.cpp) locally through its Go backend. On first start, Docker downloads the multilingual Base model and keeps it in a persistent volume. The model is downloaded once; using the local provider does not send audio to a transcription API. The Base model does not provide speaker diarization.
