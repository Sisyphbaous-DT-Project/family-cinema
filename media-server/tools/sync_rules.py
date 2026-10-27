#!/usr/bin/env python3
"""从 KazumiRules 仓库同步规则到 media-server/rules/。

在开发机运行，下载 index.json 以及其中列出的每条规则 JSON。
需要代理时设置 HTTPS_PROXY 环境变量。盒子不联网拉规则，部署时用 adb push 整个 rules/ 目录。

用法：python tools/sync_rules.py
"""
import json
import os
import sys
import urllib.request
from concurrent.futures import ThreadPoolExecutor

RAW = "https://raw.githubusercontent.com/Predidit/KazumiRules/main/"
PROXY = os.environ.get("HTTPS_PROXY")
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "rules")

opener = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": PROXY, "https": PROXY})
) if PROXY else urllib.request.build_opener()


def get(name):
    req = urllib.request.Request(RAW + name, headers={"User-Agent": "family-cinema-sync/1.0"})
    return opener.open(req, timeout=30).read()


def main():
    os.makedirs(OUT, exist_ok=True)
    index_raw = get("index.json")
    index = json.loads(index_raw.decode("utf-8"))
    names = [x["name"] for x in index]

    def fetch(name):
        err = None
        for _ in range(3):  # 代理偶发 SSL EOF，重试几次
            try:
                return name, get(name + ".json"), None
            except Exception as e:  # noqa: BLE001 - 脚本里只需要打印失败原因
                err = str(e)
        return name, None, err

    changed, same, failed = [], [], []
    with ThreadPoolExecutor(8) as ex:
        for name, data, err in ex.map(fetch, names):
            if err:
                failed.append((name, err))
                continue
            path = os.path.join(OUT, name + ".json")
            old = open(path, "rb").read() if os.path.exists(path) else None
            if old == data:
                same.append(name)
            else:
                open(path, "wb").write(data)
                changed.append(name)

    open(os.path.join(OUT, "index.json"), "wb").write(index_raw)
    open(os.path.join(OUT, "LICENSE"), "wb").write(get("LICENSE"))
    with open(os.path.join(OUT, "SOURCE.txt"), "w", encoding="utf-8") as f:
        f.write("规则来自 https://github.com/Predidit/KazumiRules （MIT），由 tools/sync_rules.py 同步。\n")

    print("total=%d changed=%d same=%d failed=%d" % (len(names), len(changed), len(same), len(failed)))
    for n in changed:
        print("  updated:", n)
    for n, e in failed:
        print("  FAILED:", n, e)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
