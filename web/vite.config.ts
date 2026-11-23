import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发期把 /api 代理到后端，避免 CORS，也让 SSE 走同源。
// 生产由 nginx 反代（见 deploy/nginx.conf），前端只认相对路径 /api。
//
// 默认指向真后端 :8080（backend/configs/dev/config.yaml 里的 http.addr）；
// 换地址用环境变量 GATEWAY_API_TARGET 覆盖，不要改代码。
export default defineConfig(({ mode }) => {
  // 第三个参数为空串 = 不加前缀过滤，命令行上 `GATEWAY_API_TARGET=... make web-dev`
  // 也会被读进来（loadEnv 会把 process.env 里匹配前缀的键一起合并）
  const env = loadEnv(mode, '.', '')
  const target = env.GATEWAY_API_TARGET || 'http://127.0.0.1:8080'

  return {
    plugins: [vue()],
    server: {
      host: '127.0.0.1',
      port: 5173,
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
          // SSE 由 http-proxy 边读边转发，这里不要加 compress 或 buffer 相关选项
        },
        // 健康检查不在 /api 下，单独代理，否则 dev 环境总显示「服务不可用」
        '/healthz': { target, changeOrigin: true },
      },
    },
  }
})
