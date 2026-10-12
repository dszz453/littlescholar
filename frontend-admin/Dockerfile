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

# 构建期就校验 nginx 配置语法。
# nginx 只在启动时才解析配置，语法错误会一路推到生产才炸
# （本项目就踩过：add_header 的值含逗号没加引号，容器无限重启）。
#
# 注意：配置里 proxy_pass 指向 compose 网络的服务名 core-server，
# 构建环境没有这条 DNS 记录，nginx -t 会以 "host not found in upstream" 中断。
# 所以先往 /etc/hosts 塞个占位，让它只做语法与结构校验。
# 运行时 Docker 会用自己的 /etc/hosts 覆盖，不影响真实解析。
RUN echo "127.0.0.1 core-server" >> /etc/hosts && nginx -t

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
