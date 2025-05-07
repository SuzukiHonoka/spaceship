package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/v2/api"
	"github.com/SuzukiHonoka/spaceship/v2/internal/indicator"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/manifest"
	"golang.org/x/term"
	"log"
	"os"
	"time"
)

var (
	configPath        = flag.String("c", "./config.json", "config path")
	showVersion       = flag.Bool("v", false, "show spaceship version")
	showStats         = flag.Bool("s", false, "show stats")
	showStatsInterval = flag.Duration("interval", 1*time.Second, "show stats interval in seconds")
)

func init() {
	flag.Parse()
}

func main() {
	if *showVersion {
		fmt.Printf("spaceship v%s\n", manifest.VersionCode)
		return
	}

	// Prepare to launch
	launcher := api.NewLauncher()

	if *showStats {
		if term.IsTerminal(int(os.Stdout.Fd())) {
			// Skip internal logging
			launcher = launcher.WithSkipInternalLogging()

			ir := indicator.NewIndicator()
			defer utils.Close(ir)

			// Replace the default logger's output with our writer
			log.SetOutput(ir)

			// Show stats
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go showStatsFunc(ctx, *showStatsInterval, ir)
		} else {
			log.Printf("Unable to show stats, not a terminal.")
		}
	}

	// prompt
	_, _ = fmt.Fprintf(log.Writer(), "spaceship v%s, for personal use only, absolutely without any warranty, "+
		"any kind of illegal intention by using this program are strongly forbidden.\n", manifest.VersionCode)

	// default launch from file
	launcher.LaunchFromFile(*configPath)
}

func showStatsFunc(ctx context.Context, interval time.Duration, ir *indicator.Indicator) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			tx, rx := transport.GlobalStats.CalculateSpeed()
			totalTx, totalRx := transport.GlobalStats.Total()
			status := fmt.Sprintf("[↑ %8s/s ↓ %8s/s | Σ↑ %8s Σ↓ %8s]",
				utils.PrettyByteSize(tx),
				utils.PrettyByteSize(rx),
				utils.PrettyByteSize(float64(totalTx)),
				utils.PrettyByteSize(float64(totalRx)))
			ir.UpdateStatus(status)
		case <-ctx.Done():
			return
		}
	}
}
