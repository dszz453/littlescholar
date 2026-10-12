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

# 构建期就校验 nginx 配置。
# nginx 只在启动时才解析配置，不校验的话语法错误会一路推到生产才炸
# （本项目就踩过：add_header 的值含逗号没加引号，容器无限重启）。
RUN nginx -t

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
