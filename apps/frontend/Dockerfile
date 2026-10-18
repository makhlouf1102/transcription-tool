FROM node:22-alpine AS build

WORKDIR /workspace
COPY package.json package-lock.json ./
COPY apps/frontend/package.json apps/frontend/package.json
RUN npm ci

COPY apps/frontend/src ./apps/frontend/src
COPY apps/frontend/web ./apps/frontend/web
COPY apps/frontend/tsconfig.json ./apps/frontend/tsconfig.json
RUN npm run build --workspace=@clearword/frontend

FROM nginx:alpine
COPY apps/frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /workspace/apps/frontend/web/index.html /usr/share/nginx/html/index.html
COPY --from=build /workspace/apps/frontend/web/app.js /usr/share/nginx/html/app.js
COPY --from=build /workspace/apps/frontend/web/app.css /usr/share/nginx/html/app.css
COPY --from=build /workspace/apps/frontend/web/openapi.json /usr/share/nginx/html/openapi.json

EXPOSE 80
