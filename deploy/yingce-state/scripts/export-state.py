#!/usr/bin/env python3
"""导出画布云端「渠道 / 模型 / 价格档 / 系统设置」为可入库的基线快照。

在画布服务器（或任意能 docker exec 到 postgres 容器的机器）上运行：

    python3 export-state.py /root/_state_export

产物：
    json/*.json                 逐行 JSON，便于 git diff 与人工比对
    sql/channels-data.sql.gz    覆盖式恢复用（TRUNCATE + INSERT）
    meta.json                   生成时间 / schema 版本 / 各表行数

所有凭据（`enc:v1:` 密文）在导出时被清空，恢复后需重新写入。
"""

import datetime
import glob
import json
import os
import re
import shutil
import subprocess
import sys

PG = [
    "docker", "exec", "open-ai-canvas-postgres", "psql",
    "-U", "open_ai_canvas", "-d", "open_ai_canvas", "-A", "-t",
]

TABLES = [
    "channel_websites",
    "model_channels",
    "channel_models",
    "channel_model_price_tiers",
    "logical_models",
    "system_settings",
]

SECRET_RE = re.compile(r"enc:v1:[A-Za-z0-9_\-=/+]+")


def q(sql):
    r = subprocess.run(PG + ["-c", sql], capture_output=True, text=True)
    if r.returncode:
        raise SystemExit("SQL 失败: " + r.stderr[:600])
    return r.stdout.strip()


def sh(cmd):
    return subprocess.run(cmd, shell=True, capture_output=True, text=True).stdout.strip()


def scrub(obj, counter):
    if isinstance(obj, dict):
        for k, v in list(obj.items()):
            if isinstance(v, str):
                nv, n = SECRET_RE.subn("REDACTED", v)
                if n:
                    obj[k] = nv
                    counter[0] += n
            else:
                scrub(v, counter)
    elif isinstance(obj, list):
        for v in obj:
            scrub(v, counter)


def main():
    out_dir = sys.argv[1] if len(sys.argv) > 1 else "/root/_state_export"

    data = {}
    for t in TABLES:
        rows = json.loads(q("SELECT coalesce(json_agg(row_to_json(x)), '[]'::json) FROM %s x" % t))
        data[t] = rows
        print("EXPORT %-28s %d rows" % (t, len(rows)))

    # 渠道密钥：整列清空（保留"原本有密钥"这一事实，便于恢复时核对）
    counter = [0]
    for r in data["model_channels"]:
        if r.get("api_key"):
            r["api_key"] = ""
            r["api_key_was_set"] = True
            counter[0] += 1

    for r in data["system_settings"]:
        try:
            vj = json.loads(r.get("value_json") or "{}")
            scrub(vj, counter)
            r["value_json"] = json.dumps(vj, ensure_ascii=False)
        except Exception:
            r["value_json"] = SECRET_RE.sub("REDACTED", r.get("value_json") or "")
    print("REDACTED tokens:", counter[0])

    shutil.rmtree(out_dir, ignore_errors=True)
    os.makedirs(os.path.join(out_dir, "json"), exist_ok=True)
    os.makedirs(os.path.join(out_dir, "sql"), exist_ok=True)

    # 逐行 JSON：一条记录一行，git diff 能精确到行
    for t, rows in data.items():
        rows.sort(key=lambda r: str(r.get("id") or r.get("key") or ""))
        path = os.path.join(out_dir, "json", t + ".json")
        with open(path, "w") as f:
            f.write("[\n")
            for i, r in enumerate(rows):
                line = "  " + json.dumps(r, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
                f.write(line + (",\n" if i < len(rows) - 1 else "\n"))
            f.write("]\n")

    # 覆盖式恢复用 SQL
    cmd = (
        "docker exec open-ai-canvas-postgres pg_dump -U open_ai_canvas -d open_ai_canvas "
        "--data-only --column-inserts --no-comments "
        + " ".join("-t public.%s" % t for t in TABLES)
    )
    body = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    if body.returncode != 0:
        raise SystemExit("pg_dump 失败: " + body.stderr[:600])

    header = (
        "-- 画布 渠道/模型/价格档/设置 数据快照（凭据已清空）\n"
        "-- 由 deploy/yingce-state/scripts/export-state.py 生成，勿手改\n"
        "-- 恢复：见 deploy/yingce-state/README.md\n\n"
    )
    truncate = (
        "TRUNCATE public.channel_model_price_tiers, public.channel_models,\n"
        "         public.model_channels, public.channel_websites,\n"
        "         public.logical_models, public.system_settings CASCADE;\n\n"
    )
    final = (
        header + "BEGIN;\n" + truncate
        + "SET session_replication_role = replica;\n\n"
        + SECRET_RE.sub("", body.stdout)
        + "\n\nSET session_replication_role = origin;\nCOMMIT;\n"
    )
    with open(os.path.join(out_dir, "sql", "channels-data.sql"), "w") as f:
        f.write(final)

    meta = {
        "generated_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "counts": {t: len(v) for t, v in data.items()},
        "containers": sh("docker ps --format '{{.Names}}={{.Image}}' | grep open-ai-canvas || true"),
        "notes": "api_key 已清空，system_settings 内密文已替换为 REDACTED；恢复后需重新写入。",
    }
    try:
        meta["schema_version"] = int(q("SELECT max(version) FROM schema_migrations"))
    except Exception:
        meta["schema_version"] = None
    with open(os.path.join(out_dir, "meta.json"), "w") as f:
        json.dump(meta, f, ensure_ascii=False, indent=1)

    print("DONE", out_dir, "schema", meta.get("schema_version"))


if __name__ == "__main__":
    main()
