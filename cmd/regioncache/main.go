// Command regioncache 在部署阶段预取并原子更新中文省市目录和 WGS84 边界。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/adapters/provider/amap"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/regioncache"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	verify := flag.Bool("verify", false, "仅校验本地缓存，不访问供应商")
	flag.Parse()
	path := filepath.Join(os.Getenv("LHASA_DATA_DIR"), "administrative", regioncache.Filename)
	if *verify {
		_, err := regioncache.Open(path, nil)
		return err
	}
	if os.Getenv("AMAP_API_KEY") == "" || os.Getenv("LHASA_DATA_DIR") == "" {
		return fmt.Errorf("需要服务端 AMAP_API_KEY 和 LHASA_DATA_DIR")
	}
	client := httpclient.New(httpclient.Options{Limiter: rate.NewLimiter(rate.Every(500*time.Millisecond), 1),
		MaxAttempts: 2})
	provider, err := amap.New(client, amap.Config{BaseURL: os.Getenv("AMAP_BASE_URL"),
		APIKey: os.Getenv("AMAP_API_KEY"), SecurityCode: os.Getenv("AMAP_JSCODE"), Timeout: 30*time.Second})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err = regioncache.Refresh(ctx, path, provider); err != nil {
		return err
	}
	log.Print("中文省市目录和 WGS84 边界全部校验完成，已原子更新本地缓存")
	return nil
}
