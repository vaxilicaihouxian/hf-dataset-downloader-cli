#!/usr/bin/env python3
"""交互式下载 Hugging Face Dataset Viewer 中的数据为 JSONL。"""

import argparse
import json
import os
import re
import sys
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


API_BASE = "https://datasets-server.huggingface.co"
PAGE_SIZE = 100  # Dataset Viewer API 单次最多返回 100 条。


def choose(prompt: str, options: list[str], default: int = 0) -> int:
    """显示选项，返回用户选择的下标。"""
    print(f"\n{prompt}")
    for index, option in enumerate(options, start=1):
        suffix = "（默认）" if index == default + 1 else ""
        print(f"  {index}. {option}{suffix}")

    while True:
        answer = input(f"请输入编号 [默认 {default + 1}]: ").strip()
        if not answer:
            return default
        try:
            index = int(answer) - 1
            if 0 <= index < len(options):
                return index
        except ValueError:
            pass
        print("输入无效，请输入列表中的编号。")


def get_token() -> str | None:
    """优先使用环境变量，其次读取 `hf auth login` 保存的 Token。"""
    if token := os.environ.get("HF_TOKEN"):
        return token

    token_path = os.environ.get("HF_TOKEN_PATH")
    if token_path:
        paths = [Path(token_path)]
    else:
        cache_root = os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))
        hf_home = Path(os.environ.get("HF_HOME", Path(cache_root) / "huggingface"))
        paths = [hf_home / "token"]

    for path in paths:
        try:
            if token := path.read_text(encoding="utf-8").strip():
                return token
        except OSError:
            continue
    return None


def request_json(path: str, params: dict[str, Any], timeout: int) -> dict[str, Any]:
    url = f"{API_BASE}{path}?{urlencode(params)}"
    headers = {"User-Agent": "hf-dataset-sample-downloader/1.0"}
    if token := get_token():
        headers["Authorization"] = f"Bearer {token}"

    try:
        with urlopen(Request(url, headers=headers), timeout=timeout) as response:
            return json.load(response)
    except HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {error.code}: {detail}") from error
    except URLError as error:
        raise RuntimeError(f"网络请求失败：{error.reason}") from error


def safe_name(text: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "_", text)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="选择 Hugging Face 数据集的 split，并下载 100、1k 或全部记录。"
    )
    parser.add_argument("dataset", help="数据集 ID，例如 HuggingFaceH4/ultrachat_200k")
    parser.add_argument("--config", help="只显示指定配置（subset）下的 split")
    parser.add_argument("-o", "--output", help="输出 JSONL 文件路径")
    parser.add_argument("--timeout", type=int, default=60, help="单次网络请求超时秒数，默认 60")
    args = parser.parse_args()

    try:
        split_response = request_json("/splits", {"dataset": args.dataset}, args.timeout)
        splits = split_response.get("splits", [])
        if args.config:
            splits = [item for item in splits if item.get("config") == args.config]
        if not splits:
            config_tip = f"（config: {args.config}）" if args.config else ""
            raise RuntimeError(f"没有找到可用 split {config_tip}")

        labels = []
        for item in splits:
            total = item.get("num_rows")
            total_text = f"，{total:,} 条" if isinstance(total, int) else ""
            labels.append(f"{item['config']} / {item['split']}{total_text}")
        selected = splits[choose("可用 config / split：", labels)]

        amount_labels = ["100 条", "1k 条", "全部"]
        amount = amount_labels[choose("下载数量：", amount_labels)]
        requested = {"100 条": 100, "1k 条": 1000, "全部": None}[amount]
        available = selected.get("num_rows")
        if requested is None:
            target = available if isinstance(available, int) else None
        elif isinstance(available, int):
            target = min(requested, available)
        else:
            target = requested

        count_name = "full" if requested is None else str(requested)
        default_name = (
            f"{safe_name(args.dataset)}_{safe_name(selected['config'])}_"
            f"{safe_name(selected['split'])}_{count_name}.jsonl"
        )
        output = Path(args.output) if args.output else Path.cwd() / default_name
        output.parent.mkdir(parents=True, exist_ok=True)

        target_text = f"{target:,}" if isinstance(target, int) else "未知总数"
        print(f"\n开始下载：目标 {target_text} 条，单次最多请求 {PAGE_SIZE} 条。")

        written = 0
        with output.open("w", encoding="utf-8") as file:
            while target is None or written < target:
                length = PAGE_SIZE if target is None else min(PAGE_SIZE, target - written)
                print(f"正在请求第 {written + 1} 到 {written + length} 条……", flush=True)
                rows_response = request_json(
                    "/rows",
                    {
                        "dataset": args.dataset,
                        "config": selected["config"],
                        "split": selected["split"],
                        "offset": written,
                        "length": length,
                    },
                    args.timeout,
                )
                rows = rows_response.get("rows", [])
                if not rows:
                    break
                for item in rows:
                    file.write(json.dumps(item.get("row", item), ensure_ascii=False, default=str) + "\n")
                written += len(rows)
                print(f"下载进度：{written:,}/{target_text} 条", flush=True)
                if len(rows) < length:
                    break
    except KeyboardInterrupt:
        print("\n已停止。", file=sys.stderr)
        return 130
    except Exception as error:
        print(f"下载失败：{error}", file=sys.stderr)
        return 1

    print(f"完成：共写入 {written:,} 条")
    print(f"文件：{output.resolve()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
