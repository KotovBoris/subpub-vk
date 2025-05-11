package main

import (
	"flag"
	"log"
	"os"

	"github.com/KotovBoris/subpub-vk/internal/app"
)

var configPath string

func init() {
	flag.StringVar(&configPath, "config", "", "Path to configuration file")
}

func main() {
	flag.Parse()

	if err := app.Run(configPath); err != nil {
		log.Printf("Application failed: %v", err)
		os.Exit(1)
	}

	log.Println("Application stopped gracefully.")
	os.Exit(0)
}
