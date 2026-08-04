# Hugging Face Dataset Downloader CLI

交互式选择 Hugging Face 数据集的 config / split，并下载 100 条（默认）、1k 条或全部记录为 JSONL。

它使用 [Hugging Face Dataset Viewer API](https://huggingface.co/docs/dataset-viewer/en/quick_start)，因此不会先把整个数据集下载到本地。目标数据集需要启用 Dataset Viewer。

## Go（二进制）

下载 release 中适合 Linux AMD64（Debian x86_64）的文件后：

```bash
chmod +x hf-dataset-download_linux_amd64
./hf-dataset-download_linux_amd64 HuggingFaceH4/ultrachat_200k
```

从源码构建（仅 Go 标准库、无第三方依赖）：

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o hf-dataset-download hf_dataset_download.go
```

## Python

仅使用 Python 标准库：

```bash
python3 hf_dataset_download.py HuggingFaceH4/ultrachat_200k
```

## 认证

公开数据集可以匿名使用。对于私有或 gated 数据集，先执行 `hf auth login`；两个版本都会读取该登录保存的 Token，也支持 `HF_TOKEN` 环境变量。
