#!/usr/bin/env python3
"""从通过扫描的公开文件集合生成源码包，保留本机私有文件。"""
from pathlib import Path
import tarfile
import sys
from privacy_check import ROOT, check, public_files

if not check():
    sys.exit(1)
output = ROOT / 'dist/go-agent-gateway-source.tar.gz'
output.parent.mkdir(exist_ok=True)
files = list(public_files())
with tarfile.open(output, 'w:gz') as archive:
    for path in files:
        metadata = archive.gettarinfo(str(path), 'go-agent-gateway/' + path.relative_to(ROOT).as_posix())
        # 不泄露构建机账号和本机修改时间。
        metadata.uid = metadata.gid = metadata.mtime = 0
        metadata.uname = metadata.gname = ''
        with path.open('rb') as source:
            archive.addfile(metadata, source)
print(f'已生成 {output.relative_to(ROOT)}，共 {len(files)} 个公开文件。')
