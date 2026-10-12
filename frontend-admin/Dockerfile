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

# nginx 配置的语法校验不放在这里，而是在 CI 的独立 job（nginx-check）里做。
# 原因：配置里 proxy_pass 指向 compose 服务名 core-server，
# 构建环境没有这条 DNS，nginx -t 会以 "host not found in upstream" 中断；
# 而想在构建期补 hosts 又不行 —— build 阶段的 /etc/hosts 是只读挂载。
# 放到 CI 用 docker run 校验（那里 /etc/hosts 可写、也能 sed 替换主机名）更合适。

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
