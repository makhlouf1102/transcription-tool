# Install the monorepo's Node.js dependencies.
setup:
    npm ci

# Build the frontend app with Nx.
build-frontend:
    npx nx run frontend:build

# Bundle the frontend and start the local transcription server.
run: build-frontend
    APP_PORT="${PORT:-8766}" docker compose up -d --wait caddy whisper
    cd apps/backend && WEB_DIR=../frontend/web WHISPER_URL="http://127.0.0.1:5052" go run -buildvcs=false .

# Build and run the Docker Compose stack in the foreground.
docker-up:
    docker compose up -d --build

# Stop the Docker Compose stack while keeping its persistent data volume.
docker-down:
    docker compose down --remove-orphans

# Follow logs from the frontend and backend containers.
docker-logs:
    docker compose logs --follow
