#!/usr/bin/env python3
"""只读环境诊断；不读取、打印密钥或自动修改系统。"""
import shutil
import subprocess

for binary, args in [('go', ['version']), ('node', ['--version']), ('npm', ['--version']), ('docker', ['--version'])]:
    path = shutil.which(binary)
    if path:
        result = subprocess.run([path] + args, text=True, capture_output=True)
        print(f'{binary}: {(result.stdout or result.stderr).strip()}')
    else:
        print(f'{binary}: 未安装（最小示例只需 Go；完整容器环境只需 Docker + Compose）')
if shutil.which('docker'):
    compose = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
    daemon = subprocess.run(['docker', 'info', '--format', '{{.ServerVersion}}'], capture_output=True)
    print('Compose: ' + ('可用' if compose.returncode == 0 else '不可用，请安装带 Compose 的 Docker Desktop 或 Compose 插件'))
    print('Docker 引擎: ' + ('可用' if daemon.returncode == 0 else '未启动或不可达'))
print('模拟模式: 无需模型密钥；真实模型请参考 handbook/tutorial/03-models.md')
print('数据库: 使用独立本地库；不要复用既有私人数据库。')
