package api

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/tun"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

// tunSession is one dynamically attached TUN frontend. The config-time TUN
// stays inside launchClient and is recorded with tunEmbedded instead.
type tunSession struct {
	service *tun.Service
	done    chan struct{}
}

func (l *Launcher) beginRun(ctx context.Context, blockIPv6DNS bool, embeddedTUN bool) {
	l.tunMu.Lock()
	l.runCtx = ctx
	l.blockIPv6DNS = blockIPv6DNS
	l.tunEmbedded = embeddedTUN
	l.tunMu.Unlock()
}

func (l *Launcher) clearRunState() {
	l.StopTUN()
	l.tunMu.Lock()
	l.runCtx = nil
	l.tunEmbedded = false
	l.tunMu.Unlock()
}

// ServeTUN adopts a TUN device on a client that is already inside Launch.
// The proxy listeners keep running. StopTUN releases only this device, and
// Launcher.Stop releases it as the client exits. A second attachment fails.
//
// On Android, raw.FileDescriptor is the VpnService fd. Spaceship duplicates
// it and closes only the duplicate. An omitted or zero bypass mark installs
// no SO_MARK there.
//
// ServeTUN does not change the outbound socket mark: it is process-wide and
// already applied to live sockets and the resolver. A TUN that needs a mark,
// which is every Linux TUN and an explicit Android mark, is refused unless
// the running client installed exactly that mark at startup. Declare the tun
// section in the launch config, or a matching redirect.bypass_mark, so the
// mark is set before any socket is opened.
func (l *Launcher) ServeTUN(raw *client.TUN) error {
	if raw == nil {
		return errors.New("tun: nil config")
	}

	l.tunMu.Lock()
	if l.tunEmbedded || l.tunSess != nil {
		l.tunMu.Unlock()
		return errors.New("tun: already running")
	}
	ctx := l.runCtx
	blockIPv6 := l.blockIPv6DNS
	if ctx == nil {
		l.tunMu.Unlock()
		return errors.New("tun: client is not running")
	}
	l.tunMu.Unlock()

	cfg, err := tun.FromClientConfig(raw, blockIPv6)
	if err != nil {
		return err
	}
	if err := checkInstalledBypassMark(cfg, transport.BypassMark()); err != nil {
		return err
	}
	service, err := tun.New(ctx, cfg)
	if err != nil {
		return err
	}

	l.tunMu.Lock()
	if l.runCtx == nil || l.tunEmbedded || l.tunSess != nil {
		l.tunMu.Unlock()
		_ = service.Close()
		if l.runCtx == nil {
			return errors.New("tun: client is not running")
		}
		return errors.New("tun: already running")
	}
	session := &tunSession{service: service, done: make(chan struct{})}
	l.tunSess = session
	l.tunMu.Unlock()

	go func() {
		defer close(session.done)
		if err := service.Run(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("tun: stopped: %v", err)
		}
		l.tunMu.Lock()
		if l.tunSess == session {
			l.tunSess = nil
		}
		l.tunMu.Unlock()
	}()
	return nil
}

// checkInstalledBypassMark refuses a TUN whose required mark differs from the
// one installed. Without it, a policy-routed TUN would capture Spaceship's own
// egress and loop it back into the device.
func checkInstalledBypassMark(cfg tun.Config, installed uint32) error {
	if !cfg.BypassMarkRequired || installed == cfg.BypassMark {
		return nil
	}
	return fmt.Errorf(
		"tun: requires outbound socket mark %#x, but the running client installed %#x; "+
			"the mark is process-wide and set only at launch, so declare tun in the launch "+
			"config (or a matching redirect.bypass_mark) instead of attaching it later",
		cfg.BypassMark,
		installed,
	)
}

// StopTUN closes a device attached with ServeTUN and waits until its run
// loop has finished. It is a no-op when no dynamic TUN is attached.
func (l *Launcher) StopTUN() {
	l.tunMu.Lock()
	session := l.tunSess
	l.tunSess = nil
	l.tunMu.Unlock()
	if session == nil {
		return
	}
	_ = session.service.Close()
	<-session.done
}
