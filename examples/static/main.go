package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/BinaryBinx/bingo/core"
	"github.com/BinaryBinx/bingo/middleware"
)

func main() {
	root := flag.String("root", "./public", "静态文件根目录（必须存在）")
	immutable := flag.Bool("immutable", false, "仅用于内容不可变的版本化 URL，快照有效期内跳过文件检查")
	flag.Parse()
	if err := serve(*root, *immutable); err != nil {
		log.Fatal(err)
	}
}

func serve(root string, immutable bool) error {
	files, err := middleware.NewStaticHandler(root, middleware.StaticConfig{
		TTL:       time.Minute,
		Immutable: immutable,
		ETag:      middleware.WeakStaticETag,
	})
	if err != nil {
		return err
	}
	config := core.DefaultConfig()
	config.RunMode = core.RunModeRelease
	app := core.NewApp(config)
	// OnShutdown owns cleanup after registration. An unconditional deferred Close
	// would release the root too early if Run returns before late work drains.
	if err := app.OnShutdown(func(context.Context) error { return files.Close() }); err != nil {
		files.Close()
		return err
	}
	app.Use(middleware.Recovery())
	app.Use(middleware.ConcurrencyLimit(128))
	app.Use(middleware.CompressWithConfig(middleware.CompressConfig{Level: 1}))
	app.Use(files.Middleware)
	return app.Run()
}
