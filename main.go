package main

import (
	"os"

	"github.com/cgoncalveslck/go-api-ebening/bot"
)

func main() {
	bot.Token = os.Getenv("BOT_TOKEN")
	if bot.Token == "" {
		panic("BOT_TOKEN environment variable is required")
	}
	bot.Run()
}
