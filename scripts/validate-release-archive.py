#!/usr/bin/env python3
"""在执行发布包内容前，校验 sidecar、tar 结构并安全解包。"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import re
import shutil
import stat
import sys
import tarfile
from pathlib import Path


SIDECAR_PATTERN = re.compile(r"^([0-9a-f]{64}) ([ *])([^/\\]+)$")
ROOT_PATTERN = re.compile(r"^ai-gdm-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-linux-amd64$")
MEMBER_PATTERN = re.compile(r"^[A-Za-z0-9._/-]+$")
MAX_ARCHIVE_BYTES = 2 * 1024**3
MAX_EXTRACTED_BYTES = 8 * 1024**3
MAX_MEMBERS = 10_000


def require_regular_file(path: Path, label: str) -> None:
    try:
        mode = path.lstat().st_mode
    except OSError as exc:
        raise ValueError(f"{label}不可读取: {exc}") from exc
    if not stat.S_ISREG(mode):
        raise ValueError(f"{label}必须是普通文件")


def read_expected_digest(sidecar: Path, archive_name: str) -> str:
    payload = sidecar.read_bytes()
    if len(payload) > 1024:
        raise ValueError("外层 SHA-256 sidecar 过大")
    try:
        text = payload.decode("ascii")
    except UnicodeDecodeError as exc:
        raise ValueError("外层 SHA-256 sidecar 不是 ASCII") from exc
    if "\r" in text:
        raise ValueError("外层 SHA-256 sidecar 包含非法换行")
    lines = text[:-1].split("\n") if text.endswith("\n") else text.split("\n")
    if len(lines) != 1 or not lines[0]:
        raise ValueError("外层 SHA-256 sidecar 必须恰好一行")
    match = SIDECAR_PATTERN.fullmatch(lines[0])
    if match is None or match.group(3) != archive_name:
        raise ValueError("外层 SHA-256 sidecar 文件名与归档不一致")
    return match.group(1)


def hash_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def member_parts(member: tarfile.TarInfo, expected_root: str) -> tuple[str, ...]:
    name = member.name.rstrip("/")
    if not name or member.name.startswith("/") or "\\" in member.name:
        raise ValueError(f"tar 成员路径无效: {member.name!r}")
    if MEMBER_PATTERN.fullmatch(name) is None:
        raise ValueError(f"tar 成员路径字符无效: {member.name!r}")
    parts = tuple(name.split("/"))
    if any(part in {"", ".", ".."} for part in parts) or parts[0] != expected_root:
        raise ValueError(f"tar 成员越界: {member.name!r}")
    return parts


def audit_members(bundle: tarfile.TarFile, expected_root: str) -> list[tuple[tarfile.TarInfo, tuple[str, ...]]]:
    audited: list[tuple[tarfile.TarInfo, tuple[str, ...]]] = []
    seen: set[tuple[str, ...]] = set()
    total_size = 0
    root_is_directory = False
    for count, member in enumerate(bundle, start=1):
        if count > MAX_MEMBERS:
            raise ValueError("tar 成员数量无效")
        parts = member_parts(member, expected_root)
        if parts in seen:
            raise ValueError(f"tar 包含重复成员: {member.name}")
        if not member.isdir() and not member.isreg():
            raise ValueError(f"tar 包含链接或特殊成员: {member.name}")
        if member.size < 0 or (member.isdir() and member.size != 0):
            raise ValueError(f"tar 成员大小无效: {member.name}")
        seen.add(parts)
        total_size += member.size
        root_is_directory = root_is_directory or (parts == (expected_root,) and member.isdir())
        audited.append((member, parts))
    if not audited or not root_is_directory or total_size > MAX_EXTRACTED_BYTES:
        raise ValueError("tar 根目录缺失或展开大小超限")
    return audited


def extract_members(
    bundle: tarfile.TarFile,
    members: list[tuple[tarfile.TarInfo, tuple[str, ...]]],
    extract_root: Path,
) -> None:
    extract_root.mkdir(parents=True, exist_ok=True)
    if any(extract_root.iterdir()):
        raise ValueError("解包目录必须为空")
    directories: list[tuple[Path, int]] = []
    for member, parts in members:
        target = extract_root.joinpath(*parts)
        if member.isdir():
            target.mkdir(parents=True, exist_ok=True)
            directories.append((target, member.mode & 0o777))
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        source = bundle.extractfile(member)
        if source is None:
            raise ValueError(f"tar 普通文件不可读取: {member.name}")
        with source, target.open("xb") as destination:
            shutil.copyfileobj(source, destination, 1024 * 1024)
        target.chmod(member.mode & 0o777)
    for target, mode in reversed(directories):
        target.chmod(mode)


def validate_and_extract(archive: Path, sidecar: Path, expected_root: str, extract_root: Path) -> None:
    require_regular_file(archive, "发布归档")
    require_regular_file(sidecar, "外层 SHA-256 sidecar")
    if archive.name != expected_root + ".tar.gz" or ROOT_PATTERN.fullmatch(expected_root) is None:
        raise ValueError("发布归档文件名或根目录名称无效")
    if archive.stat().st_size > MAX_ARCHIVE_BYTES:
        raise ValueError("发布归档大小或根目录名称无效")
    expected_digest = read_expected_digest(sidecar, archive.name)
    if not hmac.compare_digest(hash_file(archive), expected_digest):
        raise ValueError("发布归档外层 SHA-256 不一致")
    try:
        with tarfile.open(archive, mode="r:gz") as bundle:
            members = audit_members(bundle, expected_root)
            extract_members(bundle, members, extract_root)
    except (OSError, tarfile.TarError) as exc:
        raise ValueError(f"发布归档格式无效: {exc}") from exc


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--sidecar", type=Path, required=True)
    parser.add_argument("--expected-root", required=True)
    parser.add_argument("--extract-root", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    try:
        validate_and_extract(
            arguments.archive,
            arguments.sidecar,
            arguments.expected_root,
            arguments.extract_root,
        )
    except (OSError, ValueError) as exc:
        print(f"发布归档校验失败: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
