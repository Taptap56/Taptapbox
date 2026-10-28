// singboxlib.go — TaptapBox library: sing-box jalan di dalam proses taptapbox,
// bukan sebagai biner terpisah. Pola ini sama dengan cmd/sing-box run/check:
//   ctx = include.Context(service.ExtendContext(ctx))
//   opts = json.UnmarshalExtendedContext[option.Options](ctx, configJSON)
//   inst = box.New(box.Options{Context: ctx, Options: opts}); inst.Start(); inst.Close()
// Check = box.New lalu Close tanpa Start (sama seperti cmd check).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

const singboxLibraryVersion = "1.14.3"

type singboxLogWriter struct{ w *capLogWriter }

func (s *singboxLogWriter) WriteMessage(level sblog.Level, message string) {
	if s.w == nil {
		return
	}
	_, _ = s.w.Write([]byte(message + "\n"))
}

func parseSingboxConfig(data []byte) (option.Options, context.Context, error) {
	ctx := include.Context(service.ExtendContext(context.Background()))
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return opts, ctx, err
	}
	return opts, ctx, nil
}

// checkSingboxConfigFile: validasi file JSON config dengan library (box.New + Close).
func (a *App) checkSingboxConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	opts, ctx, err := parseSingboxConfig(data)
	if err != nil {
		return fmt.Errorf("config tidak valid: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inst, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return fmt.Errorf("config tidak valid: %v", err)
	}
	_ = inst.Close()
	return nil
}

// checkSingboxConfigBytes: validasi config dari bytes (dipakai tes unit).
func checkSingboxConfigBytes(data []byte) error {
	opts, ctx, err := parseSingboxConfig(data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inst, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return err
	}
	return inst.Close()
}
