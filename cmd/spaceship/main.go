package main

import (
	"flag"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/api"
	"github.com/SuzukiHonoka/spaceship/pkg/config/manifest"
)

var (
	configPath  = flag.String("c", "./config.json", "config path")
	showVersion = flag.Bool("v", false, "show spaceship version")
)

func init() {
	flag.Parse()
}

func main() {
	if *showVersion {
		fmt.Printf("spaceship v%s\n", manifest.VersionCode)
		return
	}
	// prompt
	fmt.Printf("spaceship v%s, for personal use only, absolutely without any warranty, any kind of illegal intention by using this program are strongly forbidden.\n", manifest.VersionCode)

	// default launch from file
	api.NewLauncher().LaunchFromFile(*configPath)
}
