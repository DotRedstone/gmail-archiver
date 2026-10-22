"""生成不含 Git 历史、测试数据和部署配置的可构建源码包。"""

import argparse
import gzip
from pathlib import Path
import tarfile


ROOT = Path(__file__).resolve().parents[1]
STATIC = ("flake.nix", "flake.lock", "go.mod", "go.sum")


def build(output: Path) -> None:
    files = [ROOT / name for name in STATIC]
    files.extend(sorted((ROOT / "cmd/server").glob("*.go")))
    files.extend(sorted(path for path in (ROOT / "internal").rglob("*.go") if not path.name.endswith("_test.go")))
    if not files or any(not path.is_file() or path.is_symlink() for path in files):
        raise ValueError("发布源码清单不完整或包含符号链接")
    output = output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("wb") as target, gzip.GzipFile(filename="", mode="wb", fileobj=target, mtime=0) as compressed, tarfile.open(fileobj=compressed, mode="w") as archive:
        for path in files:
            relative = path.relative_to(ROOT)
            info = archive.gettarinfo(str(path), f"gmail-archiver/{relative}")
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            info.mtime = 0
            info.mode = 0o644
            with path.open("rb") as content:
                archive.addfile(info, content)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    build(parser.parse_args().output)
