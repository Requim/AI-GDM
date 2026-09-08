package nccs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

const (
	DefaultDirectory       = "https://portal.nccs.nasa.gov/datashare/landslides/nrt/hazard/tif/"
	ProviderName           = "NASA NCCS"
	DatasetName            = "LHASA NRT"
	MaxFileBytes     int64 = 512 << 20
)

var filenamePattern = regexp.MustCompile(`^\d{8}T\d{4}\.tif$`)

// Provider 从 NCCS 真实近实时目录发现最新完整栅格，不混用预报文件。
type Provider struct {
	client     *httpclient.Client
	directory  *url.URL
	staleAfter time.Duration
}

// New 创建有界目录发现适配器；传输安全策略由共用 HTTP 客户端执行。
func New(client *httpclient.Client, directory string, staleAfter time.Duration) (*Provider, error) {
	if directory == "" {
		directory = DefaultDirectory
	}
	target, err := url.Parse(directory)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" ||
		target.Fragment != "" || !strings.HasSuffix(target.Path, "/") || staleAfter <= 0 || client == nil {
		return nil, fmt.Errorf("%w: NCCS 目录配置无效", domain.ErrInvalidInput)
	}
	return &Provider{client: client, directory: target, staleAfter: staleAfter}, nil
}

// DiscoverLatest 只解析同目录有效日期文件，并读取强修订与发布时间。
func (p *Provider) DiscoverLatest(ctx context.Context) (provenance.Artifact, error) {
	response, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodGet,
		URL: p.directory.String(), MaxBodyBytes: 2 << 20, RedirectPolicy: httpclient.RedirectDeny})
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("读取 NCCS 目录: %w", err)
	}
	reference, err := latestLink(response.Body, p.directory)
	if err != nil {
		return provenance.Artifact{}, err
	}
	metadata, err := p.inspect(ctx, reference)
	if err != nil {
		return provenance.Artifact{}, err
	}
	return p.artifact(reference, metadata), nil
}

func latestLink(body []byte, directory *url.URL) (string, error) {
	tokens := html.NewTokenizer(bytes.NewReader(body))
	var names []string
	for tokens.Next() != html.ErrorToken {
		token := tokens.Token()
		if token.Data != "a" {
			continue
		}
		for _, attribute := range token.Attr {
			if attribute.Key == "href" && validLink(directory, attribute.Val) {
				target, _ := directory.Parse(attribute.Val)
				names = append(names, path.Base(target.Path))
			}
		}
	}
	if tokens.Err() != io.EOF || len(names) == 0 {
		return "", fmt.Errorf("%w: NCCS 目录没有有效近实时文件", domain.ErrProviderUnavailable)
	}
	sort.Strings(names)
	target, _ := directory.Parse(names[len(names)-1])
	return target.String(), nil
}

func validLink(directory *url.URL, href string) bool {
	target, err := directory.Parse(href)
	if err != nil || target.Scheme != directory.Scheme || target.Host != directory.Host ||
		target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		strings.TrimRight(path.Dir(target.Path), "/")+"/" != directory.Path || !filenamePattern.MatchString(path.Base(target.Path)) {
		return false
	}
	_, err = time.Parse("20060102T1504.tif", path.Base(target.Path))
	return err == nil
}
