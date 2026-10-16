# Install the local React frontend build dependencies.
setup:
    npm ci

# Bundle the React frontend and Mantine styles into web/app.js and web/app.css.
build-frontend:
    npm run build

# Bundle the frontend and start the local transcription server.
run: build-frontend
    APP_PORT="${PORT:-8766}" docker compose up -d --wait caddy libretranslate
    LIBRETRANSLATE_URL="http://127.0.0.1:5001" go run -buildvcs=false .

# Build and run the Docker Compose stack in the foreground.
docker-up:
    docker compose up -d --build

# Stop the Docker Compose stack while keeping its persistent data volume.
docker-down:
    docker compose down

# Follow logs from the frontend and backend containers.
docker-logs:
    docker compose logs --follow
