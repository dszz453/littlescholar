# ============ 构建阶段 ============
FROM node:20-alpine AS builder

WORKDIR /build

ARG VITE_API_BASE=/api
ENV VITE_API_BASE=${VITE_API_BASE}

# 国内 npm 加速（如不需要可删除）
RUN npm config set registry https://registry.npmmirror.com

COPY package.json ./
RUN npm install

COPY . .
RUN npm run build

# ============ 运行阶段 ============
FROM nginx:alpine

COPY --from=builder /build/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
