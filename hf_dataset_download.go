// hf_dataset_download is a dependency-free Hugging Face Dataset Viewer downloader.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	apiBase  = "https://datasets-server.huggingface.co"
	pageSize = 100 // Dataset Viewer API 每次最多返回 100 条。
)

type splitInfo struct {
	Config  string `json:"config"`
	Split   string `json:"split"`
	NumRows *int64 `json:"num_rows"`
}

type splitsResponse struct {
	Splits []splitInfo `json:"splits"`
}

type rowItem struct {
	Row json.RawMessage `json:"row"`
}

type rowsResponse struct {
	Rows []rowItem `json:"rows"`
}

func main() {
	config := flag.String("config", "", "只显示指定配置（subset）下的 split")
	output := flag.String("o", "", "输出 JSONL 文件路径")
	flag.StringVar(output, "output", "", "输出 JSONL 文件路径")
	timeout := flag.Int("timeout", 60, "单次网络请求超时秒数")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：%s [选项] <数据集 ID>\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(flag.Arg(0), *config, *output, time.Duration(*timeout)*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "下载失败：", err)
		os.Exit(1)
	}
}

func run(dataset, config, output string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	var response splitsResponse
	if err := getJSON(client, "/splits", url.Values{"dataset": {dataset}}, &response); err != nil {
		return err
	}

	splits := make([]splitInfo, 0, len(response.Splits))
	for _, item := range response.Splits {
		if config == "" || item.Config == config {
			splits = append(splits, item)
		}
	}
	if len(splits) == 0 {
		if config == "" {
			return fmt.Errorf("未找到可用 split；该数据集可能未启用 Dataset Viewer")
		}
		return fmt.Errorf("config %q 下没有可用 split", config)
	}

	labels := make([]string, len(splits))
	for i, item := range splits {
		count := ""
		if item.NumRows != nil {
			count = fmt.Sprintf("，%d 条", *item.NumRows)
		}
		labels[i] = fmt.Sprintf("%s / %s%s", item.Config, item.Split, count)
	}
	selectedIndex, err := choose("可用 config / split：", labels, 0)
	if err != nil {
		return err
	}
	selected := splits[selectedIndex]

	amounts := []string{"100 条", "1k 条", "全部"}
	amountIndex, err := choose("下载数量：", amounts, 0)
	if err != nil {
		return err
	}
	requested := int64(100)
	full := amountIndex == 2
	if amountIndex == 1 {
		requested = 1000
	}

	// target < 0 表示 Viewer 未提供总行数，下载到服务端返回空页为止。
	target := requested
	if full {
		target = -1
		if selected.NumRows != nil {
			target = *selected.NumRows
		}
	} else if selected.NumRows != nil && *selected.NumRows < target {
		target = *selected.NumRows
	}

	if output == "" {
		countName := strconv.FormatInt(requested, 10)
		if full {
			countName = "full"
		}
		output = fmt.Sprintf("%s_%s_%s_%s.jsonl", safeName(dataset), safeName(selected.Config), safeName(selected.Split), countName)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil && filepath.Dir(output) != "." {
		return err
	}

	targetText := "未知总数"
	if target >= 0 {
		targetText = strconv.FormatInt(target, 10)
	}
	fmt.Printf("\n开始下载：目标 %s 条，单次最多请求 %d 条。\n", targetText, pageSize)

	file, err := os.Create(output)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	var written int64
	for target < 0 || written < target {
		length := int64(pageSize)
		if target >= 0 && target-written < length {
			length = target - written
		}
		fmt.Printf("正在请求第 %d 到 %d 条……\n", written+1, written+length)

		var page rowsResponse
		err := getJSON(client, "/rows", url.Values{
			"dataset": {dataset},
			"config":  {selected.Config},
			"split":   {selected.Split},
			"offset":  {strconv.FormatInt(written, 10)},
			"length":  {strconv.FormatInt(length, 10)},
		}, &page)
		if err != nil {
			if written != 0 {
				return err
			}
			fmt.Printf("Dataset Viewer 不可用，尝试直接读取原始 JSONL……\n")
			fallbackWritten, rawPath, fallbackErr := downloadRawJSONL(client, dataset, selected, target, writer)
			if fallbackErr != nil {
				return fmt.Errorf("Dataset Viewer 失败：%v；原始 JSONL 回退也失败：%w", err, fallbackErr)
			}
			written = fallbackWritten
			fmt.Printf("已从原始文件 %s 写入 %d 条\n", rawPath, written)
			break
		}
		if len(page.Rows) == 0 {
			break
		}
		for _, item := range page.Rows {
			if _, err := writer.Write(item.Row); err != nil {
				return err
			}
			if err := writer.WriteByte('\n'); err != nil {
				return err
			}
		}
		written += int64(len(page.Rows))
		fmt.Printf("下载进度：%d/%s 条\n", written, targetText)
		if int64(len(page.Rows)) < length {
			break
		}
	}

	if err := writer.Flush(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("完成：共写入 %d 条\n文件：%s\n", written, absolutePath(output))
	return nil
}

func choose(prompt string, options []string, defaultIndex int) (int, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("\n%s\n", prompt)
	for i, option := range options {
		defaultLabel := ""
		if i == defaultIndex {
			defaultLabel = "（默认）"
		}
		fmt.Printf("  %d. %s%s\n", i+1, option, defaultLabel)
	}

	for {
		fmt.Printf("请输入编号 [默认 %d]: ", defaultIndex+1)
		text, err := reader.ReadString('\n')
		if err == io.EOF && strings.TrimSpace(text) == "" {
			return defaultIndex, nil
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return defaultIndex, nil
		}
		index, err := strconv.Atoi(text)
		if err == nil && index >= 1 && index <= len(options) {
			return index - 1, nil
		}
		fmt.Println("输入无效，请输入列表中的编号。")
	}
}

func getJSON(client *http.Client, path string, values url.Values, destination any) error {
	req, err := http.NewRequest(http.MethodGet, apiBase+path+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "hf-dataset-sample-downloader/1.0")
	if token := hfToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(destination)
}

// downloadRawJSONL is a fallback for datasets whose Dataset Viewer cannot build
// a preview. It reads the source stream and closes the HTTP response as soon as
// the requested number of JSONL records has been written.
func downloadRawJSONL(client *http.Client, dataset string, selected splitInfo, target int64, writer *bufio.Writer) (int64, string, error) {
	paths := []string{
		"data/" + selected.Split + ".jsonl",
		selected.Split + ".jsonl",
		selected.Config + "/" + selected.Split + ".jsonl",
	}

	var lastError error
	for _, path := range paths {
		response, err := openRawFile(client, dataset, path)
		if err != nil {
			lastError = err
			continue
		}
		defer response.Body.Close()

		reader := bufio.NewReader(response.Body)
		var written int64
		for target < 0 || written < target {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				line = bytesTrimLineEnding(line)
				if len(line) > 0 {
					if _, err := writer.Write(line); err != nil {
						return written, path, err
					}
					if err := writer.WriteByte('\n'); err != nil {
						return written, path, err
					}
					written++
					if written == 1 || written%100 == 0 || (target >= 0 && written == target) {
						fmt.Printf("下载进度：%d 条\n", written)
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return written, path, readErr
			}
		}
		return written, path, nil
	}
	if lastError == nil {
		lastError = fmt.Errorf("未找到 JSONL 原始文件")
	}
	return 0, "", lastError
}

func openRawFile(client *http.Client, dataset, path string) (*http.Response, error) {
	parts := strings.Split(dataset, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("无效的数据集 ID：%q", dataset)
	}
	urlPath := "https://huggingface.co/datasets/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/resolve/main/"
	for _, part := range strings.Split(path, "/") {
		urlPath += url.PathEscape(part) + "/"
	}
	urlPath = strings.TrimSuffix(urlPath, "/")

	req, err := http.NewRequest(http.MethodGet, urlPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hf-dataset-sample-downloader/1.0")
	if token := hfToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		response.Body.Close()
		return nil, fmt.Errorf("%s：HTTP %s: %s", path, response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func bytesTrimLineEnding(value []byte) []byte {
	return bytes.TrimRight(value, "\r\n")
}

func hfToken() string {
	if token := strings.TrimSpace(os.Getenv("HF_TOKEN")); token != "" {
		return token
	}
	path := os.Getenv("HF_TOKEN_PATH")
	if path == "" {
		cacheRoot := os.Getenv("XDG_CACHE_HOME")
		if cacheRoot == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			cacheRoot = filepath.Join(home, ".cache")
		}
		hfHome := os.Getenv("HF_HOME")
		if hfHome == "" {
			hfHome = filepath.Join(cacheRoot, "huggingface")
		}
		path = filepath.Join(hfHome, "token")
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(token))
}

func safeName(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, text)
}

func absolutePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
