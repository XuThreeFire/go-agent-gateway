#!/usr/bin/env python3
"""检查公开源码隐私；密钥识别交给 Gitleaks，输出始终脱敏。"""
from pathlib import Path
import ipaddress
import os
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
PRIVATE_DIRS = {'.git', '.idea', '.local-docs', 'docs', 'tmp', 'dist', 'node_modules', 'bin', 'data', '.cache', '__pycache__'}
PRIVATE_NAMES = {'.DS_Store', '.env', 'config.local.yaml', '.gateway-session'}


def is_private(path):
    p = Path(path)
    return any(part in PRIVATE_DIRS for part in p.parts) or p.name in PRIVATE_NAMES or (p.name.startswith('.env.') and not p.name.endswith('.example'))


def public_files():
    for directory, dirs, files in os.walk(ROOT, followlinks=False):
        dirs[:] = sorted(d for d in dirs if d not in PRIVATE_DIRS and not Path(directory, d).is_symlink())
        for name in sorted(files):
            path = Path(directory, name)
            if not is_private(path.relative_to(ROOT)):
                yield path


def check():
    failures = []
    git_dir = ROOT / '.git'
    if git_dir.exists():
        for directory in ('docs', '.local-docs'):
            if subprocess.run(['git', 'check-ignore', '-q', f'{directory}/probe.md'], cwd=ROOT).returncode:
                failures.append((directory, '本地资料未被 Git 忽略'))
        tracked = subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0')
        for name in filter(None, tracked):
            if is_private(name):
                failures.append((name, '私有文件已经被跟踪，忽略规则不能移除既有跟踪'))
    else:
        print('尚无 Git 元数据：扫描当前公开文件；初始化后请再次运行检查。')
    for path in public_files():
        relative = path.relative_to(ROOT).as_posix()
        if path.is_symlink():
            failures.append((relative, '公开文件不允许使用符号链接'))
            continue
        try:
            content = path.read_text('utf-8')
        except UnicodeError:
            if path.suffix.lower() not in {'.png', '.jpg', '.gif', '.ico', '.woff2'}:
                failures.append((relative, '存在未分类二进制产物'))
            continue
        if re.search(r'/(?:Users|home)/[A-Za-z0-9_.-]+/', content):
            failures.append((relative, '包含个人绝对路径'))
        for raw in re.findall(r'(?<![\d.])(?:\d{1,3}\.){3}\d{1,3}(?![\d.])', content):
            try:
                if ipaddress.ip_address(raw).is_global:
                    failures.append((relative, '包含公网 IP，请改为示例域名或显式配置'))
                    break
            except ValueError:
                pass
    for path, reason in failures:
        print(f'失败：{path}：{reason}', file=sys.stderr)
    if failures:
        return False
    configured = os.environ.get('GITLEAKS_BIN')
    binary = configured or (str(ROOT / 'bin/gitleaks') if (ROOT / 'bin/gitleaks').exists() else shutil.which('gitleaks'))
    if not binary:
        print('缺少 Gitleaks；先执行 make tools。', file=sys.stderr)
        return False
    result = subprocess.run([binary, 'dir', '--redact', '--no-banner', '--config', str(ROOT / '.gitleaks.toml'), str(ROOT)], cwd=ROOT)
    if result.returncode:
        return False
    print('公开文件隐私扫描通过；原始本地工作区不能整体打包发布。')
    return True


if __name__ == '__main__':
    sys.exit(0 if check() else 1)
