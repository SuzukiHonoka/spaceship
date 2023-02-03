package spaceship

import (
	"flag"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/api"
	"github.com/SuzukiHonoka/spaceship/internal/config/manifest"
)

func Main() {
	// first prompt
	fmt.Printf("spaceship v%s ", manifest.VersionCode)
	fmt.Println("for personal use only, absolutely without any warranty, any kind of illegal intention by using this program are strongly forbidden.")
	// load configuration
	configPath := flag.String("c", "./config.json", "config path")
	flag.Parse()
	api.LaunchFromFile(*configPath)
}
