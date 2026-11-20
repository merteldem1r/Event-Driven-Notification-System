// Command loadtest sends many notifications on every channel at once through the API and
// checks that the provider never received more than its limit in any one second (D17). Run it
// with `make loadtest`, which starts several workers against the fake provider first.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/loadtest"
)

func main() {
	api := flag.String("api", "http://localhost:8080", "API address")
	fake := flag.String("fake", "http://localhost:9090", "address of the fake provider the workers send to")
	perChannel := flag.Int("per-channel", 1000, "notifications per channel")
	maxPerSecond := flag.Int("max", 100, "the provider's limit: most messages per channel it may receive in any one second")
	timeout := flag.Duration("timeout", 3*time.Minute, "how long to wait for every notification to finish")
	flag.Parse()

	if err := run(loadtest.Config{APIURL: *api, FakeURL: *fake, PerChannel: *perChannel, Timeout: *timeout}, *maxPerSecond); err != nil {
		fmt.Fprintln(os.Stderr, "load test failed:", err)
		os.Exit(1)
	}
}

// run performs the load test and returns an error if the provider got more than maxPerSecond
// in one second on any channel, or if not every notification was sent.
func run(cfg loadtest.Config, maxPerSecond int) error {
	fmt.Printf("sending %d notifications on each channel through %s\n", cfg.PerChannel, cfg.APIURL)
	start := time.Now()
	results, err := loadtest.Run(context.Background(), &http.Client{Timeout: 30 * time.Second}, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("all finished after %.1fs\n\n", time.Since(start).Seconds())

	fmt.Printf("%-6s %5s %7s %10s %22s %21s   %s\n",
		"", "sent", "failed", "avg rate", "busiest 1s at provider", "busiest 1s recorded", "recorded per second")
	broken := false
	for _, r := range results {
		mark := "ok"
		if r.AtProvider > maxPerSecond {
			mark, broken = "OVER", true
		}
		if r.Failed > 0 || r.Sent != cfg.PerChannel {
			broken = true
		}
		fmt.Printf("%-6s %5d %7d %8.1f/s %17d (%s) %21d   %v\n",
			r.Channel, r.Sent, r.Failed, r.Rate, r.AtProvider, mark, r.Busiest, r.PerSecond)
	}
	fmt.Printf("\nprovider limit: at most %d per channel in any one second\n", maxPerSecond)
	if broken {
		return fmt.Errorf("the provider got more than %d in one second, or not every notification was sent", maxPerSecond)
	}
	return nil
}
