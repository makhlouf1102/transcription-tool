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
