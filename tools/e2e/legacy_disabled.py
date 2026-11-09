"""Fail closed before historical scripts can touch a browser or a database."""


def retire_unsafe_script(path):
    raise SystemExit(
        f"历史验收脚本已停用：{path}。旧默认库、时间范围清理及既有任务操作不再允许。"
        "请使用 tools/e2e/README.md 中的 C6 隔离夹具入口；历史源码仅保留作兼容覆盖参考。"
    )
